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
		{"Queue", []helpRow{
			r(km.Up), r(km.Down), r(km.PageUp), r(km.PageDown), r(km.Home),
			r(km.End), r(km.Open), r(km.ExpandCollapse), r(km.NewIssue),
			r(km.Close), r(km.Reopen),
		}},
		{"Detail", []helpRow{
			r(km.NextTab), r(km.PrevTab), r(km.JumpRef), r(km.Back),
			r(km.EditBody), r(km.NewComment), r(km.SetParent),
			r(km.AddBlocker), r(km.AddLink), r(km.AddLabel),
			r(km.RemoveLabel), r(km.AssignOwner), r(km.ClearOwner),
		}},
		{"Children", []helpRow{
			r(km.NewChild),
			{key: "↑↓", desc: "move child cursor"},
			{key: "enter", desc: "open child"},
		}},
		{"Forms", []helpRow{
			{key: "ctrl+s", desc: "save or apply"},
			{key: "esc", desc: "cancel"},
			{key: "tab/shift+tab", desc: "change field"},
			{key: "ctrl+e", desc: "open editor"},
			{key: "ctrl+u", desc: "clear prompt"},
		}},
		{"Filters", []helpRow{
			r(km.Search), r(km.FilterStatus), r(km.FilterForm), r(km.ClearFilters),
			{key: "ctrl+r", desc: "reset filter form"},
		}},
	}
}

// keyDisplay joins multi-key bindings with '/' (e.g. "q/ctrl+c").
func keyDisplay(k key) string {
	parts := make([]string, len(k.Keys))
	for i, binding := range k.Keys {
		if binding == " " {
			parts[i] = "space"
		} else {
			parts[i] = binding
		}
	}
	return strings.Join(parts, "/")
}

// reflowHelpRows redistributes helpRow items across rows so the rendered
// table fits within width. Each cell's visible width is key + space +
// desc, and non-first columns add 2 chars (▕ border + padding). width
// <= 0 returns rows unchanged.
//
// Lifted verbatim from roborev (`cmd/roborev/tui/tui.go::reflowHelpRows`)
// per the design lock §"Resolved decisions" #5 — no point getting
// clever; the algorithm is battle-tested and bounded.
//
// Currently unused after M3.5 — the persistent footer joins items
// inline via joinHelpItems instead of via a reflowed table. Kept
// for the help-overlay rebuild that M5 will land.
//
//nolint:unused // reserved for M5 help overlay re-style
func reflowHelpRows(rows [][]helpRow, width int) [][]helpRow {
	if width <= 0 {
		return rows
	}
	cellWidth := func(item helpRow) int {
		w := runewidth.StringWidth(item.key)
		if item.desc != "" {
			w += 1 + runewidth.StringWidth(item.desc)
		}
		return w
	}
	maxItemsPerRow := 0
	for _, row := range rows {
		if len(row) > maxItemsPerRow {
			maxItemsPerRow = len(row)
		}
	}
	for ncols := maxItemsPerRow; ncols >= 1; ncols-- {
		var candidate [][]helpRow
		for _, row := range rows {
			for i := 0; i < len(row); i += ncols {
				end := min(i+ncols, len(row))
				candidate = append(candidate, row[i:end])
			}
		}
		colW := make([]int, ncols)
		for _, crow := range candidate {
			for c, item := range crow {
				if w := cellWidth(item); w > colW[c] {
					colW[c] = w
				}
			}
		}
		total := 0
		for c, w := range colW {
			total += w
			if c > 0 {
				total += 2 // ▕ + padding
			}
		}
		if total <= width {
			return candidate
		}
	}
	// Fallback: one item per row.
	var result [][]helpRow
	for _, row := range rows {
		for _, item := range row {
			result = append(result, []helpRow{item})
		}
	}
	return result
}

// renderHelpBar renders a flat list of helpRow items as a single line
// (or a few wrapped lines, via reflowHelpRows) suitable for the
// persistent footer help row on the main views. Each item is rendered
// as `helpKeyStyle(key) " " helpDescStyle(desc)`; entries are joined
// with two spaces. Empty input renders as "".
//
// Currently unused after M3.5 — list/detail footers use renderFooterBar
// (joins items with ` │ ` and right-aligns a position indicator).
// Kept for the help-overlay rebuild that M5 will land.
//
//nolint:unused // reserved for M5 help overlay re-style
func renderHelpBar(items []helpRow, width int) string {
	if len(items) == 0 {
		return ""
	}
	rows := reflowHelpRows([][]helpRow{items}, width)
	out := make([]string, len(rows))
	for ri, row := range rows {
		parts := make([]string, len(row))
		for i, item := range row {
			if item.desc == "" {
				parts[i] = helpKeyStyle.Render(item.key)
			} else {
				parts[i] = helpKeyStyle.Render(item.key) + " " +
					helpDescStyle.Render(item.desc)
			}
		}
		out[ri] = strings.Join(parts, "  ")
	}
	return strings.Join(out, "\n")
}

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

// helpColumnCount picks how many on-screen columns the layout uses at
// width w. Wide terminals get the sections side-by-side; narrow ones
// stack everything in a single column.
func helpColumnCount(w int) int {
	switch {
	case w >= 120:
		return 3
	case w >= 80:
		return 2
	}
	return 1
}

// chunkSections splits sections into cols on-screen columns by packing
// sections vertically per column (ceil(len/cols) sections per column).
// cols < 1 is clamped so a zero/negative width still renders something.
//
// Earlier this function treated the second argument as "sections per
// chunk", which inverted the layout: helpColumnCount(120)=3 produced one
// chunk of three sections (one screen column) instead of three single-
// section chunks (three screen columns). The reframe matches the
// helpColumnCount contract.
func chunkSections(s []helpSection, cols int) [][]helpSection {
	if cols < 1 {
		cols = 1
	}
	perCol := (len(s) + cols - 1) / cols
	if perCol < 1 {
		perCol = 1
	}
	out := [][]helpSection{}
	for i := 0; i < len(s); i += perCol {
		out = append(out, s[i:min(i+perCol, len(s))])
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
