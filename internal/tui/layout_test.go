package tui

import (
	"io"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
)

// layoutTestSetup pins KATA_COLOR_MODE=none and rebuilds the styles
// against io.Discard so View() output is plain UTF-8 (no ANSI). It
// also returns a fresh Model with no loading flag and the listFixture
// seeded so the layout tests have content to render.
func layoutTestSetup(t *testing.T) (Model, func()) {
	t.Helper()
	t.Setenv("KATA_COLOR_MODE", "none")
	t.Setenv("NO_COLOR", "")
	applyDefaultColorMode(io.Discard)
	m := initialModel(Options{})
	m.list.loading = false
	m.list.issues = snapListFixture()
	cleanup := func() { applyDefaultColorMode(io.Discard) }
	return m, cleanup
}

// TestLayout_PickLayout_Stacked verifies the stacked-fallback branch:
// any width below the breakpoint OR any height below the breakpoint
// must return layoutStacked. The post-Plan-8 thresholds are
// width>=140, height>=36.
func TestLayout_PickLayout_Stacked(t *testing.T) {
	cases := []struct {
		w, h int
	}{
		{100, 40}, // width below threshold
		{160, 30}, // height below threshold
		{139, 36}, // exactly one cell below width
		{140, 35}, // exactly one row below height
	}
	for _, c := range cases {
		if got := pickLayout(c.w, c.h); got != layoutStacked {
			t.Errorf("pickLayout(%d, %d) = %v, want layoutStacked", c.w, c.h, got)
		}
	}
}

// TestLayout_PickLayout_Split verifies the split branch fires when
// BOTH dimensions meet the breakpoint. 140x36 is the minimum split
// terminal; 200x50 is comfortable.
func TestLayout_PickLayout_Split(t *testing.T) {
	cases := []struct {
		w, h int
	}{
		{140, 36}, // exactly at breakpoint
		{160, 40}, // typical wide
		{200, 50}, // very wide
	}
	for _, c := range cases {
		if got := pickLayout(c.w, c.h); got != layoutSplit {
			t.Errorf("pickLayout(%d, %d) = %v, want layoutSplit", c.w, c.h, got)
		}
	}
}

// TestLayout_ResizeSplitToStacked_PreservesSelectionFocusDetail covers
// the split → stacked transition while focusDetail is active. The
// resulting m.view must be viewDetail (the user's focused pane), and
// selectedNumber must survive (identity-based, never touched by the
// layout flip).
func TestLayout_ResizeSplitToStacked_PreservesSelectionFocusDetail(t *testing.T) {
	m, cleanup := layoutTestSetup(t)
	defer cleanup()
	// Boot into split layout.
	out, _ := m.Update(tea.WindowSizeMsg{Width: 160, Height: 40})
	m = out.(Model)
	if m.layout != layoutSplit {
		t.Fatalf("setup failed: layout=%v want split", m.layout)
	}
	// Seed an open detail + focus detail + selectedNumber.
	iss := m.list.issues[1]
	m.detail.issue = &iss
	m.detail.scopePID = 7
	m.focus = focusDetail
	m.list.selectedNumber = 42
	// Resize down across the breakpoint.
	out, _ = m.Update(tea.WindowSizeMsg{Width: 100, Height: 40})
	m = out.(Model)
	if m.layout != layoutStacked {
		t.Errorf("layout=%v after resize, want layoutStacked", m.layout)
	}
	if m.view != viewDetail {
		t.Errorf("view=%v after split→stacked focusDetail flip, want viewDetail", m.view)
	}
	if m.list.selectedNumber != 42 {
		t.Errorf("selectedNumber=%d after flip, want 42", m.list.selectedNumber)
	}
}

// TestLayout_ResizeSplitToStacked_PreservesSelectionFocusList covers
// the split → stacked transition while focusList is active. The
// resulting m.view must be viewList.
func TestLayout_ResizeSplitToStacked_PreservesSelectionFocusList(t *testing.T) {
	m, cleanup := layoutTestSetup(t)
	defer cleanup()
	out, _ := m.Update(tea.WindowSizeMsg{Width: 160, Height: 40})
	m = out.(Model)
	m.focus = focusList
	m.list.selectedNumber = 99
	out, _ = m.Update(tea.WindowSizeMsg{Width: 100, Height: 40})
	m = out.(Model)
	if m.layout != layoutStacked {
		t.Errorf("layout=%v after resize, want layoutStacked", m.layout)
	}
	if m.view != viewList {
		t.Errorf("view=%v after split→stacked focusList flip, want viewList", m.view)
	}
	if m.list.selectedNumber != 99 {
		t.Errorf("selectedNumber=%d after flip, want 99", m.list.selectedNumber)
	}
}

// TestLayout_ResizeStackedToSplit_PreservesFocusFromList: stacked
// viewList, resize up to a split-mode-eligible terminal → focus
// follows view (focusList).
func TestLayout_ResizeStackedToSplit_PreservesFocusFromList(t *testing.T) {
	m, cleanup := layoutTestSetup(t)
	defer cleanup()
	// Start stacked, viewList.
	out, _ := m.Update(tea.WindowSizeMsg{Width: 100, Height: 40})
	m = out.(Model)
	if m.layout != layoutStacked || m.view != viewList {
		t.Fatalf("setup failed: layout=%v view=%v", m.layout, m.view)
	}
	// Resize up across the breakpoint.
	out, _ = m.Update(tea.WindowSizeMsg{Width: 160, Height: 40})
	m = out.(Model)
	if m.layout != layoutSplit {
		t.Errorf("layout=%v after resize, want layoutSplit", m.layout)
	}
	if m.focus != focusList {
		t.Errorf("focus=%v after stacked→split from viewList, want focusList", m.focus)
	}
}

// TestLayout_ResizeStackedToSplit_PreservesFocusFromDetail: stacked
// viewDetail, resize up → focus follows view (focusDetail). Requires
// dm.issue to be set (otherwise focus falls back to focusList).
func TestLayout_ResizeStackedToSplit_PreservesFocusFromDetail(t *testing.T) {
	m, cleanup := layoutTestSetup(t)
	defer cleanup()
	out, _ := m.Update(tea.WindowSizeMsg{Width: 100, Height: 40})
	m = out.(Model)
	iss := m.list.issues[0]
	m.detail.issue = &iss
	m.view = viewDetail
	out, _ = m.Update(tea.WindowSizeMsg{Width: 160, Height: 40})
	m = out.(Model)
	if m.layout != layoutSplit {
		t.Errorf("layout=%v after resize, want layoutSplit", m.layout)
	}
	if m.focus != focusDetail {
		t.Errorf("focus=%v after stacked→split from viewDetail, want focusDetail", m.focus)
	}
}
