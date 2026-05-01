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
// flex-fills whatever the fixed columns leave behind.
//
// A single header row above the table holds either the active inline
// prompt (search/owner/label/new title) or the chip strip summarizing
// active filters. A status line below renders one-shot mutation
// feedback ("created #4") until the next keypress clears it.
func (lm listModel) View(width, _ int) string {
	if lm.loading {
		return statusStyle.Render("loading…")
	}
	if lm.err != nil {
		return errorStyle.Render(lm.err.Error())
	}
	header := lm.renderHeader()
	body := lm.renderBody(width)
	footer := lm.renderFooter()
	return joinNonEmpty([]string{header, body, footer})
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

// renderBody is the main table or the empty-state hint.
func (lm listModel) renderBody(width int) string {
	if len(lm.issues) == 0 {
		return statusStyle.Render(
			"no issues match. press c to clear filters or n to create one.",
		)
	}
	rows := buildRows(lm.issues, lm.cursor, width)
	t := table.New().
		Border(lipgloss.HiddenBorder()).
		Width(width).
		Wrap(false).
		Rows(rows...).
		StyleFunc(func(row, _ int) lipgloss.Style {
			s := lipgloss.NewStyle()
			if row >= 0 && row < len(rows) && row == lm.cursor {
				s = s.Inherit(selectedStyle)
			}
			return s
		})
	return t.Render()
}

// renderFooter is the one-shot status line. It is the seed of the
// future toast machinery (Task 12); for now it is plain text.
func (lm listModel) renderFooter() string {
	if lm.status == "" {
		return ""
	}
	return statusStyle.Render(lm.status)
}

// renderPrompt formats the inline input. The cursor is a literal block
// glyph appended to the buffer so tests can assert on a deterministic
// shape; a richer caret blink lands later.
func renderPrompt(s searchState) string {
	label := promptLabel(s.field)
	body := fmt.Sprintf("%s%s_  (esc to cancel)", label, s.buffer)
	return chipActive.Render(body)
}

// promptLabel maps the active field to its prompt prefix.
func promptLabel(f searchField) string {
	switch f {
	case searchFieldQuery:
		return "search:"
	case searchFieldOwner:
		return "owner:"
	case searchFieldLabel:
		return "label:"
	case searchFieldNewTitle:
		return "new title:"
	default:
		return ""
	}
}

// renderChips returns one chip per active filter slot. Inactive defaults
// (status="", owner="", labels nil, search="") are skipped so the
// strip stays empty when the user has not constrained the list.
func renderChips(f ListFilter) string {
	chips := []string{}
	if f.Status != "" {
		chips = append(chips, chipActive.Render("status:"+f.Status))
	}
	if f.Owner != "" {
		chips = append(chips, chipStyle.Render("owner:"+f.Owner))
	}
	if len(f.Labels) > 0 {
		chips = append(chips, chipStyle.Render("label:"+strings.Join(f.Labels, ",")))
	}
	if f.Search != "" {
		chips = append(chips, chipStyle.Render(fmt.Sprintf("q:%q", f.Search)))
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
// other columns whole.
func buildRows(issues []Issue, cursor, width int) [][]string {
	titleW := max(20, width-50)
	rows := make([][]string, 0, len(issues))
	for i, iss := range issues {
		rows = append(rows, []string{
			selMarker(i == cursor),
			fmt.Sprintf("#%d", iss.Number),
			statusChip(iss),
			truncate(iss.Title, titleW),
			truncate(ownerText(iss.Owner), 12),
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

// humanizeRelative renders a timestamp as a small human delta
// (e.g. "30s ago", "2h ago", "3d ago"). The zero value renders as a
// dash so empty rows in tests stay readable; malformed inputs fail
// earlier at JSON decode time and never reach this function.
func humanizeRelative(t time.Time) string {
	if t.IsZero() {
		return "-"
	}
	d := time.Since(t)
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
