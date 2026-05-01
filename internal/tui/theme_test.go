package tui

import (
	"io"
	"testing"

	"github.com/charmbracelet/lipgloss"
)

func TestResolveColorMode_NoColorOverridesAll(t *testing.T) {
	t.Setenv("NO_COLOR", "1")
	t.Setenv("KATA_COLOR_MODE", "dark")
	if got := resolveColorMode(); got != colorNone {
		t.Fatalf("NO_COLOR=1 must force colorNone, got %v", got)
	}
}

func TestResolveColorMode_KataColorModeRespected(t *testing.T) {
	cases := map[string]colorMode{
		"":      colorAuto,
		"auto":  colorAuto,
		"dark":  colorDark,
		"light": colorLight,
		"none":  colorNone,
	}
	for in, want := range cases {
		t.Run(in, func(t *testing.T) {
			t.Setenv("NO_COLOR", "")
			t.Setenv("KATA_COLOR_MODE", in)
			if got := resolveColorMode(); got != want {
				t.Fatalf("KATA_COLOR_MODE=%q -> %v, want %v", in, got, want)
			}
		})
	}
}

func TestResolveColorMode_InvalidFallsBackToAuto(t *testing.T) {
	t.Setenv("NO_COLOR", "")
	t.Setenv("KATA_COLOR_MODE", "rainbow")
	if got := resolveColorMode(); got != colorAuto {
		t.Fatalf("invalid value should fall back to colorAuto, got %v", got)
	}
}

func TestApplyColorMode_NoneStripsForeground(t *testing.T) {
	applyColorMode(colorNone, io.Discard)
	rendered := titleStyle.Render("hello")
	if rendered != "hello" {
		t.Fatalf("colorNone should render plain text, got %q", rendered)
	}
}

// TestApplyColorMode_RebuildsAllStyles guards against silently
// forgetting a style var in applyColorMode (which would leak the prior
// mode's value across boots). We pre-poison every var with a sentinel
// foreground (a real lipgloss.Color) so that GetForeground returns
// that exact value. After applyColorMode(colorNone) every var must
// have shed the sentinel foreground (colorNone leaves Foreground unset
// or a different value entirely).
func TestApplyColorMode_RebuildsAllStyles(t *testing.T) {
	sentinelColor := lipgloss.Color("999")
	sentinel := lipgloss.NewStyle().Foreground(sentinelColor)
	titleStyle = sentinel
	subtleStyle = sentinel
	statusStyle = sentinel
	selectedStyle = sentinel
	openStyle = sentinel
	closedStyle = sentinel
	deletedStyle = sentinel
	helpKeyStyle = sentinel
	helpDescStyle = sentinel
	errorStyle = sentinel
	toastStyle = sentinel
	chipStyle = sentinel
	chipActive = sentinel
	tabActive = sentinel
	tabInactive = sentinel

	applyColorMode(colorNone, io.Discard)

	all := []lipgloss.Style{
		titleStyle, subtleStyle, statusStyle, selectedStyle,
		openStyle, closedStyle, deletedStyle, helpKeyStyle,
		helpDescStyle, errorStyle, toastStyle, chipStyle,
		chipActive, tabActive, tabInactive,
	}
	for i, s := range all {
		if fg, ok := s.GetForeground().(lipgloss.Color); ok && fg == sentinelColor {
			t.Fatalf("style %d not rebuilt by applyColorMode(colorNone): retained sentinel %q", i, fg)
		}
	}
}
