package tui

import (
	"fmt"
	"strings"
	"unicode/utf8"

	"github.com/charmbracelet/x/ansi"
	"github.com/mattn/go-runewidth"
)

// View renders the detail view under the M3.5 chrome layer:
//
//   - line 1:    title bar (kata かた · project: $name · version)
//   - line 2:    meta row (#N · status · author · timestamps)
//   - line 3:    assignment row (Owner: <name>           label chips)
//   - line 4:    issue title row (bold, full-width)
//   - line 5:    labeled body rule (── body ─────)
//   - lines 6..: body (scrollable, padded so the activity rule pins
//     under the body); labeled activity rule; tab strip; tab content;
//     padding so the info+footer pin to the bottom
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
	meta := renderHeaderMeta(*dm.issue)
	assign := renderHeaderAssignment(width, *dm.issue)
	titleRow := renderHeaderTitle(width, *dm.issue)
	hierarchy := renderHierarchySummary(width, dm.parent, dm.children)
	bodyRule := renderLabeledRule("body", width)
	activityRule := renderLabeledRule("activity", width)
	tabs := dm.renderTabStrip()
	footer := renderFooterBar(width, footerHints(detailFooterContext(dm, chrome)), 0, 0)

	// Reserve ten fixed rows:
	//   title bar (1) + meta (1) + assignment (1) + title row (1) +
	//   hierarchy summary (1) + body rule (1) + activity rule (1) + tab strip (1) +
	//   info (1) + footer (1) = 10.
	// (variable: body content + optional children + tab content)
	// No separate "tab rule" — the activity rule above the tab strip
	// is the only horizontal divider in the activity panel.
	//
	// Plan-8 commit-3 follow-up: the suggestion menu OVERLAYS tab
	// content rather than shrinking the body. If the body+tab budget
	// also shrank by menuH, the info+footer would slide UP by menuH
	// while overlaySuggestMenu still anchored at height-2-menuH —
	// the menu's top border collided with the prompt row and entries
	// extended past the footer. Keeping the body at full height
	// preserves info on row height-2 and footer on height-1, and the
	// menu overlay places its bottom border on row height-3 (one
	// above the info line). The scroll indicator is briefly less
	// accurate while the menu is open — acceptable: the user is
	// autocompleting, not paging tab content.
	bodyA, childA, tabA := detailStackedBudgets(height, len(dm.children))
	body := dm.renderBody(width, bodyA)
	children := dm.renderChildrenSection(width, childA)
	tabContent := dm.renderActiveTab(width, tabA)
	bodyArea := dm.padArea(body, bodyA, width)
	childrenArea := ""
	if childA > 0 {
		childrenArea = dm.padArea(children, childA, width)
	}
	tabArea := dm.padArea(tabContent, tabA, width)
	// Info line uses the real tabA budget so the scroll indicator
	// fires correctly (roborev #107 finding 1).
	infoLine := dm.renderInfoLine(width, chrome, tabA)
	parts := []string{
		title, meta, assign, titleRow, hierarchy, bodyRule, bodyArea,
	}
	if childrenArea != "" {
		parts = append(parts, childrenArea)
	}
	parts = append(parts, activityRule, tabs, tabArea, infoLine, footer)
	return strings.Join(parts, "\n")
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
	if rows <= 0 {
		return ""
	}
	lines := strings.Split(content, "\n")
	for len(lines) < rows {
		lines = append(lines, normalRowStyle.Render(strings.Repeat(" ", width)))
	}
	if len(lines) > rows {
		lines = lines[:rows]
	}
	return strings.Join(lines, "\n")
}

func detailStackedBudgets(height, childCount int) (bodyRows, childRows, tabRows int) {
	return detailBudgets(height-10, childCount)
}

func detailSplitBudgets(height, childCount int) (bodyRows, childRows, tabRows int) {
	return detailBudgets(height-7, childCount)
}

func detailBudgets(avail, childCount int) (bodyRows, childRows, tabRows int) {
	if avail < detailMinSplit {
		avail = detailMinSplit
	}
	if childCount > 0 && avail >= detailMinBodyRows+detailMinTabRows+2 {
		childRows = min(childCount+1, max(2, avail/4))
		avail -= childRows
	}
	bodyRows = avail * 2 / 3
	if bodyRows < detailMinBodyRows {
		bodyRows = detailMinBodyRows
	}
	tabRows = avail - bodyRows
	if tabRows < detailMinTabRows {
		tabRows = detailMinTabRows
	}
	return bodyRows, childRows, tabRows
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
		// Compute the visible-entry window from the same chunk-
		// windowing logic the per-tab renderer uses, so multi-line
		// chunks (comments) report the right [start-end] range and
		// don't suppress the indicator when entry count <= line
		// budget but total wrapped lines exceed it (#119 finding 2).
		n := dm.activeRowCount()
		if n > 0 && tabBudget > 0 {
			chunks := dm.activeChunks(width)
			start, end := windowChunkBounds(chunks, dm.tabCursor, tabBudget)
			if end-start < n {
				body = rightAlignInside(
					fmt.Sprintf("[%d-%d of %d %s]",
						start+1, end, n, dm.activeTabLabel()),
					titleBarInnerWidth(width))
			}
		}
	}
	return statsLineStyle.Render(padToWidth(body, titleBarInnerWidth(width)))
}

// renderInfoPrompt renders an active panel-local prompt as a single
// info-line row. Bordered/labeled at panel-prompt scope makes the
// info line too tall; instead the prompt's title prefixes the buffer.
//
// The textinput's View() carries bubbles' own cursor-paint ANSI;
// keep it intact (don't sanitize — strips the cursor) and width-clip
// with ansi.Truncate so escape sequences survive.
func renderInfoPrompt(s inputState, innerWidth int) string {
	body := s.title + ": " + s.activeField().input.View()
	return ansi.Truncate(body, innerWidth, "…")
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

// renderHeaderMeta builds the first detail-header row:
// `#N · status · author · created Xago · updated Yago`. Sanitized
// agent text only — author flows through sanitizeForDisplay.
func renderHeaderMeta(iss Issue) string {
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

// renderHeaderAssignment builds the second header row: owner on the
// left, label chips packed on the right. Width-aware so the chip
// strip surrenders cells to the owner half rather than overflowing.
// Owner placeholder `Owner: —` keeps the row's visible weight when
// no owner is set.
func renderHeaderAssignment(width int, iss Issue) string {
	left := "Owner: " + ownerDisplay(iss.Owner)
	leftCells := runewidth.StringWidth(left)
	// Right side gets whatever remains after the owner half plus a
	// 1-cell separator. Floor at 1 so renderLabelChips doesn't get a
	// negative budget on tiny terminals — its own ultra-narrow path
	// will degrade gracefully.
	rightBudget := width - leftCells - 1
	if rightBudget < 1 {
		rightBudget = 1
	}
	right := renderLabelChips(iss.Labels, rightBudget)
	return padLeftRightInside(left, right, width)
}

// ownerDisplay returns the rendered owner text, sanitized for display.
// Nil or empty owner renders as the em-dash placeholder so the row
// keeps its visible weight (`Owner: —`).
func ownerDisplay(owner *string) string {
	if owner == nil || *owner == "" {
		return "—"
	}
	return sanitizeForDisplay(*owner)
}

// renderHeaderTitle is the bold full-width title row below the
// assignment row. Sanitized + truncated so the rendered cell never
// overflows or carries control sequences.
func renderHeaderTitle(width int, iss Issue) string {
	t := sanitizeForDisplay(iss.Title)
	return titleStyle.Render(truncate(t, max(20, width)))
}

func renderHierarchySummary(width int, parent *IssueRef, children []Issue) string {
	left := "Parent: -"
	if parent != nil {
		left = fmt.Sprintf("Parent: #%d %s", parent.Number, sanitizeForDisplay(parent.Title))
	}
	right := "Children: " + childrenCountSummary(children)
	rightW := runewidth.StringWidth(right)
	leftBudget := width - rightW - 1
	if leftBudget < 1 {
		leftBudget = 1
	}
	left = truncate(left, leftBudget)
	return padLeftRightInside(left, right, width)
}

func childrenCountSummary(children []Issue) string {
	open := 0
	for _, child := range children {
		if child.Status == "open" {
			open++
		}
	}
	return fmt.Sprintf("%d open / %d total", open, len(children))
}

// renderLabeledRule produces `── <label> ──` padded to width with
// dashes. Falls back to a plain dash run when the label prefix is
// wider than the available width — defensive against tiny terminals
// so we never call strings.Repeat with a negative count.
func renderLabeledRule(label string, width int) string {
	if width <= 0 {
		return ""
	}
	prefix := "── " + label + " ──"
	prefixW := runewidth.StringWidth(prefix)
	if prefixW > width {
		return separatorRuleStyle.Render(strings.Repeat("─", width))
	}
	return separatorRuleStyle.Render(prefix + strings.Repeat("─", width-prefixW))
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

func (dm detailModel) renderChildrenSection(width, rows int) string {
	if rows <= 0 || len(dm.children) == 0 {
		return ""
	}
	lines := []string{renderLabeledRule("children "+childrenCountSummary(dm.children), width)}
	if rows == 1 {
		return strings.Join(lines, "\n")
	}
	cursor := clampInt(dm.childCursor, 0, len(dm.children)-1)
	visible, vCursor := windowChildIssues(dm.children, cursor, rows-1)
	for i, child := range visible {
		line := renderChildIssueRow(child, i == vCursor && dm.detailFocus == focusChildren, width)
		if i == vCursor && dm.detailFocus == focusChildren {
			line = cursorRowStyle.Render(padToWidth(line, width))
		} else if i%2 == 1 {
			line = altRowStyle.Render(padToWidth(line, width))
		} else {
			line = normalRowStyle.Render(padToWidth(line, width))
		}
		lines = append(lines, line)
	}
	return strings.Join(lines, "\n")
}

func renderChildIssueRow(child Issue, selected bool, width int) string {
	const (
		markerW = 2
		numW    = 7
		statusW = 10
		ownerW  = 12
		updateW = 10
	)
	titleW := width - markerW - numW - statusW - ownerW - updateW
	if titleW < 12 {
		titleW = 12
	}
	marker := " "
	if selected {
		marker = "▶"
	}
	parts := []string{
		padToWidth(marker, markerW),
		padToWidth(fmt.Sprintf("#%d", child.Number), numW),
		padToWidth(statusChip(child), statusW),
		padToWidth(truncate(sanitizeForDisplay(child.Title), titleW), titleW),
		padToWidth(truncate(sanitizeForDisplay(ownerText(child.Owner)), ownerW-1), ownerW),
		padToWidth(humanizeRelative(child.UpdatedAt), updateW),
	}
	return strings.Join(parts, "")
}

func windowChildIssues(issues []Issue, cursor, budget int) ([]Issue, int) {
	n := len(issues)
	if n == 0 {
		return issues, 0
	}
	start, end := windowBounds(n, cursor, budget)
	return issues[start:end], cursor - start
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
