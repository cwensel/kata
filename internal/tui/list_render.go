package tui

import (
	"fmt"
	"strings"
	"time"

	"github.com/charmbracelet/lipgloss"
	"github.com/charmbracelet/lipgloss/table"
	"github.com/mattn/go-runewidth"
)

// viewChrome carries the cross-cutting render inputs that lm.View
// needs to draw the title bar, status line, and footer help row.
// Plumbed from Model so the list view doesn't have to reach back into
// parent state. Zero-value renders a "minimal chrome" view (used by
// snapshot tests that exercise just the table) — no version, no SSE
// indicator text, no toast.
type viewChrome struct {
	scope     scope        // project / counts / version go in the title bar
	sseStatus sseConnState // "connected" / "reconnecting" / "disconnected"
	pending   bool         // pendingRefetch — surfaced as "0 / 1+ pending events"
	toast     *toast       // optional flash message (e.g. "resynced")
	version   string       // build-time version string for the title bar; "" hides
}

// View renders the list under the M1 chrome layer: title bar, status
// line, optional inline prompt or chip strip, headered table with
// hairline-rule separators, footer status + scroll indicator, and
// footer help row.
//
// width and height are the full terminal dimensions. listBodyHeight
// reserves space for every chrome line so the table never pushes the
// footer off screen; the table itself is windowed by windowIssues so
// the cursor row is always visible.
//
// chrome carries the cross-cutting bits (scope, SSE state, version,
// toast). When chrome.scope is the zero scope, the title bar
// degrades to just "kata" + version — useful for snapshot tests.
func (lm listModel) View(width, height int, chrome viewChrome) string {
	if lm.loading {
		return statusStyle.Render("loading…")
	}
	if lm.err != nil {
		return errorStyle.Render(lm.err.Error())
	}
	title := renderTitleBar(width, chrome.scope, lm.issueCounts(), chrome.version)
	statusBar := renderStatusLine(width, chrome.sseStatus, chrome.pending, lm.actor)
	header := lm.renderHeader()
	helpRow := renderHelpBar(listHelpItems(), width)
	bodyH := listBodyHeight(height, title, statusBar, header, helpRow)
	body := lm.renderBody(width, bodyH)
	footer := lm.renderFooterStatusLine(width, bodyH)
	parts := []string{title, statusBar}
	if header != "" {
		parts = append(parts, header)
	}
	parts = append(parts, body, footer)
	if t := renderToast(chrome.toast); t != "" {
		parts = append(parts, t)
	}
	parts = append(parts, helpRow)
	return joinNonEmpty(parts)
}

// listBodyHeight returns the row budget for the table body given the
// total terminal height and the rendered chrome strings. Each piece's
// rendered line count is subtracted; the remainder (with a floor) is
// the table's data-row capacity.
//
// Reserve order: title (≥1) + status (≥1) + header strip (0/1) + the
// table's own border/header overhead (~5 rows for the hairline
// borders + column header + middle rule + bottom rule) + footer
// status line (1) + footer help row (1+, reflow-dependent).
func listBodyHeight(total int, chromeStrings ...string) int {
	if total <= 0 {
		return listBodyFloor
	}
	chromeLines := tableOverheadRows
	for _, s := range chromeStrings {
		chromeLines += countLines(s)
	}
	chromeLines++ // footer status line
	avail := total - chromeLines
	if avail < listBodyFloor {
		return listBodyFloor
	}
	return avail
}

// tableOverheadRows is the constant number of rows the lipgloss table
// reserves for its own chrome (top rule + column header + middle rule
// + bottom rule = 4) when configured with hairline rules. Locked here
// so listBodyHeight stays in sync with renderBody's table config.
const tableOverheadRows = 4

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

// listBodyFloor is the minimum row count the table will ever render.
// Small enough to fit on a 10-line terminal alongside the header and
// footer; large enough to preserve a sense of context.
const listBodyFloor = 5

// countLines returns the line count of a rendered block (the rune count
// of '\n' plus one). Empty input is zero so renderHeader's quiet path
// reserves no extra space.
func countLines(s string) int {
	if s == "" {
		return 0
	}
	return strings.Count(s, "\n") + 1
}

// renderHeader returns the prompt (when inputting) or chip strip.
// Empty string when neither is active so the table sits flush against
// the top.
func (lm listModel) renderHeader() string {
	if lm.search.inputting {
		return renderPrompt(lm.search)
	}
	return renderChips(lm.filter)
}

// renderTitleBar formats the top-line title: project name + counts +
// version. Layout is left-aligned summary, right-aligned version, with
// padding between them so wide terminals stretch cleanly. width <= 0
// renders without padding (snapshot fallback).
//
// The summary degrades gracefully when scope is the zero value (no
// project bound): just "kata" appears, no counts. Useful for the
// empty-state and very-narrow-terminal hint paths.
func renderTitleBar(width int, sc scope, c issueCounts, version string) string {
	left := titleLeftSummary(sc, c)
	right := version
	return padLeftRight(left, right, width)
}

// titleLeftSummary builds the left-side text for the title bar. When
// sc.projectName is set, the format is `kata · {project} · open:N
// closed:N all:N`; when not, just `kata`.
func titleLeftSummary(sc scope, c issueCounts) string {
	parts := []string{titleStyle.Render("kata")}
	if name := sanitizeForDisplay(sc.projectName); name != "" {
		parts = append(parts, name)
	}
	if c.all > 0 {
		parts = append(parts, fmt.Sprintf("open:%d closed:%d all:%d",
			c.open, c.closed, c.all))
	}
	return strings.Join(parts, " · ")
}

// renderStatusLine formats the second-line status: SSE state +
// pending-events indicator + actor. Same left/right layout as the
// title bar.
func renderStatusLine(width int, state sseConnState, pending bool, actor string) string {
	left := sseSummary(state, pending)
	right := sanitizeForDisplay(actor)
	return padLeftRight(left, right, width)
}

// sseSummary renders "SSE: connected · 0 pending events" /
// "reconnecting" / "disconnected" — concise enough to fit alongside
// the actor name on the same line at 80 cols.
func sseSummary(state sseConnState, pending bool) string {
	var word string
	switch state {
	case sseReconnecting:
		word = "reconnecting"
	case sseDisconnected:
		word = "disconnected"
	default:
		word = "connected"
	}
	pendingNum := "0"
	if pending {
		pendingNum = "1+"
	}
	return statusStyle.Render(fmt.Sprintf("SSE: %s · %s pending events", word, pendingNum))
}

// padLeftRight composes a single-line string with left text on the
// left and right text on the right, separated by enough spaces to
// fill width. Right text is dropped when it would push the line over
// width (degrades to just left).
func padLeftRight(left, right string, width int) string {
	if width <= 0 {
		if right == "" {
			return left
		}
		return left + "  " + right
	}
	lw := runewidth.StringWidth(stripANSI(left))
	rw := runewidth.StringWidth(stripANSI(right))
	if lw+rw+1 > width {
		// Right side doesn't fit; truncate it.
		return left
	}
	gap := width - lw - rw
	if gap < 1 {
		gap = 1
	}
	return left + strings.Repeat(" ", gap) + right
}

// stripANSI removes ANSI escape sequences from s for width math (so
// padding accounts for visible runes only). Reuses the sanitize
// pattern from sanitize.go so the algorithms stay consistent.
func stripANSI(s string) string {
	return ansiEscapePattern.ReplaceAllString(s, "")
}

// renderFooterStatusLine combines the mutation-status flash (left)
// with the scroll indicator (right) on a single line below the table.
// The status flash takes priority — when both would render, the flash
// occupies the line and the scroll indicator is dropped (matches
// roborev's render_review.go:251-256 priority).
//
// bodyH is the table's data-row budget; combined with len(filteredIssues)
// and lm.cursor it determines whether a scroll indicator is needed.
func (lm listModel) renderFooterStatusLine(width, bodyH int) string {
	left := lm.status
	right := ""
	visible := filteredIssues(lm.issues, lm.filter)
	if n := len(visible); n > bodyH {
		start, end := windowBounds(n, lm.cursor, bodyH)
		right = statusStyle.Render(fmt.Sprintf("[%d-%d of %d issues]", start+1, end, n))
	}
	if left != "" {
		// Flash takes priority — drop the scroll indicator so the user
		// sees the mutation result without competing chrome.
		return statusStyle.Render(left)
	}
	return padLeftRight("", right, width)
}

// listHelpItems is the persistent footer help row for the list view's
// default state. The inline command bar (M3a) and modal forms (M4)
// will swap in their own help row when active.
func listHelpItems() []helpRow {
	return []helpRow{
		{key: "j/k", desc: "move"},
		{key: "enter", desc: "open"},
		{key: "n", desc: "new"},
		{key: "/", desc: "search"},
		{key: "o", desc: "owner"},
		{key: "s", desc: "status"},
		{key: "c", desc: "clear"},
		{key: "x", desc: "close"},
		{key: "r", desc: "reopen"},
		{key: "?", desc: "help"},
		{key: "q", desc: "quit"},
	}
}

// renderBody is the main table or the empty-state hint. Owner/Author/
// Search filters are applied client-side here so the chip strip and the
// rendered rows stay in sync (Status is server-side and already
// reflected in lm.issues). The cursor still indexes lm.issues, so we
// clamp the visual marker to the filtered length for placement only —
// no state mutation in the render path.
//
// Long lists are windowed around the cursor (windowIssues): the
// visible slice always contains the cursor row.
//
// Borders: top + middle (under the column header) + bottom hairline
// rules; no left/right/column borders. Mirrors roborev's queue table
// (`render_queue.go:444-456`) so kata and roborev feel consistent.
// The header row is enabled so column titles get the middle rule
// underline.
func (lm listModel) renderBody(width, height int) string {
	issues := filteredIssues(lm.issues, lm.filter)
	if len(issues) == 0 {
		return statusStyle.Render(
			"no issues match. press c to clear filters or n to create one.",
		)
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
		Border(lipgloss.Border{Top: "─", Bottom: "─", Middle: "─"}).
		BorderTop(true).
		BorderBottom(true).
		BorderHeader(true).
		BorderLeft(false).
		BorderRight(false).
		BorderColumn(false).
		BorderRow(false).
		Width(width).
		Wrap(false).
		Headers("", "#", "status", "title", "owner", "updated").
		Rows(rows...).
		StyleFunc(func(row, col int) lipgloss.Style {
			s := lipgloss.NewStyle().Width(cols.byIndex(col)).PaddingRight(1)
			if row == table.HeaderRow {
				return s.Inherit(subtleStyle)
			}
			if row >= 0 && row < len(rows) && row == vCursor {
				s = s.Inherit(selectedStyle)
			}
			return s
		})
	return t.Render()
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
		cursor:  2,  // "›" + padding
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
// non-list views (help / empty) when the SSE consumer is degraded. The
// list view renders its own SSE indicator inline via renderStatusLine,
// so this helper only fires for views that don't carry the M1 chrome.
func renderSSEStatus(state sseConnState) string {
	switch state {
	case sseReconnecting:
		return statusStyle.Render("kata: reconnecting…")
	case sseDisconnected:
		return statusStyle.Render("kata: disconnected (retrying)")
	}
	return ""
}

// renderToast wraps an active toast for footer display. nil toast
// renders as empty so the steady state is unchanged.
func renderToast(t *toast) string {
	if t == nil {
		return ""
	}
	return toastStyle.Render(t.text)
}

// renderPrompt formats the inline input. The cursor is a literal block
// glyph appended to the buffer so tests can assert on a deterministic
// shape; a richer caret blink lands later. The buffer is sanitized
// because filterPrintable already drops Unicode controls but a
// pasted-in ANSI escape could still slip through; this is a safety net.
func renderPrompt(s searchState) string {
	label := promptLabel(s.field)
	body := fmt.Sprintf("%s%s_  (esc to cancel)", label, sanitizeForDisplay(s.buffer))
	return chipActive.Render(body)
}

// promptLabel maps the active field to its prompt prefix.
func promptLabel(f searchField) string {
	switch f {
	case searchFieldQuery:
		return "search:"
	case searchFieldOwner:
		return "owner:"
	case searchFieldNewTitle:
		return "new title:"
	default:
		return ""
	}
}

// renderChips returns one chip per active filter slot. Inactive defaults
// (status="", owner="", search="") are skipped so the strip stays empty
// when the user has not constrained the list.
//
// The label chip is omitted because the label-filter UI was retired:
// the Issue projection drops Labels and the wire has no label query
// param yet, so neither the chip nor the prompt would behave honestly.
// ListFilter.Labels stays on the type (used internally by tests and
// reserved for future wire support) but no UI exposes it today.
func renderChips(f ListFilter) string {
	chips := []string{}
	if f.Status != "" {
		// Status is a daemon-defined keyword; no sanitization needed.
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
			fmt.Sprintf("q:%q", sanitizeForDisplay(f.Search))))
	}
	if len(chips) == 0 {
		return ""
	}
	return strings.Join(chips, "  ")
}

// joinNonEmpty assembles the view from its non-empty sections. A naive
// strings.Join would leave blank lines between absent sections; this
// drops them so the table starts at row 0 in the steady state.
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
// width (PaddingRight(1) eats the slack). Title and owner are
// agent-authored so both run through sanitizeForDisplay before
// truncation.
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

// selMarker is the per-row arrow glyph; ' ' for unselected so the column
// width stays stable.
func selMarker(selected bool) string {
	if selected {
		return "›"
	}
	return " "
}

// statusChip picks the right colored chip text for the issue. Soft-deleted
// rows win over closed.
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

// ownerText flattens a *string owner to display form ("" when unset so
// truncate's no-op branch handles the empty case cleanly).
func ownerText(owner *string) string {
	if owner == nil {
		return ""
	}
	return *owner
}

// truncate cuts s to terminal-width w, appending an ellipsis. Width is
// measured in cells, so wide East-Asian glyphs and zero-width joiners
// are handled correctly.
func truncate(s string, w int) string {
	if w <= 0 || runewidth.StringWidth(s) <= w {
		return s
	}
	return runewidth.Truncate(s, w-1, "…")
}

// renderNow is the clock injection point for humanizeRelative. Production
// uses time.Now; snapshot tests override this to freeze time so the "Nh
// ago" column in golden files doesn't churn as wall-clock advances.
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
