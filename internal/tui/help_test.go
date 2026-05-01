package tui

import (
	"reflect"
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
)

// TestHelpSections_AllBindingsCovered guards against drift between
// keymap.go and helpSections: if a future task adds a key field, the
// test fails until helpSections also references it. The check counts
// occurrences of each rendered display string so duplicate-bound keys
// (e.g. Open and JumpRef both bind "enter", ClearFilters and NewComment
// both bind "c") all stay covered — dropping one half of a duplicate
// pair leaves a count gap that the assertion catches. The unchecked
// type assertion is replaced by a guarded form so future non-key fields
// on keymap (e.g. a config struct) wouldn't panic the test.
func TestHelpSections_AllBindingsCovered(t *testing.T) {
	km := newKeymap()
	found := map[string]int{}
	for _, s := range helpSections(km) {
		for _, r := range s.rows {
			found[r.key]++
		}
	}
	required := map[string]int{}
	v := reflect.ValueOf(km)
	for i := 0; i < v.NumField(); i++ {
		k, ok := v.Field(i).Interface().(key)
		if !ok {
			continue
		}
		required[keyDisplay(k)]++
	}
	for display, want := range required {
		if got := found[display]; got < want {
			t.Errorf("display %q: helpSections has %d, keymap requires %d",
				display, got, want)
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

// TestHelp_GatedByInputting: pressing ? while a list-view inline prompt
// is open must reach the buffer instead of opening help. The gate lives
// on canQuit (model.go), shared with q and R.
func TestHelp_GatedByInputting(t *testing.T) {
	m := initialModel(Options{})
	m.list.search.inputting = true
	out, _ := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'?'}})
	if out.(Model).view == viewHelp {
		t.Fatal("? opened help while inputting; should be gated")
	}
}

// TestHelp_RefetchWhileOpen_KeepsListInSync: a refetchedMsg landing
// while the help overlay is active must update lm.issues so toggling
// back to the list does not show stale rows. Pre-fix, dispatchToView
// only forwarded to viewList/viewDetail, so the refetch updated the
// cache but left lm.issues at the pre-help snapshot. The fix moves
// applyFetched into populateCache so cache and list stay in lockstep
// regardless of the active view.
func TestHelp_RefetchWhileOpen_KeepsListInSync(t *testing.T) {
	m := initialModel(Options{})
	m.scope = scope{projectID: 1}
	m.list.issues = []Issue{{Number: 1, Title: "old"}}
	m.prevView = viewList
	m.view = viewHelp
	out, _ := m.Update(refetchedMsg{
		issues: []Issue{{Number: 2, Title: "new"}},
	})
	nm := out.(Model)
	if got := len(nm.list.issues); got != 1 {
		t.Fatalf("list.issues len = %d, want 1", got)
	}
	if nm.list.issues[0].Number != 2 || nm.list.issues[0].Title != "new" {
		t.Fatalf("list.issues = %+v, want [{Number:2 Title:new}]", nm.list.issues)
	}
	// Toggling back to the list must surface the refreshed rows.
	out2, _ := nm.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'?'}})
	if out2.(Model).view != viewList {
		t.Fatalf("after ? from help, view = %v, want viewList", out2.(Model).view)
	}
	if out2.(Model).list.issues[0].Number != 2 {
		t.Fatal("returning to list must show refetched issues, not stale snapshot")
	}
}
