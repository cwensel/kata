package tui

import (
	"fmt"
	"strings"
	"unicode/utf8"

	"github.com/mattn/go-runewidth"
)

// View renders the detail view under the M3.5 chrome layer:
//
//   - line 1:    title bar (kata かた · project: $name · version)
//   - line 2:    detail header strip (#N · status · author · timestamps)
//   - line 3:    issue title row (bold, full-width)
//   - line 4:    body separator rule
//   - lines 5..: body (scrollable, padded so the tab strip pins
//     under the body); tab strip; tab content; padding
//     so the info+footer pin to the bottom
//   - line H-1:  info line (panel prompt OR scroll/flash text)
//   - line H:    footer help row
//
// Same fillScreen pattern as the list view: the body+tab area
// absorbs slack so the footer always sits on the last terminal row.
func (dm detailModel) View(width, height int, chrome viewChrome) string {
	if dm.loading {
		return statusStyle.Render("loading…")
	}
	if dm.issue == nil {
		return statusStyle.Render("no issue selected")
	}
	if width <= 0 || height < listMinHeight {
		return dm.renderTinyFallback(width)
	}
	title := renderTitleBar(width, chrome.scope, chrome.version)
	header := dm.renderHeader(width)
	titleRow := dm.renderTitleRow(width)
	tabs := dm.renderTabStrip()
	footer := renderFooterBar(width, detailFooterItemsFor(chrome.input), 0, nil, ListFilter{})

	// Reserve: header (1) + detail-header (1) + title-row (1) +
	// body-rule (1) + tab-rule (1) + info (1) + footer (1) = 7 fixed.
	// Whatever remains is split body 2/3, tab content 1/3.
	avail := height - 7
	if avail < detailMinSplit {
		avail = detailMinSplit
	}
	bodyA := avail * 2 / 3
	if bodyA < detailMinBodyRows {
		bodyA = detailMinBodyRows
	}
	tabA := avail - bodyA
	if tabA < detailMinTabRows {
		tabA = detailMinTabRows
	}
	body := dm.renderBody(width, bodyA)
	tabContent := dm.renderActiveTab(width, tabA)
	rule := separatorRuleStyle.Render(strings.Repeat("─", width))
	bodyArea := dm.padArea(body, bodyA, width)
	tabArea := dm.padArea(tabContent, tabA, width)
	// Info line uses the real tabA budget so the scroll indicator
	// fires correctly (roborev #107 finding 1).
	infoLine := dm.renderInfoLine(width, chrome, tabA)
	return strings.Join([]string{
		title, header, titleRow, rule, bodyArea, tabs, tabArea,
		infoLine, footer,
	}, "\n")
}

// renderTinyFallback is the degraded render for terminals below the
// minimum height. Just dump body content so the user sees something.
func (dm detailModel) renderTinyFallback(width int) string {
	return dm.renderBody(width, detailMinBodyRows)
}

// padArea pads `content` with normalRowStyle blank rows so the
// rendered block is exactly `rows` lines tall. Used to absorb the
// slack between the body / tab content and the rest of the chrome
// so the footer pins to the bottom (msgvault fillScreen pattern).
func (dm detailModel) padArea(content string, rows, width int) string {
	lines := strings.Split(content, "\n")
	for len(lines) < rows {
		lines = append(lines, normalRowStyle.Render(strings.Repeat(" ", width)))
	}
	if len(lines) > rows {
		lines = lines[:rows]
	}
	return strings.Join(lines, "\n")
}

// renderInfoLine renders the info line just above the footer for the
// detail view. Same priority order as the list view: active panel
// prompt > flash > SSE-degraded > toast > scroll indicator. Always
// rendered inside statsLineStyle so the row reads as chrome even
// when blank.
//
// tabBudget is the actual tab-content row budget (computed in View
// from height). When 0 the scroll indicator is suppressed — used by
// the early View call before bodyA/tabA are resolved; View calls
// this again with the real budget once it knows tabA.
func (dm detailModel) renderInfoLine(width int, chrome viewChrome, tabBudget int) string {
	body := ""
	switch {
	case chrome.input.kind.isPanelPrompt():
		body = renderInfoPrompt(chrome.input, titleBarInnerWidth(width))
	case dm.status != "":
		body = dm.status
	case chrome.sseStatus != sseConnected:
		body = sseDegradedFlash(chrome.sseStatus)
	case chrome.toast != nil:
		body = chrome.toast.text
	default:
		n := dm.activeRowCount()
		if n > 0 && tabBudget > 0 && n > tabBudget {
			start, end := windowBounds(n, dm.tabCursor, tabBudget)
			body = rightAlignInside(
				fmt.Sprintf("[%d-%d of %d %s]",
					start+1, end, n, dm.activeTabLabel()),
				titleBarInnerWidth(width))
		}
	}
	return statsLineStyle.Render(padToWidth(body, titleBarInnerWidth(width)))
}

// renderInfoPrompt renders an active panel-local prompt as a single
// info-line row. Bordered/labeled at panel-prompt scope makes the
// info line too tall; instead the prompt's title prefixes the buffer.
func renderInfoPrompt(s inputState, innerWidth int) string {
	body := s.title + ": " + sanitizeForDisplay(s.activeField().input.View())
	return runewidth.Truncate(body, innerWidth, "…")
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

// detailFooterItems is the persistent footer help row for the
// detail view's default state. M3b's panel-local prompts swap in
// their own help row via detailFooterItemsFor when active.
//
// Labels are msgvault-style short tokens; `?` opens the full
// sectioned help overlay for the long-form descriptions.
func detailFooterItems() []helpRow {
	return []helpRow{
		{key: "↑↓", desc: "move"},
		{key: "↹", desc: "tab"},
		{key: "↵", desc: "jump"},
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

// detailFooterItemsFor returns the footer items given the active
// input. Panel prompts get the prompt's commit/cancel hints; otherwise
// the default detail keys render.
func detailFooterItemsFor(input inputState) []helpRow {
	if input.kind.isPanelPrompt() {
		return []helpRow{
			{key: "enter", desc: "commit"},
			{key: "esc", desc: "cancel"},
		}
	}
	return detailFooterItems()
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
