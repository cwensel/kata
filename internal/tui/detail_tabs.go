package tui

import (
	"fmt"
	"strings"
)

// renderCommentsTab formats the comments tab as: header line plus per-
// comment "[author] timestamp" header (selected row inverse-styled),
// body line-wrapped to width with a 2-space indent, blank separator.
// The cursor highlights the row under dm.tabCursor.
func renderCommentsTab(cs []CommentEntry, width, height, cursor int) string {
	out := []string{titleStyle.Render(fmt.Sprintf("Comments (%d)", len(cs)))}
	if len(cs) == 0 {
		out = append(out, statusStyle.Render("(no comments yet)"))
		return clipTab(out, width, height)
	}
	for i, c := range cs {
		header := fmt.Sprintf("[%s] %s", c.Author, fmtTime(c.CreatedAt))
		out = append(out, applyRowCursor(header, i == cursor))
		for _, ln := range wrapBody(c.Body, max(1, width-2)) {
			out = append(out, "  "+ln)
		}
		out = append(out, "")
	}
	return clipTab(out, width, height)
}

// renderEventsTab formats one line per event:
// "[type] timestamp actor — description". The description is type-
// specific (e.g., "labeled bug", "linked #7"). Cursor highlights the
// row under dm.tabCursor.
func renderEventsTab(es []EventLogEntry, width, height, cursor int) string {
	out := []string{titleStyle.Render(fmt.Sprintf("Events (%d)", len(es)))}
	if len(es) == 0 {
		out = append(out, statusStyle.Render("(no events yet)"))
		return clipTab(out, width, height)
	}
	for i, e := range es {
		line := fmt.Sprintf("[%s] %s %s — %s",
			e.Type, fmtTime(e.CreatedAt), e.Actor, eventDescription(e))
		out = append(out, applyRowCursor(line, i == cursor))
	}
	return clipTab(out, width, height)
}

// renderLinksTab formats one line per link:
// "[type] → #ToN ← #FromN  by author @ timestamp". The "(open|closed)"
// status is not on the LinkEntry projection; pressing Enter on a link
// jumps to the target so the user can see the title and status there.
func renderLinksTab(ls []LinkEntry, width, height, cursor int) string {
	out := []string{titleStyle.Render(fmt.Sprintf("Links (%d)", len(ls)))}
	if len(ls) == 0 {
		out = append(out, statusStyle.Render("(no links)"))
		return clipTab(out, width, height)
	}
	for i, l := range ls {
		line := fmt.Sprintf("[%s] → #%d ← #%d  by %s @ %s",
			l.Type, l.ToNumber, l.FromNumber, l.Author, fmtTime(l.CreatedAt))
		out = append(out, applyRowCursor(line, i == cursor))
	}
	return clipTab(out, width, height)
}

// applyRowCursor returns line wrapped in selectedStyle when isCursor.
// Centralizing the cursor application here keeps each tab renderer
// readable.
func applyRowCursor(line string, isCursor bool) string {
	if isCursor {
		return selectedStyle.Render(line)
	}
	return line
}

// clipTab truncates lines to width and caps the slice at height. Empty
// input or zero height is an empty render so the layout doesn't shift.
func clipTab(lines []string, width, height int) string {
	if height < 1 {
		return ""
	}
	if len(lines) > height {
		lines = lines[:height]
	}
	out := make([]string, 0, len(lines))
	for _, ln := range lines {
		out = append(out, truncate(ln, width))
	}
	return strings.Join(out, "\n")
}
