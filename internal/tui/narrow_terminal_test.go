package tui

import (
	"io"
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
)

// narrowHintMarker is the substring that uniquely identifies the
// renderTooNarrow hint screen. Centralized so the test bodies stay
// short and a future copy edit only touches one place.
const narrowHintMarker = "needs more space"

// narrowTestSetup pins the package to KATA_COLOR_MODE=none so View
// output is plain UTF-8, then returns a fresh Model with no loading
// flag set. The cleanup func reverts the color rebuild.
func narrowTestSetup(t *testing.T) (Model, func()) {
	t.Helper()
	t.Setenv("KATA_COLOR_MODE", "none")
	t.Setenv("NO_COLOR", "")
	applyDefaultColorMode(io.Discard)
	m := initialModel(Options{})
	m.list.loading = false
	cleanup := func() { applyDefaultColorMode(io.Discard) }
	return m, cleanup
}

// TestNarrowTerminal_NarrowWidthShowsHint verifies that a sub-80-column
// width trips the M5 short-circuit and renders the centered hint
// regardless of how tall the terminal is.
func TestNarrowTerminal_NarrowWidthShowsHint(t *testing.T) {
	m, cleanup := narrowTestSetup(t)
	defer cleanup()

	out, _ := m.Update(tea.WindowSizeMsg{Width: 60, Height: 24})
	m = out.(Model)
	got := m.View()
	if !strings.Contains(got, narrowHintMarker) {
		t.Fatalf("narrow width view missing hint marker %q; got:\n%s",
			narrowHintMarker, got)
	}
}

// TestNarrowTerminal_NarrowHeightShowsHint verifies that a sub-16-row
// height trips the short-circuit even when the width is comfortably
// above the 80-cell threshold.
func TestNarrowTerminal_NarrowHeightShowsHint(t *testing.T) {
	m, cleanup := narrowTestSetup(t)
	defer cleanup()

	out, _ := m.Update(tea.WindowSizeMsg{Width: 120, Height: 12})
	m = out.(Model)
	got := m.View()
	if !strings.Contains(got, narrowHintMarker) {
		t.Fatalf("narrow height view missing hint marker %q; got:\n%s",
			narrowHintMarker, got)
	}
}

// TestNarrowTerminal_BothNarrowShowsHint covers the OR-of-axes case:
// either dimension below threshold should suffice; both below should
// also trip the hint cleanly.
func TestNarrowTerminal_BothNarrowShowsHint(t *testing.T) {
	m, cleanup := narrowTestSetup(t)
	defer cleanup()

	out, _ := m.Update(tea.WindowSizeMsg{Width: 60, Height: 12})
	m = out.(Model)
	got := m.View()
	if !strings.Contains(got, narrowHintMarker) {
		t.Fatalf("both-narrow view missing hint marker %q; got:\n%s",
			narrowHintMarker, got)
	}
}

// TestNarrowTerminal_NormalSizeRendersNormally verifies the negative
// case: a comfortably-sized terminal should NOT short-circuit. We
// assert the hint marker is absent rather than checking for a
// specific list/detail substring so the test stays robust against
// future chrome wording changes.
func TestNarrowTerminal_NormalSizeRendersNormally(t *testing.T) {
	m, cleanup := narrowTestSetup(t)
	defer cleanup()

	out, _ := m.Update(tea.WindowSizeMsg{Width: 120, Height: 30})
	m = out.(Model)
	got := m.View()
	if strings.Contains(got, narrowHintMarker) {
		t.Fatalf("normal-size view unexpectedly contains hint marker; got:\n%s",
			got)
	}
}

// TestNarrowTerminal_QStillRoutesToQuitConfirm proves that the View
// short-circuit doesn't break key routing — q at narrow size still
// opens the quit-confirm modal via routeGlobalKey. This is the
// primary safety invariant: the user must be able to quit even when
// the terminal is too small to render normally.
func TestNarrowTerminal_QStillRoutesToQuitConfirm(t *testing.T) {
	m, cleanup := narrowTestSetup(t)
	defer cleanup()

	out, _ := m.Update(tea.WindowSizeMsg{Width: 60, Height: 12})
	m = out.(Model)
	out, cmd := m.Update(runeKey('q'))
	nm := out.(Model)
	if cmd != nil {
		if msg := cmd(); msg != nil {
			if _, isQuit := msg.(tea.QuitMsg); isQuit {
				t.Fatal("q at narrow size produced tea.Quit; should have opened the confirm modal")
			}
		}
	}
	if nm.modal != modalQuitConfirm {
		t.Fatalf("modal = %v at narrow size, want modalQuitConfirm", nm.modal)
	}
}

// TestNarrowTerminal_CtrlCStillQuits proves ctrl+c remains the
// power-user immediate-quit even when the hint is up. Mirrors
// TestQuit_CtrlCFastQuits but at sub-threshold dimensions.
func TestNarrowTerminal_CtrlCStillQuits(t *testing.T) {
	m, cleanup := narrowTestSetup(t)
	defer cleanup()

	out, _ := m.Update(tea.WindowSizeMsg{Width: 60, Height: 12})
	m = out.(Model)
	_, cmd := m.Update(tea.KeyMsg{Type: tea.KeyCtrlC})
	if cmd == nil {
		t.Fatal("ctrl+c at narrow size produced no cmd; expected tea.Quit")
	}
	if _, isQuit := cmd().(tea.QuitMsg); !isQuit {
		t.Fatalf("ctrl+c cmd at narrow size = %T, want tea.QuitMsg", cmd())
	}
}

// TestNarrowTerminal_ZeroWidthBeforeFirstResize_DoesNotShowHint pins
// the `m.width > 0` gate: before the first WindowSizeMsg arrives,
// initialModel leaves width=0 (and height=0). Without the gate, the
// hint would flash on every cold start. We assert View renders the
// normal body (no hint marker) so the boot path stays clean.
func TestNarrowTerminal_ZeroWidthBeforeFirstResize_DoesNotShowHint(t *testing.T) {
	m, cleanup := narrowTestSetup(t)
	defer cleanup()
	// No WindowSizeMsg; m.width and m.height remain 0.
	got := m.View()
	if strings.Contains(got, narrowHintMarker) {
		t.Fatalf("pre-resize View unexpectedly contains hint marker; got:\n%s",
			got)
	}
}
