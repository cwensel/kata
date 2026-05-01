package tui

import (
	"fmt"
	"strings"
	"unicode/utf8"

	"github.com/mattn/go-runewidth"
)

// View renders the detail view. Layout (top → bottom):
//   - header line: #N status [deleted?]  title
//   - body: line-wrapped issue.Body, scrolled by dm.scroll
//   - tab strip: [Comments]  Events  Links
//   - tab content: per-tab one-line stubs (Task 8 fleshes these out)
//
// width and height come from the parent Model's WindowSize. height is
// split between body and tab content; the tab strip and header take one
// line each.
func (dm detailModel) View(width, height int) string {
	if dm.loading {
		return statusStyle.Render("loading…")
	}
	if dm.issue == nil {
		return statusStyle.Render("no issue selected")
	}
	header := dm.renderHeader(width)
	bodyArea := dm.bodyHeight(height)
	body := dm.renderBody(width, bodyArea)
	tabs := dm.renderTabStrip()
	tabContent := dm.renderActiveTab(width, dm.tabContentHeight(height))
	return strings.Join([]string{header, body, tabs, tabContent}, "\n")
}

// bodyHeight is the row count allotted to the body window. The header
// and tab strip each consume one row; the rest is split 50/50 between
// body and tab content with a 5-row floor for body so even tiny
// terminals show something readable.
func (dm detailModel) bodyHeight(total int) int {
	avail := total - 2 // header + tab strip
	if avail < 6 {
		return 5
	}
	if h := avail / 2; h >= 5 {
		return h
	}
	return 5
}

// tabContentHeight is the complement of bodyHeight. The split mirrors
// bodyHeight so the two stay in sync.
func (dm detailModel) tabContentHeight(total int) int {
	avail := total - 2
	if avail < 6 {
		return 1
	}
	return avail - dm.bodyHeight(total)
}

// renderHeader builds the top line: #N status[chip] [deleted?] title.
// titleStyle highlights the title; statusChip handles the status pill
// with the right color. Soft-deleted rows get a [deleted] marker so the
// header mirrors the list row's status presentation.
func (dm detailModel) renderHeader(width int) string {
	iss := *dm.issue
	parts := []string{
		fmt.Sprintf("#%d", iss.Number),
		statusChip(iss),
		titleStyle.Render(truncate(iss.Title, max(20, width-30))),
	}
	return strings.Join(parts, " ")
}

// renderBody splits the issue body on newlines, hard-wraps each line to
// width, then takes a window of length lines starting at dm.scroll. The
// scroll offset is clamped here (not in Update) so the consumer is the
// only place that reads the wrapped-line count. Hard-wrap (truncate)
// keeps the v1 simple; soft word-wrap is deferred.
func (dm detailModel) renderBody(width, lines int) string {
	wrapped := wrapBody(dm.issue.Body, width)
	if len(wrapped) == 0 {
		return statusStyle.Render("(no body)")
	}
	start := dm.scroll
	if maxStart := len(wrapped) - lines; start > maxStart {
		if maxStart < 0 {
			maxStart = 0
		}
		start = maxStart
	}
	end := start + lines
	if end > len(wrapped) {
		end = len(wrapped)
	}
	return strings.Join(wrapped[start:end], "\n")
}

// wrapBody splits s on newlines, then hard-wraps each segment to width.
// The result is the flat sequence of display lines the body window
// scrolls over. Empty input yields nil so renderBody can show a hint.
func wrapBody(s string, width int) []string {
	if s == "" {
		return nil
	}
	if width < 1 {
		width = 1
	}
	out := []string{}
	for _, raw := range strings.Split(s, "\n") {
		if raw == "" {
			out = append(out, "")
			continue
		}
		out = append(out, hardWrap(raw, width)...)
	}
	return out
}

// hardWrap breaks s into chunks no wider than width cells. We walk one
// rune at a time and emit a chunk whenever the cell width crosses the
// limit — runewidth.Truncate alone would drop the tail; the loop keeps
// it as the next chunk. When the leading rune itself is wider than
// width (e.g. a CJK glyph at width=1), Truncate returns "" because no
// rune fits; we emit that single oversize rune as its own chunk and
// advance so the loop terminates.
func hardWrap(s string, width int) []string {
	out := []string{}
	for runewidth.StringWidth(s) > width {
		head := runewidth.Truncate(s, width, "")
		if head == "" {
			// First rune is wider than `width` — emit it as oversize and
			// advance one rune so we make progress.
			_, sz := utf8.DecodeRuneInString(s)
			out = append(out, s[:sz])
			s = s[sz:]
			continue
		}
		out = append(out, head)
		s = s[len(head):]
	}
	if s != "" {
		out = append(out, s)
	}
	return out
}

// renderTabStrip renders the three tab titles. The active tab gets the
// tabActive style; the rest get tabInactive. Two-space gaps separate
// tabs so the strip reads like a tab-bar without box-drawing.
func (dm detailModel) renderTabStrip() string {
	titles := [detailTabCount]string{"Comments", "Events", "Links"}
	parts := make([]string, 0, detailTabCount)
	for i, t := range titles {
		if detailTab(i) == dm.activeTab {
			parts = append(parts, tabActive.Render(t))
		} else {
			parts = append(parts, tabInactive.Render(t))
		}
	}
	return strings.Join(parts, "  ")
}

// renderActiveTab dispatches to the per-tab renderer. Task 7 stubs each
// tab as a one-liner per row so the dispatch is exercised; Task 8
// replaces these with the real renderers.
func (dm detailModel) renderActiveTab(width, height int) string {
	switch dm.activeTab {
	case tabComments:
		return renderTabStub(commentLines(dm.comments), width, height)
	case tabEvents:
		return renderTabStub(eventLines(dm.events), width, height)
	case tabLinks:
		return renderTabStub(linkLines(dm.links), width, height)
	}
	return ""
}

// renderTabStub is the shared frame for the v1 tab content: a header
// line ("Comments (N)" etc. supplied by the caller as lines[0]) plus
// the body. Width-truncates so long lines never wrap and break the
// layout. Empty data renders the header alone — the caller already
// formats the (0) count.
func renderTabStub(lines []string, width, height int) string {
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

// commentLines is the v1 stub renderer: "Comments (N)" header + one
// "[author] body" line per comment. Task 8 replaces this with full
// formatting (timestamps, multi-line bodies, author colors).
func commentLines(cs []CommentEntry) []string {
	out := []string{titleStyle.Render(fmt.Sprintf("Comments (%d)", len(cs)))}
	for _, c := range cs {
		out = append(out, fmt.Sprintf("[%s] %s", c.Author, c.Body))
	}
	return out
}

// eventLines is the v1 stub renderer for the events tab.
func eventLines(es []EventLogEntry) []string {
	out := []string{titleStyle.Render(fmt.Sprintf("Events (%d)", len(es)))}
	for _, e := range es {
		out = append(out, fmt.Sprintf("[%s] %s", e.Actor, e.Type))
	}
	return out
}

// linkLines is the v1 stub renderer for the links tab.
func linkLines(ls []LinkEntry) []string {
	out := []string{titleStyle.Render(fmt.Sprintf("Links (%d)", len(ls)))}
	for _, l := range ls {
		out = append(out, fmt.Sprintf("%s #%d → #%d", l.Type, l.FromNumber, l.ToNumber))
	}
	return out
}
