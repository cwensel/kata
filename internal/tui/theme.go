package tui

import (
	"os"
	"strings"

	"github.com/charmbracelet/lipgloss"
)

type colorMode int

const (
	colorAuto colorMode = iota
	colorDark
	colorLight
	colorNone
)

// resolveColorMode honors NO_COLOR (any non-empty value) over
// KATA_COLOR_MODE. Unrecognized values fall back to auto.
func resolveColorMode() colorMode {
	if v := os.Getenv("NO_COLOR"); v != "" {
		return colorNone
	}
	switch strings.ToLower(os.Getenv("KATA_COLOR_MODE")) {
	case "dark":
		return colorDark
	case "light":
		return colorLight
	case "none":
		return colorNone
	default:
		return colorAuto
	}
}

// Style vars are package-level so View() functions don't reach into
// state. applyColorMode rebuilds them once at boot.
var (
	titleStyle    lipgloss.Style
	subtleStyle   lipgloss.Style
	statusStyle   lipgloss.Style
	selectedStyle lipgloss.Style
	openStyle     lipgloss.Style
	closedStyle   lipgloss.Style
	deletedStyle  lipgloss.Style
	helpKeyStyle  lipgloss.Style
	helpDescStyle lipgloss.Style
	errorStyle    lipgloss.Style
	toastStyle    lipgloss.Style
	chipStyle     lipgloss.Style
	chipActive    lipgloss.Style
	tabActive     lipgloss.Style
	tabInactive   lipgloss.Style
)

// applyColorMode rebuilds all package-level styles. Called at TUI boot
// so tests can swap modes without leaking state across tests.
func applyColorMode(m colorMode) {
	if m == colorNone {
		base := lipgloss.NewStyle()
		titleStyle = base.Bold(true)
		subtleStyle = base
		statusStyle = base
		selectedStyle = base.Reverse(true)
		openStyle = base
		closedStyle = base
		deletedStyle = base.Faint(true)
		helpKeyStyle = base.Bold(true)
		helpDescStyle = base
		errorStyle = base.Bold(true)
		toastStyle = base.Bold(true)
		chipStyle = base
		chipActive = base.Bold(true)
		tabActive = base.Bold(true).Underline(true)
		tabInactive = base.Faint(true)
		return
	}
	pick := func(light, dark string) lipgloss.TerminalColor {
		switch m {
		case colorLight:
			return lipgloss.Color(light)
		case colorDark:
			return lipgloss.Color(dark)
		default:
			return lipgloss.AdaptiveColor{Light: light, Dark: dark}
		}
	}
	titleStyle = lipgloss.NewStyle().Bold(true).Foreground(pick("125", "205"))
	subtleStyle = lipgloss.NewStyle().Foreground(pick("242", "246"))
	statusStyle = lipgloss.NewStyle().Foreground(pick("242", "246"))
	selectedStyle = lipgloss.NewStyle().Background(pick("153", "24"))
	openStyle = lipgloss.NewStyle().Foreground(pick("28", "46"))
	closedStyle = lipgloss.NewStyle().Foreground(pick("30", "51"))
	deletedStyle = lipgloss.NewStyle().Faint(true).Foreground(pick("243", "245"))
	helpKeyStyle = lipgloss.NewStyle().Foreground(pick("242", "246"))
	helpDescStyle = lipgloss.NewStyle().Foreground(pick("248", "240"))
	errorStyle = lipgloss.NewStyle().Bold(true).Foreground(pick("124", "196"))
	toastStyle = lipgloss.NewStyle().Bold(true).Foreground(pick("28", "46"))
	chipStyle = lipgloss.NewStyle().Foreground(pick("242", "246"))
	chipActive = lipgloss.NewStyle().Bold(true).Foreground(pick("125", "205"))
	tabActive = lipgloss.NewStyle().Bold(true).Underline(true).Foreground(pick("125", "205"))
	tabInactive = lipgloss.NewStyle().Foreground(pick("242", "246"))
}

// applyDefaultColorMode is called from initialModel so style vars are
// always populated, even in tests that bypass Run().
func applyDefaultColorMode() { applyColorMode(resolveColorMode()) }
