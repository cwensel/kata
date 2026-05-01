package tui

import (
	"io"
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
// so tests can swap modes without leaking state across tests. The
// renderer is bound to w so color-capability detection runs against the
// actual output stream (not the package-default os.Stdout-bound
// renderer, which is wrong when opts.Stdout is something else).
func applyColorMode(m colorMode, w io.Writer) {
	if w == nil {
		w = os.Stdout
	}
	r := lipgloss.NewRenderer(w)
	if m == colorNone {
		base := r.NewStyle()
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
	titleStyle = r.NewStyle().Bold(true).Foreground(pick("125", "205"))
	subtleStyle = r.NewStyle().Foreground(pick("242", "246"))
	statusStyle = r.NewStyle().Foreground(pick("242", "246"))
	selectedStyle = r.NewStyle().Background(pick("153", "24"))
	openStyle = r.NewStyle().Foreground(pick("28", "46"))
	closedStyle = r.NewStyle().Foreground(pick("30", "51"))
	deletedStyle = r.NewStyle().Faint(true).Foreground(pick("243", "245"))
	helpKeyStyle = r.NewStyle().Foreground(pick("242", "246"))
	helpDescStyle = r.NewStyle().Foreground(pick("248", "240"))
	errorStyle = r.NewStyle().Bold(true).Foreground(pick("124", "196"))
	toastStyle = r.NewStyle().Bold(true).Foreground(pick("28", "46"))
	chipStyle = r.NewStyle().Foreground(pick("242", "246"))
	chipActive = r.NewStyle().Bold(true).Foreground(pick("125", "205"))
	tabActive = r.NewStyle().Bold(true).Underline(true).Foreground(pick("125", "205"))
	tabInactive = r.NewStyle().Foreground(pick("242", "246"))
}

// applyDefaultColorMode wires the resolved color mode to the active
// output writer. Called from Run so style vars are always populated
// against the real stream.
func applyDefaultColorMode(w io.Writer) { applyColorMode(resolveColorMode(), w) }
