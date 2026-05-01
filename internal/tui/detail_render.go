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
	parts := []string{header, body, tabs, tabContent}
	if s := dm.modal.Render(); s != "" {
		parts = append(parts, s)
	}
	if dm.status != "" {
		parts = append(parts, dm.status)
	}
	return strings.Join(parts, "\n")
}

// bodyHeight is the row count allotted to the body window.
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

// tabContentHeight is the complement of bodyHeight.
func (dm detailModel) tabContentHeight(total int) int {
	avail := total - 2
	if avail < 6 {
		return 1
	}
	return avail - dm.bodyHeight(total)
}

// renderHeader builds the top line: #N status[chip] [deleted?] title.
// Title is agent-authored so it runs through sanitizeForDisplay before
// truncate to keep ANSI / control sequences out of the rendered cell.
func (dm detailModel) renderHeader(width int) string {
	iss := *dm.issue
	parts := []string{
		fmt.Sprintf("#%d", iss.Number),
		statusChip(iss),
		titleStyle.Render(truncate(sanitizeForDisplay(iss.Title), max(20, width-30))),
	}
	return strings.Join(parts, " ")
}

// renderBody splits the issue body on newlines, hard-wraps each line,
// and returns the dm.scroll-windowed slice. Hard-wrap (truncate) keeps
// v1 simple; soft word-wrap is deferred. Body is sanitized before
// wrapping so agent-authored ANSI / control sequences cannot reach
// the terminal.
func (dm detailModel) renderBody(width, lines int) string {
	wrapped := wrapBody(sanitizeForDisplay(dm.issue.Body), width)
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

// hardWrap breaks s into chunks no wider than width cells.
func hardWrap(s string, width int) []string {
	out := []string{}
	for runewidth.StringWidth(s) > width {
		head := runewidth.Truncate(s, width, "")
		if head == "" {
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

// renderTabStrip renders the three tab titles. The active tab gets
// tabActive; the rest get tabInactive.
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

// renderActiveTab dispatches to the per-tab renderer. The header line
// "Comments (N)" / "Events (N)" / "Links (N)" sits above the entries
// and is always rendered (even on empty data) so the tab strip + count
// stays consistent across tab switches. The per-tab loading/err state
// is forwarded so the renderer can substitute "(loading...)" or an
// error chip for the entry list.
func (dm detailModel) renderActiveTab(width, height int) string {
	switch dm.activeTab {
	case tabComments:
		return renderCommentsTab(dm.comments, width, height, dm.tabCursor,
			tabState{loading: dm.commentsLoading, err: dm.commentsErr})
	case tabEvents:
		return renderEventsTab(dm.events, width, height, dm.tabCursor,
			tabState{loading: dm.eventsLoading, err: dm.eventsErr})
	case tabLinks:
		return renderLinksTab(dm.links, width, height, dm.tabCursor,
			tabState{loading: dm.linksLoading, err: dm.linksErr})
	}
	return ""
}
