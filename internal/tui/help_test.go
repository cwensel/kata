package tui

import (
	"reflect"
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
)

// TestHelpSections_AllBindingsCovered guards against drift between
// keymap.go and helpSections: if a future task adds a key field, the
// test fails until helpSections also references it. The check uses
// reflection to enumerate keymap fields so adding a new binding
// automatically participates.
func TestHelpSections_AllBindingsCovered(t *testing.T) {
	km := newKeymap()
	listed := map[string]bool{}
	for _, s := range helpSections(km) {
		for _, r := range s.rows {
			listed[r.key] = true
		}
	}
	v := reflect.ValueOf(km)
	for i := 0; i < v.NumField(); i++ {
		field := v.Field(i).Interface().(key)
		display := keyDisplay(field)
		if !listed[display] {
			t.Errorf("keymap field %q (binding %s) not in helpSections",
				v.Type().Field(i).Name, display)
		}
	}
}

// TestRenderHelp_NarrowWidth: width 40 picks a 1-column layout. We
// assert each section title appears on its own line so a future
// regression that drops Detail (or any other section) is caught.
func TestRenderHelp_NarrowWidth(t *testing.T) {
	out := renderHelp(newKeymap(), 40, ListFilter{})
	for _, want := range []string{"Global", "List", "Detail"} {
		if !strings.Contains(out, want) {
			t.Errorf("narrow help missing section %q\n%s", want, out)
		}
	}
	if helpColumnCount(40) != 1 {
		t.Fatalf("helpColumnCount(40)=%d, want 1", helpColumnCount(40))
	}
}

// TestRenderHelp_WideWidth: at width 130 the layout uses 3 columns so
// sections lay out side-by-side. We don't assert exact placement (column
// padding varies), but the column count helper is the contract.
func TestRenderHelp_WideWidth(t *testing.T) {
	if helpColumnCount(130) != 3 {
		t.Fatalf("helpColumnCount(130)=%d, want 3", helpColumnCount(130))
	}
	out := renderHelp(newKeymap(), 130, ListFilter{})
	for _, want := range []string{"Global", "List", "Detail", "kata — keybindings"} {
		if !strings.Contains(out, want) {
			t.Errorf("wide help missing %q\n%s", want, out)
		}
	}
}

// TestRenderHelp_FilterChips: an active filter renders as a chip strip
// above the bindings so the user can see why their list looks the way
// it does without leaving the help view.
func TestRenderHelp_FilterChips(t *testing.T) {
	out := renderHelp(newKeymap(), 100, ListFilter{Status: "open"})
	if !strings.Contains(out, "status:open") {
		t.Errorf("expected status chip in help output\n%s", out)
	}
}

// TestHelpToggle_FromList_AndBack: pressing ? in viewList enters
// viewHelp; pressing ? again restores viewList. The Model's prevView is
// the carrier so a future viewDetail-and-back would round-trip the same
// way (see TestHelpToggle_FromDetail).
func TestHelpToggle_FromList_AndBack(t *testing.T) {
	m := initialModel(Options{})
	m.view = viewList
	out, _ := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'?'}})
	mh := out.(Model)
	if mh.view != viewHelp {
		t.Fatalf("after ? from list, view = %v, want viewHelp", mh.view)
	}
	out, _ = mh.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'?'}})
	ml := out.(Model)
	if ml.view != viewList {
		t.Fatalf("after ? from help, view = %v, want viewList", ml.view)
	}
}

// TestHelpToggle_FromDetail: pressing ? in viewDetail enters viewHelp,
// pressing ? again returns to viewDetail (not viewList). Catches a
// regression that would always pop back to the list.
func TestHelpToggle_FromDetail(t *testing.T) {
	m := initialModel(Options{})
	m.view = viewDetail
	out, _ := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'?'}})
	mh := out.(Model)
	if mh.view != viewHelp {
		t.Fatalf("after ? from detail, view = %v, want viewHelp", mh.view)
	}
	out, _ = mh.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'?'}})
	md := out.(Model)
	if md.view != viewDetail {
		t.Fatalf("after ? from help, view = %v, want viewDetail", md.view)
	}
}

// TestHelpToggle_QuitFromHelp: q from viewHelp still quits. The plan
// keeps q wired to global Quit even inside the overlay so the user can
// always escape regardless of which view is active.
func TestHelpToggle_QuitFromHelp(t *testing.T) {
	m := initialModel(Options{})
	m.view = viewHelp
	_, cmd := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'q'}})
	if cmd == nil {
		t.Fatal("q from help returned nil cmd, want tea.Quit")
	}
	if msg := cmd(); msg == nil {
		t.Fatal("q from help cmd produced nil msg, want tea.QuitMsg")
	} else if _, ok := msg.(tea.QuitMsg); !ok {
		t.Fatalf("q from help produced %T, want tea.QuitMsg", msg)
	}
}
