package tui

import (
	"github.com/charmbracelet/lipgloss"
)

// renderInputBar formats the inline command bar for an active
// inputSearchBar / inputOwnerBar. Single-line bordered box, title in
// the top border, magenta border (always focused while open).
// Sanitizes the rendered buffer text as a safety net so a pasted ANSI
// sequence can't reach the terminal.
func renderInputBar(s inputState, width int) string {
	if width < 10 {
		width = 10
	}
	box := lipgloss.NewStyle().
		Border(lipgloss.NormalBorder()).
		BorderForeground(panelActiveBorder).
		Width(width-2). // -2 for the side borders
		Padding(0, 1)
	body := sanitizeForDisplay(s.activeField().input.View())
	rendered := box.Render(body)
	// Embed the title in the top border via a manual overlay: lipgloss
	// doesn't expose a "title in border" primitive yet, so prepend a
	// labeled top line and let the box's own top border act as the
	// underline.
	title := titleStyle.Render(" " + s.title + " ")
	return title + "\n" + rendered
}

// renderPanelPrompt is the M3b shell — short single-field prompt
// anchored to the bottom of the detail pane. Stub for now so the
// kind dispatch table is complete; M3b fills it in.
//
//nolint:unused // reserved for M3b panel-local prompts
func renderPanelPrompt(_ inputState, _ int) string { return "" }

// renderCenteredForm is the M4 shell — multi-field form centered on
// screen via lipgloss.Place. Stub for now; M4 fills it in.
//
//nolint:unused // reserved for M4 centered forms
func renderCenteredForm(_ inputState, _, _ int) string { return "" }
