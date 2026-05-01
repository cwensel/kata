package tui

import (
	"fmt"
	"time"

	"github.com/charmbracelet/lipgloss"
	"github.com/charmbracelet/lipgloss/table"
	"github.com/mattn/go-runewidth"
)

// View renders the list as a hidden-bordered lipgloss table. The cursor
// row is highlighted via the table's StyleFunc; see selMarker for the
// in-row glyph. Width is the full terminal width; the title column
// flex-fills whatever the fixed columns leave behind.
func (lm listModel) View(width, _ int) string {
	if lm.loading {
		return statusStyle.Render("loading…")
	}
	if lm.err != nil {
		return errorStyle.Render(lm.err.Error())
	}
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

// humanizeRelative renders an RFC3339 timestamp as a small human delta
// (e.g. "30s ago", "2h ago", "3d ago"). Unparseable input falls back to
// the YYYY-MM-DD prefix or the raw string when too short.
func humanizeRelative(rfc3339 string) string {
	t, err := time.Parse(time.RFC3339, rfc3339)
	if err != nil {
		if len(rfc3339) >= 10 {
			return rfc3339[:10]
		}
		return rfc3339
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
