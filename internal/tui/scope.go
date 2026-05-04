package tui

import (
	"strings"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
)

// handleScopeToggle implements the R binding. Today the cross-project
// surface is gated off (the daemon has no GET /issues route), so R is
// a toast-only no-op explaining the gate. Re-enable the actual toggle
// once handlers_issues.go ships the cross-project endpoint and the
// list model has a wire path that won't 404.
//
// The pre-gate behavior is intentionally not preserved as a fallback:
// silently switching into a 404-backed mode would land the user on an
// error screen with no clue why. Better to surface the gate.
func (m Model) handleScopeToggle() (Model, tea.Cmd) {
	return m.toastScopeGated()
}

// toastScopeGated surfaces a hint that the all-projects surface is
// gated until the daemon ships cross-project list support. The TTL
// matches the no-binding toast's cadence — long enough to read.
func (m Model) toastScopeGated() (Model, tea.Cmd) {
	m.toast = &toast{
		text:      "all-projects not available yet (daemon route pending)",
		level:     toastError,
		expiresAt: m.toastNow().Add(toastNoBindingTTL),
	}
	return m, toastExpireCmd(toastNoBindingTTL)
}

// renderEmpty draws the centered onboarding hint shown when the daemon
// has zero registered projects. lipgloss.Place handles vertical and
// horizontal centering inside width × height; small terminals fall back
// to top-left placement (lipgloss caps the offsets) so the message
// remains visible.
func renderEmpty(width, height int) string {
	body := strings.Join([]string{
		titleStyle.Render("no kata projects registered yet"),
		"",
		subtleStyle.Render("run `kata init` in a repo to get started."),
		subtleStyle.Render("press q to quit."),
	}, "\n")
	if width <= 0 || height <= 0 {
		return body
	}
	return lipgloss.Place(width, height, lipgloss.Center, lipgloss.Center, body)
}

// renderTooNarrow is the degraded full-screen hint shown when the
// terminal width can't fit readable table columns + chip strip.
//
// q / ctrl+c still route through Model.routeGlobalKey, so the user
// can quit from the hint screen without resizing first.
func renderTooNarrow(width, height int) string {
	msg := strings.Join([]string{
		"kata tui needs more space",
		"",
		">=80 columns wide",
		"resize your terminal and try again",
		"",
		"press q to quit",
	}, "\n")
	body := lipgloss.NewStyle().
		Border(lipgloss.NormalBorder()).
		BorderForeground(panelActiveBorder).
		Padding(1, 2).
		Render(msg)
	if width <= 0 || height <= 0 {
		return body
	}
	return lipgloss.Place(width, height, lipgloss.Center, lipgloss.Center, body)
}
