package tui

import (
	"fmt"
	"strings"
	"time"

	"github.com/charmbracelet/lipgloss"
	"github.com/charmbracelet/lipgloss/table"
	"github.com/mattn/go-runewidth"
)

// View renders the list as a hidden-bordered lipgloss table. The cursor
// row is highlighted via the table's StyleFunc; see selMarker for the
// in-row glyph. Width is the full terminal width; the title column
// flex-fills whatever the fixed columns leave behind. Height bounds
// how many rows the table renders so the cursor stays in view as the
// list grows past the terminal.
//
// A single header row above the table holds either the active inline
// prompt (search/owner/new title) or the chip strip summarizing
// active filters. A status line below renders one-shot mutation
// feedback ("created #4") until the next keypress clears it.
func (lm listModel) View(width, height int) string {
	if lm.loading {
		return statusStyle.Render("loading…")
	}
	if lm.err != nil {
		return errorStyle.Render(lm.err.Error())
	}
	header := lm.renderHeader()
	body := lm.renderBody(width, listBodyHeight(height, header))
	footer := lm.renderFooter()
	return joinNonEmpty([]string{header, body, footer})
}

// listBodyHeight is the row budget for the table body given the total
// terminal height and the rendered chip/prompt header. We reserve a
// few lines for the footer (status line + SSE reconnect indicator +
// optional toast, each on its own line) so the table doesn't push
// them off-screen. Very small heights fall back to a small floor so
// the list still shows something rather than collapsing to zero.
func listBodyHeight(total int, header string) int {
	if total <= 0 {
		return listBodyFloor
	}
	headerLines := 0
	if header != "" {
		headerLines = countLines(header)
	}
	// Reserve 3 lines for footer extras (status, SSE state, toast).
	avail := total - headerLines - 3
	if avail < listBodyFloor {
		return listBodyFloor
	}
	return avail
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

// renderBody is the main table or the empty-state hint. Owner/Author/
// Search filters are applied client-side here so the chip strip and the
// rendered rows stay in sync (Status is server-side and already
// reflected in lm.issues). The cursor still indexes lm.issues, so we
// clamp the visual marker to the filtered length for placement only —
// no state mutation in the render path.
//
// Long lists are windowed around the cursor: the visible slice always
// contains the cursor row, with as much context as the body height
// allows. Without windowing the table would render every row and push
// the cursor off the bottom of the terminal once the count exceeded
// the screen.
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
	rows := buildRows(visible, vCursor, width)
	t := table.New().
		Border(lipgloss.HiddenBorder()).
		Width(width).
		Wrap(false).
		Rows(rows...).
		StyleFunc(func(row, _ int) lipgloss.Style {
			s := lipgloss.NewStyle()
			if row >= 0 && row < len(rows) && row == vCursor {
				s = s.Inherit(selectedStyle)
			}
			return s
		})
	return t.Render()
}

// windowIssues returns the contiguous slice of issues that includes
// the cursor row and fits within budget. The cursor index in the
// returned slice (vCursor) is the local position so the table renderer
// can highlight the right row.
//
// The window slides so the cursor sits anywhere from the top to the
// bottom of the viewport, preferring to anchor at the top until the
// cursor moves past the budget, then scrolling to keep the cursor
// near the bottom. Budget < 1 falls back to a single-row window so we
// always render the cursor.
func windowIssues(issues []Issue, cursor, budget int) ([]Issue, int) {
	n := len(issues)
	if n == 0 {
		return issues, 0
	}
	if budget < 1 {
		budget = 1
	}
	if n <= budget {
		return issues, cursor
	}
	// Anchor the window so the cursor is visible. We use a "two-thirds
	// from the top" anchor so the user sees more upcoming rows than
	// scrolled-past rows — matches the conventional vim/less feel.
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
	return issues[start:end], cursor - start
}

// renderFooter is the one-shot status line. It renders the active
// mutation status (lm.status), the SSE reconnect indicator (when the
// SSE consumer is not in the connected state), and the active toast
// (when present), each on its own line. Tests still see lm.status
// because they drive Update directly without the SSE goroutine.
func (lm listModel) renderFooter() string {
	if lm.status == "" {
		return ""
	}
	return statusStyle.Render(lm.status)
}

// renderSSEStatus returns the connection-status line rendered below the
// list table. Empty when the consumer is in the connected state so the
// steady state shows nothing extra.
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
// The title column flexes to width-50 (with a 20ch floor) to leave the
// other columns whole. Title and owner are agent-authored, so both run
// through sanitizeForDisplay before truncation to keep ANSI / control
// sequences out of the rendered cells.
func buildRows(issues []Issue, cursor, width int) [][]string {
	titleW := max(20, width-50)
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
