package tui

import (
	"github.com/charmbracelet/lipgloss"
)

// renderInputBar formats the inline command bar as a bordered box.
// Used by the M3a above-the-table layout; M3.5 moved bars to the
// info line via renderInfoBar instead. Kept for any caller that
// wants the heavier bordered presentation.
//
//nolint:unused // superseded by renderInfoBar in M3.5
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

// renderPanelPrompt is the M3b bordered panel-prompt shell. M3.5
// moved panel prompts to the info line via renderInfoPrompt for a
// lighter footprint; this stays for any caller wanting the heavier
// bordered presentation.
//
//nolint:unused // superseded by renderInfoPrompt in M3.5
func renderPanelPrompt(s inputState, width int) string {
	if width < 10 {
		width = 10
	}
	box := lipgloss.NewStyle().
		Border(lipgloss.NormalBorder()).
		BorderForeground(panelActiveBorder).
		Width(width-2).
		Padding(0, 1)
	body := sanitizeForDisplay(s.activeField().input.View())
	rendered := box.Render(body)
	title := titleStyle.Render(" " + s.title + " ")
	return title + "\n" + rendered
}

// renderCenteredForm is the M4 shell — multi-field form centered on
// screen via lipgloss.Place. Stub for now; M4 fills it in.
//
//nolint:unused // reserved for M4 centered forms
func renderCenteredForm(_ inputState, _, _ int) string { return "" }
