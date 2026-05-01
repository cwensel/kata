package tui

import (
	"strings"

	"github.com/charmbracelet/lipgloss"
	"github.com/mattn/go-runewidth"
)

// helpRow is a binding row (key + desc); helpSection groups them under a
// title (Global/List/Detail).
type helpRow struct{ key, desc string }
type helpSection struct {
	title string
	rows  []helpRow
}

// helpSections returns bindings grouped by section in stable order.
// TestHelpSections_AllBindingsCovered fails CI when a keymap entry is
// missed here, so future binding additions must update this too.
func helpSections(km keymap) []helpSection {
	r := func(k key) helpRow { return helpRow{keyDisplay(k), k.Help} }
	return []helpSection{
		{"Global", []helpRow{r(km.Help), r(km.Quit), r(km.ToggleScope)}},
		{"List", []helpRow{
			r(km.Up), r(km.Down), r(km.PageUp), r(km.PageDown), r(km.Home),
			r(km.End), r(km.Open), r(km.NewIssue), r(km.Search),
			r(km.FilterStatus), r(km.FilterOwner), r(km.FilterLabel),
			r(km.ClearFilters), r(km.Close), r(km.Reopen),
		}},
		{"Detail", []helpRow{
			r(km.NextTab), r(km.PrevTab), r(km.JumpRef), r(km.Back),
			r(km.EditBody), r(km.NewComment), r(km.SetParent),
			r(km.AddBlocker), r(km.AddLink), r(km.AddLabel),
			r(km.RemoveLabel), r(km.AssignOwner), r(km.ClearOwner),
		}},
	}
}

// keyDisplay joins multi-key bindings with '/' (e.g. "q/ctrl+c").
func keyDisplay(k key) string { return strings.Join(k.Keys, "/") }

// renderHelp builds the help overlay. width picks column count.
func renderHelp(km keymap, width int, filter ListFilter) string {
	cols := chunkSections(helpSections(km), helpColumnCount(width))
	rendered := make([]string, len(cols))
	for i, g := range cols {
		rendered[i] = renderHelpGroup(g)
	}
	body := lipgloss.JoinHorizontal(lipgloss.Top, padColumns(rendered)...)
	parts := []string{titleStyle.Render("kata — keybindings"), ""}
	if chips := renderChips(filter); chips != "" {
		parts = append(parts, chips, "")
	}
	return strings.Join(append(parts, body, "",
		subtleStyle.Render("press ? to return")), "\n")
}

// helpColumnCount picks how many sections share a row at width w.
func helpColumnCount(w int) int {
	switch {
	case w >= 120:
		return 3
	case w >= 80:
		return 2
	}
	return 1
}

// chunkSections splits sections into rows of n (clamped to ≥1).
func chunkSections(s []helpSection, n int) [][]helpSection {
	if n < 1 {
		n = 1
	}
	out := [][]helpSection{}
	for i := 0; i < len(s); i += n {
		out = append(out, s[i:min(i+n, len(s))])
	}
	return out
}

// renderHelpGroup formats one row-chunk as a single column: bold title
// per section + 'key  desc' lines with keys padded to a uniform width.
func renderHelpGroup(group []helpSection) string {
	parts := []string{}
	for i, s := range group {
		if i > 0 {
			parts = append(parts, "")
		}
		parts = append(parts, titleStyle.Render(s.title))
		keyW := 0
		for _, r := range s.rows {
			if w := runewidth.StringWidth(r.key); w > keyW {
				keyW = w
			}
		}
		for _, r := range s.rows {
			pad := strings.Repeat(" ", keyW-runewidth.StringWidth(r.key)+2)
			parts = append(parts, helpKeyStyle.Render(r.key)+pad+
				helpDescStyle.Render(r.desc))
		}
	}
	return strings.Join(parts, "\n")
}

// padColumns equalizes column heights and appends a 4-space gutter so
// JoinHorizontal aligns at the top without columns running together.
func padColumns(cols []string) []string {
	maxN := 0
	for _, c := range cols {
		if n := strings.Count(c, "\n"); n > maxN {
			maxN = n
		}
	}
	out := make([]string, len(cols))
	for i, c := range cols {
		out[i] = c + strings.Repeat("\n", maxN-strings.Count(c, "\n")) + "    "
	}
	return out
}
