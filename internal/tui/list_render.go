package tui

import (
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/charmbracelet/lipgloss"
	"github.com/charmbracelet/lipgloss/table"
	"github.com/charmbracelet/x/ansi"
	"github.com/mattn/go-runewidth"
	"github.com/wesm/kata/internal/textsafe"
)

// viewChrome carries the cross-cutting render inputs that lm.View
// and dm.View need to draw the title bar, stats line, info line, and
// footer help row. Plumbed from Model so the sub-views don't have to
// reach back into parent state. Zero-value renders a "minimal chrome"
// view (used by snapshot tests that exercise just the body) — no
// version, no SSE indicator text, no toast, no input.
//
// input is the active inline command bar / panel-local prompt /
// centered form. When input.kind is a command bar, the chrome
// renders the bar on the info line just above the footer (msgvault
// pattern) and swaps the help row to the bar's keys.
type viewChrome struct {
	scope     scope        // project / counts / version go in the title bar
	sseStatus sseConnState // surfaces only as a flash when not connected
	pending   bool         // pendingRefetch — surfaces as a flash when set
	toast     *toast       // optional flash message (e.g. "resynced")
	version   string       // build-time version string for the title bar; "" hides
	input     inputState   // active input shell (M3a bar; M3b prompt; M4 form)
	// suggestions / suggestEntry feed the label-prompt autocomplete
	// menu. Threaded through chrome so the detail-view layout can
	// reserve the menu's rendered height when computing tab/body
	// budgets without reaching back into Model state. Zero value is
	// a no-menu signal (renderInfoLine treats len(suggestions)==0
	// AND zero entry as "no menu visible," same as no input open).
	suggestions   []LabelCount
	suggestEntry  labelCacheEntry
	suggestActive bool
}

// View renders the list under the M3.5 chrome layer:
//
//   - line 1: title bar (brand · project · version)
//   - line 2: stats line (counts + filter chips)
//   - lines 3..H-2: table (header + separator + windowed rows, padded
//     so the footer pins to the bottom of the terminal regardless of
//     row count)
//   - line H-1: info line (active input bar OR scroll/flash text)
//   - line H:   footer help row
//
// The table body absorbs the slack so the footer always sits on the
// last line of the terminal — the msgvault `fillScreen` pattern.
func (lm listModel) View(width, height int, chrome viewChrome) string {
	if lm.loading {
		return statusStyle.Render("loading…")
	}
	if lm.err != nil {
		return errorStyle.Render(lm.err.Error())
	}
	if width <= 0 || height < listMinHeight {
		// Below the floor, render whatever fits without the chrome
		// layout math (avoids divide-by-negative or empty renders).
		return lm.renderTinyFallback(width)
	}
	title := renderTitleBar(width, chrome.scope, chrome.version)
	stats := renderStatsLine(width, chrome.scope, lm.issueCounts(), lm.filter)
	footer := renderFooterBar(width, listFooterItemsFor(chrome.input), lm.cursor, lm.issues, lm.filter)

	// Body area: everything between header (lines 1-2) and the
	// info+footer (last 2 lines). bodyRows is computed first so the
	// info-line scroll indicator uses the actual budget (not a
	// hardcoded approximation — roborev #107 finding 2). The
	// table-overhead cost (header + separator) is baked into
	// renderBodyArea, so bodyRows here is the *visible-data* budget.
	bodyRows := height - 2 /* header */ - 2 /* info+footer */
	if bodyRows < listBodyFloor {
		bodyRows = listBodyFloor
	}
	dataBudget := bodyRows - 2 /* table header + separator */
	if dataBudget < 1 {
		dataBudget = 1
	}
	infoLine := renderListInfoLine(width, chrome, lm, dataBudget)
	body := lm.renderBodyArea(width, bodyRows, chrome)

	return strings.Join([]string{title, stats, body, infoLine, footer}, "\n")
}

// listMinHeight is the smallest terminal height the layout supports.
// Below this we fall through to the bare fallback render.
const listMinHeight = 10

// listBodyFloor is the smallest row count the table body will ever
// render. Keeps a usable list visible even when the chrome math is
// tight on a small terminal.
const listBodyFloor = 5

// renderTinyFallback is the degraded render for terminals below the
// minimum height. M5 will replace this with a proper "too narrow"
// hint; for now it's just the table without chrome.
func (lm listModel) renderTinyFallback(width int) string {
	return lm.renderBody(width, listBodyFloor)
}

// renderBodyArea wraps renderBody with the fillScreen padding that
// pins the footer to the bottom. The table's data rows window around
// the cursor to fit bodyRows; the remaining vertical slack is padded
// with blank rows styled with normalRowStyle so terminals that retain
// prior content overwrite cleanly.
//
// When chrome.input.kind == inputNewIssueRow, a synthetic title-input
// row is prepended to the body — see renderBodyWithNewIssueRow.
func (lm listModel) renderBodyArea(width, bodyRows int, chrome viewChrome) string {
	body := lm.renderBodyWithChrome(width, bodyRows-2 /* header + sep */, chrome)
	rendered := strings.Split(body, "\n")
	for len(rendered) < bodyRows {
		rendered = append(rendered, normalRowStyle.Render(strings.Repeat(" ", width)))
	}
	if len(rendered) > bodyRows {
		rendered = rendered[:bodyRows]
	}
	return strings.Join(rendered, "\n")
}

// renderBodyWithChrome dispatches to either the standard renderBody
// or the new-issue-row variant based on chrome.input.kind.
func (lm listModel) renderBodyWithChrome(width, height int, chrome viewChrome) string {
	if chrome.input.kind == inputNewIssueRow {
		return lm.renderBodyWithNewIssueRow(width, height, chrome.input)
	}
	return lm.renderBody(width, height)
}

// renderBodyWithNewIssueRow renders the table with the inline new-
// issue row injected at index 0. The row hosts the title textinput;
// other columns render placeholders ("new", "—", etc.). Existing
// issues shift down by one within the data window.
//
// Cursor highlighting always lands on the new-issue row (it owns
// focus while the row is open); the underlying lm.cursor is not
// changed.
//
// The data window is forced to start at index 0 of the filtered list
// while the new-issue row is open, so users who pressed `n` while
// scrolled mid-list see the create row above the freshest issues
// (recency-sorted lists put the soon-to-be-created peer at the top)
// rather than above an arbitrary middle slice — roborev #113
// finding 1.
func (lm listModel) renderBodyWithNewIssueRow(width, height int, in inputState) string {
	cols := listColumnWidths(width)
	issues := filteredIssues(lm.issues, lm.filter)
	// Build the new-issue row from the textinput's view. The view
	// carries bubbles' own ANSI escape sequences for the cursor —
	// don't re-sanitize (would strip the cursor and leave the user
	// with no typing indication) and use ansi.Truncate so the cursor
	// escapes survive width-clipping intact.
	titleView := in.activeField().input.View()
	newRow := []string{
		"▶",
		"new",
		statusChipText("draft", "open"),
		ansi.Truncate(titleView, cols.title, "…"),
		"—",
		"—",
	}
	// Build the standard data rows; one fewer to make room for the
	// new row.
	dataBudget := height - 1
	if dataBudget < 1 {
		dataBudget = 1
	}
	visible, _ := windowIssues(issues, 0, dataBudget)
	// vCursor for issues is meaningless because the new row owns
	// the cursor highlight; pass -1 so no issue row gets cursorRowStyle.
	dataRows := buildRows(visible, -1, cols.title)
	allRows := append([][]string{newRow}, dataRows...)
	t := table.New().
		Border(lipgloss.HiddenBorder()).
		BorderTop(false).
		BorderBottom(false).
		BorderLeft(false).
		BorderRight(false).
		BorderColumn(false).
		BorderRow(false).
		BorderHeader(false).
		Width(width).
		Wrap(false).
		Headers("", "#", "status", "title", "owner", "updated").
		Rows(allRows...).
		StyleFunc(func(row, col int) lipgloss.Style {
			s := lipgloss.NewStyle().Width(cols.byIndex(col)).PaddingRight(1)
			if row == table.HeaderRow {
				return s.Inherit(tableHeaderStyle)
			}
			if row == 0 {
				return s.Inherit(cursorRowStyle)
			}
			if row > 0 && row%2 == 1 {
				return s.Inherit(altRowStyle)
			}
			return s.Inherit(normalRowStyle)
		})
	rendered := t.Render()
	lines := strings.SplitN(rendered, "\n", 2)
	if len(lines) < 2 {
		return rendered
	}
	rule := separatorRuleStyle.Render(strings.Repeat("─", width))
	return lines[0] + "\n" + rule + "\n" + lines[1]
}

// statusChipText is a non-styled fallback for the status column when
// the row is synthetic (e.g. the inline new-issue row). Returns the
// label text without lipgloss styling so it sits consistently inside
// the table cell.
func statusChipText(label, _ string) string {
	return label
}

// listFooterItems is the persistent footer help row for the list view.
// Labels are msgvault-style: single nouns/verbs separated from the key
// by one space. The `?` key still opens the full sectioned help
// overlay for the long-form descriptions; the footer carries only the
// most-used at-a-glance hints.
func listFooterItems() []helpRow {
	return []helpRow{
		{key: "↑↓", desc: "move"},
		{key: "↵", desc: "open"},
		{key: "n", desc: "new"},
		{key: "/", desc: "search"},
		{key: "o", desc: "owner"},
		{key: "s", desc: "status"},
		{key: "c", desc: "clear"},
		{key: "x", desc: "close"},
		{key: "?", desc: "help"},
		{key: "q", desc: "quit"},
	}
}

// listFooterItemsFor returns the footer items for the list view given
// the active input. Bars and the inline new-issue row get focused
// commit/cancel hints; otherwise the default list keys render.
func listFooterItemsFor(input inputState) []helpRow {
	if input.kind.isCommandBar() {
		return []helpRow{
			{key: "enter", desc: "commit"},
			{key: "esc", desc: "cancel"},
			{key: "ctrl+u", desc: "clear"},
		}
	}
	if input.kind == inputNewIssueRow {
		return []helpRow{
			{key: "enter", desc: "create"},
			{key: "esc", desc: "cancel"},
		}
	}
	return listFooterItems()
}

// renderTitleBar formats the top brand strip:
//
//	Project: {name}                         kata かた · vX.Y.Z
//
// Project context lives on the left because it's what the user is
// actually working in; brand + version are window-chrome and pin to
// the right. `かた` is hiragana for "form/pattern" — the romaji
// disambiguator for the brand vs. a project that happens to also be
// named "kata". All-projects scope and the empty-scope hint render
// in the project slot so the left side is never blank.
func renderTitleBar(width int, sc scope, version string) string {
	left := titleBarLeft(sc)
	right := titleBarRight(version)
	body := padLeftRightInside(left, right, titleBarInnerWidth(width))
	return titleBarStyle.Render(body)
}

// titleBarLeft builds the left-aligned project label. Single-project
// scope reads `Project: $name`; all-projects scope reads
// `Project: all`; the no-scope startup state reads `Project: —` so
// the bar layout doesn't shift once a project is resolved.
func titleBarLeft(sc scope) string {
	switch {
	case sc.allProjects:
		return "Project: all"
	case sc.projectName != "":
		return "Project: " + sanitizeForDisplay(sc.projectName)
	}
	return "Project: —"
}

// titleBarRight is the brand + version cluster pinned to the right
// of the title bar. Version is omitted (gracefully) on builds that
// didn't stamp it so the right side is just the brand.
func titleBarRight(version string) string {
	if version == "" {
		return "kata かた"
	}
	return "kata かた · " + version
}

// titleBarInnerWidth subtracts the titleBarStyle horizontal padding
// (1 cell each side) so padLeftRightInside fills exactly the
// renderable area.
func titleBarInnerWidth(width int) int {
	w := width - 2 // padding
	if w < 1 {
		w = 1
	}
	return w
}

// renderStatsLine is the second header line — counts + filter chips.
// SSE state and the actor used to live here in M1; both are gone
// (SSE surfaces as a flash on the info line when degraded; actor
// stays in lm.actor for mutation dispatch but isn't surfaced).
func renderStatsLine(width int, _ scope, c issueCounts, f ListFilter) string {
	left := statsCountsText(c)
	right := renderChips(f)
	body := padLeftRightInside(left, right, titleBarInnerWidth(width))
	return statsLineStyle.Render(body)
}

// statsCountsText reads the open/closed/all counts from issueCounts
// and renders them as `open: N · closed: N · all: N`. Empty issues
// renders as "no issues" so the line never goes blank when the user
// has the empty-state hint visible.
func statsCountsText(c issueCounts) string {
	if c.all == 0 {
		return "no issues"
	}
	return fmt.Sprintf("open: %d · closed: %d · all: %d", c.open, c.closed, c.all)
}

// renderListInfoLine renders the info line just above the footer.
// Sources, in priority order:
//
//  1. Active inline command bar (search/owner) — `/buffer` form via
//     renderInfoBar.
//  2. A status flash (mutation result like "closed #42").
//  3. SSE-degraded indicator when sseStatus != connected.
//  4. A toast (e.g. "resynced").
//  5. The scroll indicator `[start-end of N]` when the visible window
//     doesn't fit the full filtered list.
//  6. Blank if none of the above apply.
//
// dataBudget is the actual data-row budget the table renders into;
// the scroll indicator only fires when the visible list exceeds it.
// Threading it from View instead of approximating fixes roborev
// #107 finding 2 (hardcoded `lastBodyRows()` was wrong on terminals
// other than 30 rows tall).
//
// Always rendered inside statsLineStyle so the line reads as part
// of the chrome strip even when blank (background fills the row).
func renderListInfoLine(width int, chrome viewChrome, lm listModel, dataBudget int) string {
	body := ""
	switch {
	case chrome.input.kind.isCommandBar():
		body = renderInfoBar(chrome.input, titleBarInnerWidth(width))
	case lm.status != "":
		body = lm.status
	case chrome.sseStatus != sseConnected:
		body = sseDegradedFlash(chrome.sseStatus)
	case chrome.toast != nil:
		body = chrome.toast.text
	default:
		visible := filteredIssues(lm.issues, lm.filter)
		// While the inline new-issue row is open, the body anchors
		// at index 0 and gives up one data row to the synthetic row
		// (renderBodyWithNewIssueRow). Mirror both adjustments here
		// so the indicator matches the rendered window — roborev
		// #121 follow-up to #113.
		cursor := lm.cursor
		budget := dataBudget
		if chrome.input.kind == inputNewIssueRow {
			cursor = 0
			budget--
		}
		if n := len(visible); n > 0 && budget > 0 && n > budget {
			start, end := windowBounds(n, cursor, budget)
			body = rightAlignInside(
				fmt.Sprintf("[%d-%d of %d]", start+1, end, n),
				titleBarInnerWidth(width),
			)
		}
	}
	return statsLineStyle.Render(padToWidth(body, titleBarInnerWidth(width)))
}

// renderInfoBar formats the inline command bar for the info line.
// Single line, no border (the surrounding statsLineStyle already
// gives the row a chrome look), prefixed by a slash for search.
//
// The textinput's View() includes bubbles' own cursor-paint ANSI;
// we keep it intact (don't sanitize — that would erase the cursor)
// and width-clip with ansi.Truncate so escape sequences survive.
func renderInfoBar(s inputState, innerWidth int) string {
	prefix := "/"
	if s.kind == inputOwnerBar {
		prefix = "owner:"
	}
	full := prefix + s.activeField().input.View()
	return ansi.Truncate(full, innerWidth, "…")
}

// sseDegradedFlash returns a brief inline notice when SSE is in a
// non-connected state. Used by renderListInfoLine when no other
// flash takes priority.
func sseDegradedFlash(state sseConnState) string {
	switch state {
	case sseReconnecting:
		return "kata: reconnecting…"
	case sseDisconnected:
		return "kata: disconnected (retrying)"
	}
	return ""
}

// renderFooterBar formats the persistent footer help row. Items are
// joined with ` │ ` (vertical bar with spaces) — msgvault's denser
// style. A right-aligned position indicator (`[N/M]`) appears when
// there are issues to count.
func renderFooterBar(width int, items []helpRow, cursor int, issues []Issue, f ListFilter) string {
	left := joinHelpItems(items)
	right := footerPositionIndicator(cursor, issues, f)
	body := padLeftRightInside(left, right, titleBarInnerWidth(width))
	return footerBarStyle.Render(body)
}

// joinHelpItems renders a flat list of helpRow as a `key desc` line
// joined by ` │ ` separators. Each item has a faint key + slightly
// brighter desc; the separator stays faint.
func joinHelpItems(items []helpRow) string {
	parts := make([]string, 0, len(items))
	for _, it := range items {
		if it.desc == "" {
			parts = append(parts, helpKeyStyle.Render(it.key))
		} else {
			parts = append(parts, helpKeyStyle.Render(it.key)+" "+helpDescStyle.Render(it.desc))
		}
	}
	return strings.Join(parts, " │ ")
}

// footerPositionIndicator returns the cursor position in the visible
// list — `[N/M]`. Renders empty when the list is empty.
func footerPositionIndicator(cursor int, issues []Issue, f ListFilter) string {
	visible := filteredIssues(issues, f)
	if len(visible) == 0 {
		return ""
	}
	pos := cursor + 1
	if pos > len(visible) {
		pos = len(visible)
	}
	return fmt.Sprintf("[%d/%d]", pos, len(visible))
}

// padLeftRightInside places left text on the left and right text on
// the right of an `innerWidth`-cell-wide line, padding with spaces
// in between. Wide-character aware via runewidth.StringWidth so the
// `かた` glyphs in the title bar align correctly. When the right
// text would overflow, it's truncated to fit (the left side wins
// because it carries the brand/identity).
func padLeftRightInside(left, right string, innerWidth int) string {
	lw := runewidth.StringWidth(stripANSI(left))
	rw := runewidth.StringWidth(stripANSI(right))
	if lw >= innerWidth {
		return runewidth.Truncate(left, innerWidth, "…")
	}
	if lw+rw+1 > innerWidth {
		// Right doesn't fit; truncate it.
		availableForRight := innerWidth - lw - 1
		if availableForRight < 1 {
			return left + " "
		}
		right = runewidth.Truncate(right, availableForRight, "…")
		rw = runewidth.StringWidth(stripANSI(right))
	}
	gap := innerWidth - lw - rw
	if gap < 1 {
		gap = 1
	}
	return left + strings.Repeat(" ", gap) + right
}

// rightAlignInside returns s right-aligned within innerWidth cells.
// Used by the scroll indicator.
func rightAlignInside(s string, innerWidth int) string {
	w := runewidth.StringWidth(stripANSI(s))
	if w >= innerWidth {
		return runewidth.Truncate(s, innerWidth, "…")
	}
	return strings.Repeat(" ", innerWidth-w) + s
}

// padToWidth right-pads s with spaces so the rendered cell fills
// `width` cells. Used for chrome lines that need a uniform width.
func padToWidth(s string, width int) string {
	w := runewidth.StringWidth(stripANSI(s))
	if w >= width {
		return runewidth.Truncate(s, width, "…")
	}
	return s + strings.Repeat(" ", width-w)
}

// stripANSI removes ANSI escape sequences from s for width math (so
// padding accounts for visible runes only). Thin alias over
// textsafe.StripANSI so width helpers and the sanitizer share the
// same regex.
func stripANSI(s string) string { return textsafe.StripANSI(s) }

// issueCounts derives the per-status counts from the unfiltered
// lm.issues slice. Used by the title bar.
type issueCounts struct {
	open, closed, deleted, all int
}

func (lm listModel) issueCounts() issueCounts {
	c := issueCounts{all: len(lm.issues)}
	for _, iss := range lm.issues {
		if iss.DeletedAt != nil {
			c.deleted++
			continue
		}
		switch iss.Status {
		case "open":
			c.open++
		case "closed":
			c.closed++
		}
	}
	return c
}

// renderBody is the table body — header, separator, then up to height
// data rows around the cursor. No top/bottom borders (msgvault
// pattern); just one separator under the column header.
func (lm listModel) renderBody(width, height int) string {
	issues := filteredIssues(lm.issues, lm.filter)
	if len(issues) == 0 {
		hint := "no issues match. press c to clear filters or n to create one."
		return tableHeaderRow(width) + "\n" +
			separatorRuleStyle.Render(strings.Repeat("─", width)) + "\n" +
			normalRowStyle.Render(padToWidth("  "+hint, width))
	}
	displayCursor := lm.cursor
	if displayCursor >= len(issues) {
		displayCursor = len(issues) - 1
	}
	if displayCursor < 0 {
		displayCursor = 0
	}
	visible, vCursor := windowIssues(issues, displayCursor, height)
	cols := listColumnWidths(width)
	rows := buildRows(visible, vCursor, cols.title)
	t := table.New().
		Border(lipgloss.HiddenBorder()).
		BorderTop(false).
		BorderBottom(false).
		BorderLeft(false).
		BorderRight(false).
		BorderColumn(false).
		BorderRow(false).
		BorderHeader(false).
		Width(width).
		Wrap(false).
		Headers("", "#", "status", "title", "owner", "updated").
		Rows(rows...).
		StyleFunc(func(row, col int) lipgloss.Style {
			s := lipgloss.NewStyle().Width(cols.byIndex(col)).PaddingRight(1)
			if row == table.HeaderRow {
				return s.Inherit(tableHeaderStyle)
			}
			if row >= 0 && row < len(rows) && row == vCursor {
				return s.Inherit(cursorRowStyle)
			}
			if row >= 0 && row%2 == 1 {
				return s.Inherit(altRowStyle)
			}
			return s.Inherit(normalRowStyle)
		})
	rendered := t.Render()
	// Insert the separator rule between the header row and the data
	// rows. lipgloss.Table renders as "header\nrow1\nrow2..."; we
	// split, inject the rule, and re-join.
	lines := strings.SplitN(rendered, "\n", 2)
	if len(lines) < 2 {
		return rendered
	}
	rule := separatorRuleStyle.Render(strings.Repeat("─", width))
	return lines[0] + "\n" + rule + "\n" + lines[1]
}

// tableHeaderRow renders just the column-header line at the given
// width, used by the empty-state branch where the lipgloss Table
// isn't constructed. Mirrors the column widths and styling.
func tableHeaderRow(width int) string {
	cols := listColumnWidths(width)
	headers := []string{"", "#", "status", "title", "owner", "updated"}
	parts := make([]string, len(headers))
	for i, h := range headers {
		w := cols.byIndex(i)
		parts[i] = tableHeaderStyle.Render(padToWidth(h, w-1)) + " "
	}
	return strings.Join(parts, "")
}

// listColWidths holds the per-column cell widths the list table renders
// at. Fixed columns (cursor / # / status / owner / updated) take what
// they need; the title column flexes to fill the rest of the terminal
// with a 20-cell floor so titles stay readable on narrow terminals.
type listColWidths struct {
	cursor, num, status, title, owner, updated int
}

// byIndex maps a table column index to its width.
func (c listColWidths) byIndex(col int) int {
	switch col {
	case 0:
		return c.cursor
	case 1:
		return c.num
	case 2:
		return c.status
	case 3:
		return c.title
	case 4:
		return c.owner
	case 5:
		return c.updated
	}
	return 0
}

// listColumnWidths computes per-column cell widths for the list table.
// Fixed-width columns sum to ~42 cells (with PaddingRight(1) per cell);
// the title column flexes to fill the rest with a 20-cell floor.
func listColumnWidths(termWidth int) listColWidths {
	c := listColWidths{
		cursor:  2,  // "▶" + padding
		num:     6,  // "#9999"
		status:  10, // "[deleted]"
		owner:   14,
		updated: 10, // "12w ago"
	}
	fixed := c.cursor + c.num + c.status + c.owner + c.updated
	c.title = termWidth - fixed
	if c.title < 20 {
		c.title = 20
	}
	return c
}

// windowIssues returns the contiguous slice of issues that includes
// the cursor row and fits within budget. The cursor index in the
// returned slice (vCursor) is the local position so the table renderer
// can highlight the right row.
func windowIssues(issues []Issue, cursor, budget int) ([]Issue, int) {
	n := len(issues)
	if n == 0 {
		return issues, 0
	}
	start, end := windowBounds(n, cursor, budget)
	return issues[start:end], cursor - start
}

// windowBounds returns the [start, end) slice indices of the visible
// window for a list of n items with the cursor at index cursor and a
// row budget of budget. Used by both the row renderer and the scroll
// indicator so the displayed range and the rendered slice stay in
// sync. Empty input returns (0, 0); budget < 1 collapses to 1 so the
// cursor is always visible.
//
// The window slides so the cursor sits anywhere from the top to the
// bottom of the viewport, preferring to anchor at the top until the
// cursor moves past the budget, then scrolling to keep the cursor
// near the bottom. The "two-thirds from the top" anchor matches the
// conventional vim/less feel — more upcoming rows than scrolled-past.
func windowBounds(n, cursor, budget int) (int, int) {
	if n == 0 {
		return 0, 0
	}
	if budget < 1 {
		budget = 1
	}
	if n <= budget {
		return 0, n
	}
	headroom := budget / 3
	start := cursor - headroom
	if start < 0 {
		start = 0
	}
	end := start + budget
	if end > n {
		end = n
		start = n - budget
	}
	return start, end
}

// renderSSEStatus returns the connection-status line rendered below
// non-list views (help / empty) when the SSE consumer is degraded.
// The list view surfaces the same info on its info line via
// renderListInfoLine, so this helper only fires for views that
// don't carry the M3.5 chrome.
func renderSSEStatus(state sseConnState) string {
	switch state {
	case sseReconnecting:
		return statusStyle.Render("kata: reconnecting…")
	case sseDisconnected:
		return statusStyle.Render("kata: disconnected (retrying)")
	}
	return ""
}

// renderToast wraps an active toast for display below non-list views
// that don't have an info line of their own. List view renders the
// toast text inline via renderListInfoLine.
func renderToast(t *toast) string {
	if t == nil {
		return ""
	}
	return toastStyle.Render(t.text)
}

// renderChips returns one chip per active filter slot. Inactive
// defaults (status="", owner="", search="") are skipped. The label
// chip is omitted because the label-filter UI was retired (see
// keymap.go).
//
// "search" chip prefix changed from `q:` to `search:` in M3.5: the
// `q` letter collided with the global Quit binding and read as if
// the chip itself was bound to q.
func renderChips(f ListFilter) string {
	chips := []string{}
	if f.Status != "" {
		chips = append(chips, chipActive.Render("status:"+f.Status))
	}
	if f.Owner != "" {
		chips = append(chips, chipStyle.Render(
			"owner:"+sanitizeForDisplay(f.Owner)))
	}
	if f.Author != "" {
		chips = append(chips, chipStyle.Render(
			"author:"+sanitizeForDisplay(f.Author)))
	}
	if f.Search != "" {
		chips = append(chips, chipStyle.Render(
			fmt.Sprintf("search:%q", sanitizeForDisplay(f.Search))))
	}
	if len(chips) == 0 {
		return ""
	}
	return strings.Join(chips, "  ")
}

// renderLabelChips packs label chips into `available` cells for the
// detail header's right-side label strip. Chips render alphabetically;
// trailing chips that don't fit collapse into a `+N` suffix. When even
// one chip would overflow `available`, the entire row degrades to the
// fixed-width `[N labels]` token so the header stays informative on
// tiny terminals. Empty input yields a `(no labels)` placeholder so the
// row keeps its visible weight when an issue carries no labels.
//
// Sanitization runs BEFORE both width measurement and rendering: a
// caller that measured the stripped form but rendered the raw label
// would still leak ANSI / Cf control runes into the header. The
// sanitized text is the single source of truth used for both.
//
// Width math: each chip is `[<sanitized-label>]` plus one space
// separator before the next chip. The +N overflow token reserves
// up to 4 cells (` +99` worst case) inside `available` so packing
// never blows the budget by failing to leave room for the suffix.
func renderLabelChips(labels []string, available int) string {
	if len(labels) == 0 {
		return subtleStyle.Render("(no labels)")
	}
	clean := sanitizeAndSortLabels(labels)
	// Reserve worst-case +N width inside `available` so the suffix
	// always fits when packing drops chips. ` +99` is 4 cells.
	const overflowReserve = 4
	packed, dropped := packChips(clean, available, overflowReserve)
	if len(packed) == 0 {
		return ultraNarrowChipFallback(len(clean))
	}
	out := strings.Join(packed, " ")
	if dropped > 0 {
		out += " " + chipStyle.Render(fmt.Sprintf("+%d", dropped))
	}
	return out
}

// sanitizeAndSortLabels returns labels with each entry sanitized
// through textsafe.Line (ANSI / control / Cf stripped, plus newlines
// replaced with literal "\n" and tabs with spaces) and the slice
// sorted in ascending byte order. Line — not Block — because the chip
// strip is a single-row context: a label containing a literal \n
// would split mid-chip across terminal rows and break the fixed-row
// budget. The schema bars newlines in labels, but the renderer is
// the wrong layer to depend on that. Sort lives here so
// renderLabelChips stays focused on the packing math.
func sanitizeAndSortLabels(labels []string) []string {
	clean := make([]string, len(labels))
	for i, l := range labels {
		clean[i] = textsafe.Line(l)
	}
	sort.Strings(clean)
	return clean
}

// chipMinSlot returns the cell width of one rendered chip including
// the surrounding brackets — the unit packChips advances on per chip.
func chipMinSlot(label string) int {
	return runewidth.StringWidth(label) + 2 // brackets
}

// packChips greedily fits chips into `available` cells, leaving
// `overflowReserve` cells free at the tail so a `+N` token can be
// appended without overflow. Returns the rendered chip slice and the
// number of dropped tail labels.
func packChips(clean []string, available, overflowReserve int) ([]string, int) {
	out := make([]string, 0, len(clean))
	used := 0
	for i, l := range clean {
		chip := chipStyle.Render("[" + l + "]")
		w := chipMinSlot(l)
		// Separator between chips (not before the first chip).
		if len(out) > 0 {
			w++
		}
		// Reserve overflow room for any chips after this one.
		remaining := len(clean) - i - 1
		needTail := 0
		if remaining > 0 {
			needTail = overflowReserve
		}
		if used+w+needTail > available {
			return out, len(clean) - len(out)
		}
		out = append(out, chip)
		used += w
	}
	return out, 0
}

// ultraNarrowChipFallback is the degraded `[N labels]` token used when
// `available` is too small to fit even one chip plus the overflow
// reserve. Keeps the header informative without overflowing.
func ultraNarrowChipFallback(n int) string {
	return chipStyle.Render(fmt.Sprintf("[%d labels]", n))
}

// joinNonEmpty assembles a view from its non-empty sections.
// Retained for non-list callers (detail view still uses it); the list
// view now uses fixed-position composition.
func joinNonEmpty(parts []string) string {
	out := []string{}
	for _, p := range parts {
		if p != "" {
			out = append(out, p)
		}
	}
	return strings.Join(out, "\n")
}

// buildRows projects issues to the six-column shape the table renders.
// titleW is the budget for the (flexed) title column — the renderer
// truncates titles longer than that with an ellipsis. Owner is
// truncated at 12 cells so the column never overflows its 14-cell
// width. Title and owner are agent-authored so both run through
// sanitizeForDisplay before truncation.
//
// Cursor glyph is `▶` (msgvault pattern) — more visible than `›` in
// terminals that render fonts at low pixel density.
func buildRows(issues []Issue, cursor, titleW int) [][]string {
	if titleW < 20 {
		titleW = 20
	}
	rows := make([][]string, 0, len(issues))
	for i, iss := range issues {
		rows = append(rows, []string{
			selMarker(i == cursor),
			fmt.Sprintf("#%d", iss.Number),
			statusChip(iss),
			truncate(sanitizeForDisplay(iss.Title), titleW),
			truncate(sanitizeForDisplay(ownerText(iss.Owner)), 12),
			humanizeRelative(iss.UpdatedAt),
		})
	}
	return rows
}

// selMarker is the per-row arrow glyph; ' ' for unselected so the
// column width stays stable.
func selMarker(selected bool) string {
	if selected {
		return "▶"
	}
	return " "
}

// statusChip picks the right colored chip text for the issue.
// Soft-deleted rows win over closed.
func statusChip(iss Issue) string {
	switch {
	case iss.DeletedAt != nil:
		return deletedStyle.Render("[deleted]")
	case iss.Status == "closed":
		return closedStyle.Render("closed")
	default:
		return openStyle.Render("open")
	}
}

// ownerText flattens a *string owner to display form ("" when unset
// so truncate's no-op branch handles the empty case cleanly).
func ownerText(owner *string) string {
	if owner == nil {
		return ""
	}
	return *owner
}

// truncate cuts s to terminal-width w, appending an ellipsis. Width
// is measured in cells, so wide East-Asian glyphs and zero-width
// joiners are handled correctly.
func truncate(s string, w int) string {
	if w <= 0 || runewidth.StringWidth(s) <= w {
		return s
	}
	return runewidth.Truncate(s, w-1, "…")
}

// renderNow is the clock injection point for humanizeRelative.
// Production uses time.Now; snapshot tests override this to freeze
// time so the "Nh ago" column in golden files doesn't churn as
// wall-clock advances.
var renderNow = time.Now

// humanizeRelative renders a timestamp as a small human delta
// (e.g. "30s ago", "2h ago", "3d ago"). The zero value renders as a
// dash so empty rows in tests stay readable; malformed inputs fail
// earlier at JSON decode time and never reach this function.
func humanizeRelative(t time.Time) string {
	if t.IsZero() {
		return "-"
	}
	d := renderNow().Sub(t)
	switch {
	case d < time.Minute:
		return fmt.Sprintf("%ds ago", int(d.Seconds()))
	case d < time.Hour:
		return fmt.Sprintf("%dm ago", int(d.Minutes()))
	case d < 24*time.Hour:
		return fmt.Sprintf("%dh ago", int(d.Hours()))
	case d < 7*24*time.Hour:
		return fmt.Sprintf("%dd ago", int(d.Hours()/24))
	default:
		return fmt.Sprintf("%dw ago", int(d.Hours()/(24*7)))
	}
}
