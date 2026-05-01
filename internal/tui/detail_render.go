package tui

import (
	"fmt"
	"strings"
	"unicode/utf8"

	"github.com/mattn/go-runewidth"
)

// View renders the detail view under the M2 chrome layer. Layout
// (top → bottom):
//
//   - title bar (project + counts + version) — same as the list view
//   - SSE/status line                         — same as the list view
//   - detail header strip:  #N · status · author · created Xago · updated Yago
//   - title row                               — bold, full-width
//   - hairline rule
//   - body                                    — scrollable
//   - hairline rule
//   - tab strip                               — [ active ]  others
//   - hairline rule
//   - tab content
//   - hairline rule
//   - footer status line                      — flash OR scroll indicator
//   - footer help row                         — context-specific keys
//
// width and height come from the parent Model's WindowSize. The body
// area + tab content area split the remainder after chrome.
func (dm detailModel) View(width, height int, chrome viewChrome) string {
	if dm.loading {
		return statusStyle.Render("loading…")
	}
	if dm.issue == nil {
		return statusStyle.Render("no issue selected")
	}
	title := renderTitleBar(width, chrome.scope, dm.viewerCounts(), chrome.version)
	statusBar := renderStatusLine(width, chrome.sseStatus, chrome.pending, dm.actor)
	header := dm.renderHeader(width)
	titleRow := dm.renderTitleRow(width)
	tabs := dm.renderTabStrip()
	helpRow := renderHelpBar(detailHelpItems(), width)
	bodyA, tabA := dm.splitContentHeight(height,
		title, statusBar, header, titleRow, tabs, helpRow)
	body := dm.renderBody(width, bodyA)
	tabContent := dm.renderActiveTab(width, tabA)
	rule := strings.Repeat("─", width)
	parts := []string{
		title, statusBar, header, titleRow, rule, body, rule, tabs, rule, tabContent, rule,
		dm.renderFooterLine(width, tabA),
	}
	// Panel-local prompt (M3b) renders below the footer line, anchored
	// to the bottom of the detail pane. The chrome carries the active
	// inputState so the renderer doesn't have to reach back to Model.
	if chrome.input.kind.isPanelPrompt() {
		parts = append(parts, renderPanelPrompt(chrome.input, panelPromptWidth(width)))
	}
	if t := renderToast(chrome.toast); t != "" {
		parts = append(parts, t)
	}
	parts = append(parts, helpRow)
	return joinNonEmpty(parts)
}

// panelPromptWidth caps the prompt's width so it doesn't span the
// full terminal — visually it should feel like a small overlay
// anchored to the detail pane. Min 40, max 60% of terminal width.
func panelPromptWidth(termWidth int) int {
	w := termWidth * 6 / 10
	if w < 40 {
		w = 40
	}
	if w > termWidth {
		w = termWidth
	}
	return w
}

// viewerCounts returns the counts the title bar wants when rendered
// from the detail view. Detail's listModel state isn't reachable
// here, so we return a zero value; the title bar degrades to "kata
// · {project} · v…" without the open/closed/all breakdown. That's
// honest — the detail view doesn't know the list slice. M6's split
// layout will pass real counts via viewChrome.
func (dm detailModel) viewerCounts() issueCounts { return issueCounts{} }

// splitContentHeight returns the (bodyHeight, tabContentHeight)
// split given the total terminal height and the rendered chrome
// strings. We reserve every chrome line plus the four hairline rules
// (above body / below body / below tab strip / below tab content),
// then split the remainder roughly two-thirds for body / one-third
// for tab content (matches the bodyHeight bias the original split
// used).
func (dm detailModel) splitContentHeight(total int, chromeStrings ...string) (int, int) {
	chromeLines := 4 // four hairline rules
	for _, s := range chromeStrings {
		chromeLines += countLines(s)
	}
	chromeLines++ // footer status line
	avail := total - chromeLines
	if avail < detailMinSplit {
		return detailMinBodyRows, detailMinTabRows
	}
	body := avail * 2 / 3
	if body < detailMinBodyRows {
		body = detailMinBodyRows
	}
	tab := avail - body
	if tab < detailMinTabRows {
		tab = detailMinTabRows
	}
	return body, tab
}

// detailMinSplit is the smallest tab-content + body budget that gets
// the proportional split. Below it, fall back to the floors.
const detailMinSplit = 8

// detailMinBodyRows / detailMinTabRows are the floors so neither
// pane collapses to zero on small terminals.
const (
	detailMinBodyRows = 4
	detailMinTabRows  = 3
)

// renderHeader builds the detail header strip:
// `#N · status · author · created Xago · updated Yago`. Sanitized
// agent text only — author flows through sanitizeForDisplay. Width
// is currently unused (the header wraps via the surrounding render
// chain) but kept on the signature for future right-aligned bits.
func (dm detailModel) renderHeader(_ int) string {
	iss := *dm.issue
	left := fmt.Sprintf("#%d", iss.Number) + " · " + statusChip(iss)
	bits := []string{}
	if iss.Author != "" {
		bits = append(bits, sanitizeForDisplay(iss.Author))
	}
	if !iss.CreatedAt.IsZero() {
		bits = append(bits, "created "+humanizeRelative(iss.CreatedAt))
	}
	if !iss.UpdatedAt.IsZero() {
		bits = append(bits, "updated "+humanizeRelative(iss.UpdatedAt))
	}
	right := strings.Join(bits, " · ")
	if right == "" {
		return left
	}
	return left + " · " + right
}

// renderTitleRow is the bold full-width title line below the header
// strip. Sanitized + truncated so the rendered cell never overflows
// or carries control sequences.
func (dm detailModel) renderTitleRow(width int) string {
	t := sanitizeForDisplay(dm.issue.Title)
	return titleStyle.Render(truncate(t, max(20, width)))
}

// renderFooterLine is the per-tab footer status line: mutation flash
// on the left wins over the scroll indicator on the right (matching
// the list view's behavior). The scroll indicator counts entries in
// the active tab, not the body area.
func (dm detailModel) renderFooterLine(width, tabRows int) string {
	left := dm.status
	right := ""
	n := dm.activeRowCount()
	if n > tabRows {
		start, end := windowBounds(n, dm.tabCursor, tabRows)
		right = statusStyle.Render(fmt.Sprintf("[%d-%d of %d %s]",
			start+1, end, n, dm.activeTabLabel()))
	}
	if left != "" {
		return statusStyle.Render(left)
	}
	return padLeftRight("", right, width)
}

// activeTabLabel returns the singular noun for the active tab so the
// scroll indicator reads naturally ("[1-9 of 12 events]" not
// "[1-9 of 12]").
func (dm detailModel) activeTabLabel() string {
	switch dm.activeTab {
	case tabComments:
		return "comments"
	case tabEvents:
		return "events"
	case tabLinks:
		return "links"
	}
	return ""
}

// detailHelpItems is the persistent footer help row for the detail
// view's default state. M3b's panel-local prompts will swap in their
// own help row when active.
func detailHelpItems() []helpRow {
	return []helpRow{
		{key: "j/k", desc: "move"},
		{key: "tab", desc: "next"},
		{key: "shift+tab", desc: "prev"},
		{key: "enter", desc: "jump"},
		{key: "esc", desc: "back"},
		{key: "e", desc: "edit"},
		{key: "c", desc: "comment"},
		{key: "x", desc: "close"},
		{key: "+", desc: "label"},
		{key: "a", desc: "owner"},
		{key: "?", desc: "help"},
		{key: "q", desc: "quit"},
	}
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

// renderTabStrip renders the three tab titles with their counts. The
// active tab is wrapped in literal brackets (`[ Comments (4) ]`) plus
// the bold/underline tabActive style; inactive tabs are plain text.
// The bracket-and-bold combo gives the active state two redundant
// signals so it remains visible in `KATA_COLOR_MODE=none`.
func (dm detailModel) renderTabStrip() string {
	titles := [detailTabCount]string{
		fmt.Sprintf("Comments (%d)", len(dm.comments)),
		fmt.Sprintf("Events (%d)", len(dm.events)),
		fmt.Sprintf("Links (%d)", len(dm.links)),
	}
	parts := make([]string, 0, detailTabCount)
	for i, t := range titles {
		if detailTab(i) == dm.activeTab {
			parts = append(parts, tabActive.Render("[ "+t+" ]"))
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
