package tui

import (
	"fmt"
	"strings"
)

// tabState carries the per-tab loading / error markers from the model
// to the renderer. A non-nil err takes priority over loading; both
// short-circuit the entry-list path so the user gets a clear hint.
type tabState struct {
	loading bool
	err     error
}

// renderCommentsTab formats the comments tab as: header line plus per-
// comment "[author] timestamp" header (selected row inverse-styled),
// body line-wrapped to width with a 2-space indent, blank separator.
// The cursor highlights the row under dm.tabCursor and the rendered
// lines are windowed so the cursor entry is always visible even when
// the entries don't all fit in height.
func renderCommentsTab(cs []CommentEntry, width, height, cursor int, ts tabState) string {
	headers := []string{titleStyle.Render(fmt.Sprintf("Comments (%d)", len(cs)))}
	if placeholder := tabPlaceholder(ts, "comments", "(no comments yet)", len(cs)); placeholder != nil {
		return assembleTab(headers, []entryChunk{*placeholder}, width, height, -1)
	}
	chunks := make([]entryChunk, 0, len(cs))
	for i, c := range cs {
		header := fmt.Sprintf("[%s] %s", c.Author, fmtTime(c.CreatedAt))
		lines := []string{applyRowCursor(header, i == cursor)}
		for _, ln := range wrapBody(c.Body, max(1, width-2)) {
			lines = append(lines, "  "+ln)
		}
		lines = append(lines, "")
		chunks = append(chunks, entryChunk{lines: lines})
	}
	return assembleTab(headers, chunks, width, height, cursor)
}

// renderEventsTab formats one line per event:
// "[type] timestamp actor — description". The description is type-
// specific (e.g., "labeled bug", "linked #7"). Cursor highlights the
// row under dm.tabCursor; the rendered lines are windowed around the
// cursor so a tabCursor past the visible height still has its row on
// screen.
func renderEventsTab(es []EventLogEntry, width, height, cursor int, ts tabState) string {
	headers := []string{titleStyle.Render(fmt.Sprintf("Events (%d)", len(es)))}
	if placeholder := tabPlaceholder(ts, "events", "(no events yet)", len(es)); placeholder != nil {
		return assembleTab(headers, []entryChunk{*placeholder}, width, height, -1)
	}
	chunks := make([]entryChunk, 0, len(es))
	for i, e := range es {
		line := fmt.Sprintf("[%s] %s %s — %s",
			e.Type, fmtTime(e.CreatedAt), e.Actor, eventDescription(e))
		chunks = append(chunks, entryChunk{lines: []string{
			applyRowCursor(line, i == cursor),
		}})
	}
	return assembleTab(headers, chunks, width, height, cursor)
}

// renderLinksTab formats one line per link:
// "[type] → #ToN ← #FromN  by author @ timestamp". The "(open|closed)"
// status is not on the LinkEntry projection; pressing Enter on a link
// jumps to the target so the user can see the title and status there.
// Lines are windowed around the cursor for the same reason.
func renderLinksTab(ls []LinkEntry, width, height, cursor int, ts tabState) string {
	headers := []string{titleStyle.Render(fmt.Sprintf("Links (%d)", len(ls)))}
	if placeholder := tabPlaceholder(ts, "links", "(no links)", len(ls)); placeholder != nil {
		return assembleTab(headers, []entryChunk{*placeholder}, width, height, -1)
	}
	chunks := make([]entryChunk, 0, len(ls))
	for i, l := range ls {
		line := fmt.Sprintf("[%s] → #%d ← #%d  by %s @ %s",
			l.Type, l.ToNumber, l.FromNumber, l.Author, fmtTime(l.CreatedAt))
		chunks = append(chunks, entryChunk{lines: []string{
			applyRowCursor(line, i == cursor),
		}})
	}
	return assembleTab(headers, chunks, width, height, cursor)
}

// tabPlaceholder returns the chunk to render in lieu of the entry list
// when the tab is loading, errored, or empty. Returns nil when the
// caller should render the entries normally.
func tabPlaceholder(ts tabState, tab, emptyHint string, n int) *entryChunk {
	if ts.err != nil {
		return &entryChunk{lines: []string{
			errorStyle.Render(tab + ": " + ts.err.Error()),
		}}
	}
	if ts.loading {
		return &entryChunk{lines: []string{statusStyle.Render("(loading…)")}}
	}
	if n == 0 {
		return &entryChunk{lines: []string{statusStyle.Render(emptyHint)}}
	}
	return nil
}

// entryChunk groups the lines that belong to one tab entry. Comments
// produce multi-line chunks (header + wrapped body + separator);
// events and links produce one-line chunks. Windowing operates on
// chunk granularity so a cursor at entry N never lands on a partial
// row.
type entryChunk struct {
	lines []string
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

// assembleTab joins the header lines with the windowed entry chunks
// and clips the result to width. cursor is the entry index of the
// active row (or -1 for empty-tab placeholders).
func assembleTab(
	headers []string, chunks []entryChunk, width, height, cursor int,
) string {
	avail := height - len(headers)
	if avail < 1 {
		avail = 1
	}
	windowed := windowChunks(chunks, cursor, avail)
	out := make([]string, 0, len(headers)+8)
	out = append(out, headers...)
	for _, ch := range windowed {
		out = append(out, ch.lines...)
	}
	return clipTab(out, width, height)
}

// windowChunks returns the contiguous slice of chunks that includes
// the cursor entry and fits within budget lines. When everything fits,
// the input is returned unchanged. When it doesn't, the window slides
// so the cursor's chunk is fully visible — preferring to anchor at
// the top until the cursor crosses the budget, then scrolling so the
// cursor sits near the bottom of the viewport.
//
// chunks with zero lines (defensive — empty placeholders) are still
// kept so windowing arithmetic doesn't drift.
func windowChunks(chunks []entryChunk, cursor, budget int) []entryChunk {
	n := len(chunks)
	if n == 0 || budget <= 0 {
		return chunks
	}
	if totalLines(chunks) <= budget {
		return chunks
	}
	c := cursor
	if c < 0 || c >= n {
		c = 0
	}
	// Walk backwards from the cursor, including chunks until the next
	// addition would overflow. The cursor's own chunk is always included
	// even if it alone exceeds the budget — preferable to hiding the
	// cursor entirely.
	//
	// gosec G602 cannot see that c was clamped to [0, n) above.
	used := len(chunks[c].lines) //nolint:gosec // c was clamped to [0,n)
	start, end := c, c+1
	for start > 0 {
		add := len(chunks[start-1].lines)
		if used+add > budget {
			break
		}
		start--
		used += add
	}
	for end < n {
		add := len(chunks[end].lines)
		if used+add > budget {
			break
		}
		used += add
		end++
	}
	return chunks[start:end]
}

// totalLines sums the line counts across every chunk.
func totalLines(chunks []entryChunk) int {
	n := 0
	for _, ch := range chunks {
		n += len(ch.lines)
	}
	return n
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
