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
//
// The palette mirrors roborev's (cmd/roborev/tui/tui.go:38-77) so the
// two TUIs feel consistent. Where kata's status semantics differ from
// roborev's, the colors are remapped: openStyle reuses roborev's
// passStyle (green), closedStyle keeps the cyan, deletedStyle reuses
// roborev's failStyle (red) with Faint so deleted rows read as
// out-of-band rather than alarming.
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

// Border colors used by M3+ render code for panel chrome (focused vs
// unfocused panes, form/prompt boxes). Stored as lipgloss.TerminalColor
// so callers pass them straight to BorderForeground without re-resolving
// the color mode. Re-bound by applyColorMode so KATA_COLOR_MODE picks
// the right shade.
var (
	panelActiveBorder   lipgloss.TerminalColor // magenta
	panelInactiveBorder lipgloss.TerminalColor // gray
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
		// Borders carry no foreground in colorNone — lipgloss renders
		// them in the default terminal color. NoColor is the closest
		// stand-in for "use whatever the terminal would otherwise pick."
		panelActiveBorder = lipgloss.NoColor{}
		panelInactiveBorder = lipgloss.NoColor{}
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
	// deletedStyle is the dim-red semantic remap of roborev's failStyle
	// — design doc §"Visual language". Faint avoids reading as alarming
	// while still distinguishing soft-deleted rows from open/closed.
	deletedStyle = r.NewStyle().Faint(true).Foreground(pick("124", "196"))
	helpKeyStyle = r.NewStyle().Foreground(pick("242", "246"))
	helpDescStyle = r.NewStyle().Foreground(pick("248", "240"))
	errorStyle = r.NewStyle().Bold(true).Foreground(pick("124", "196"))
	toastStyle = r.NewStyle().Bold(true).Foreground(pick("28", "46"))
	chipStyle = r.NewStyle().Foreground(pick("242", "246"))
	chipActive = r.NewStyle().Bold(true).Foreground(pick("125", "205"))
	tabActive = r.NewStyle().Bold(true).Underline(true).Foreground(pick("125", "205"))
	tabInactive = r.NewStyle().Foreground(pick("242", "246"))
	panelActiveBorder = pick("125", "205")
	panelInactiveBorder = pick("242", "246")
}

// applyDefaultColorMode wires the resolved color mode to the active
// output writer. Called from Run so style vars are always populated
// against the real stream.
func applyDefaultColorMode(w io.Writer) { applyColorMode(resolveColorMode(), w) }
