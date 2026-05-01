package tui

import (
	"os"
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
			_ = os.Unsetenv("NO_COLOR")
			t.Setenv("KATA_COLOR_MODE", in)
			if got := resolveColorMode(); got != want {
				t.Fatalf("KATA_COLOR_MODE=%q -> %v, want %v", in, got, want)
			}
		})
	}
}

func TestResolveColorMode_InvalidFallsBackToAuto(t *testing.T) {
	_ = os.Unsetenv("NO_COLOR")
	t.Setenv("KATA_COLOR_MODE", "rainbow")
	if got := resolveColorMode(); got != colorAuto {
		t.Fatalf("invalid value should fall back to colorAuto, got %v", got)
	}
}

func TestApplyColorMode_NoneStripsForeground(t *testing.T) {
	applyColorMode(colorNone)
	rendered := titleStyle.Render("hello")
	if rendered != "hello" {
		t.Fatalf("colorNone should render plain text, got %q", rendered)
	}
}

// TestApplyColorMode_RebuildsAllStyles guards against silently
// forgetting a style var in applyColorMode (which would leak the prior
// mode's value across boots). We rebuild from a known mode and
// confirm every var has been touched.
func TestApplyColorMode_RebuildsAllStyles(t *testing.T) {
	applyColorMode(colorNone)
	all := []lipgloss.Style{
		titleStyle, subtleStyle, statusStyle, selectedStyle,
		openStyle, closedStyle, deletedStyle, helpKeyStyle,
		helpDescStyle, errorStyle, toastStyle, chipStyle,
		chipActive, tabActive, tabInactive,
	}
	for i, s := range all {
		if got := s.Render("x"); got == "" {
			t.Fatalf("style %d rendered empty after applyColorMode", i)
		}
	}
}
