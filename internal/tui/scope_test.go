package tui

import (
	"strings"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"
)

// scopeFixtureSingle returns a Model in single-project scope with a
// home project bound. The cache holds a mock entry so the toggle's
// drop() has something to clear; tests assert it became empty.
func scopeFixtureSingle() Model {
	now := time.Date(2026, 5, 1, 12, 0, 0, 0, time.UTC)
	m := Model{
		view:     viewList,
		keymap:   newKeymap(),
		cache:    newIssueCache(),
		toastNow: func() time.Time { return now },
		scope: scope{
			projectID:       7,
			projectName:     "kata",
			homeProjectID:   7,
			homeProjectName: "kata",
		},
		list: listModel{actor: "wesm"},
	}
	m.cache.put(cacheKey{projectID: 7}, []Issue{{Number: 1}})
	return m
}

// TestScopeToggle_GatedNoOp: pressing R is a toast-only no-op while
// the cross-project surface is gated. Scope must be unchanged, the
// list cache untouched, and the toast text must explain the gate so
// the user isn't confused by the silent no-op.
func TestScopeToggle_GatedNoOp(t *testing.T) {
	m := scopeFixtureSingle()
	next, cmd := m.handleScopeToggle()
	if next.scope.allProjects {
		t.Fatal("scope must not flip while all-projects is gated")
	}
	if next.scope.projectID != 7 {
		t.Fatalf("projectID changed: got %d, want 7", next.scope.projectID)
	}
	if !next.cache.set {
		t.Fatal("cache must NOT be dropped when toggle is a no-op")
	}
	if next.toast == nil {
		t.Fatal("expected gate-explanation toast")
	}
	if !strings.Contains(next.toast.text, "all-projects not available") {
		t.Fatalf("toast text = %q, want hint about gated all-projects",
			next.toast.text)
	}
	if cmd == nil {
		t.Fatal("expected toast-expiry cmd")
	}
}

// TestScopeToggle_RKeyDispatch_Gated: R at the top level still routes
// through handleScopeToggle. The binding is wired; the toggle just
// produces a toast instead of changing scope.
func TestScopeToggle_RKeyDispatch_Gated(t *testing.T) {
	m := scopeFixtureSingle()
	out, _ := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'R'}})
	nm := out.(Model)
	if nm.scope.allProjects {
		t.Fatal("R must not flip scope while all-projects is gated")
	}
	if nm.toast == nil {
		t.Fatal("R must surface the gate-explanation toast")
	}
}

// TestScopeToggle_GatedByInputting: pressing R while the M3a inline
// command bar is open must reach the bar's textinput buffer instead
// of toggling scope. canQuit gates global keys via m.input.kind.
func TestScopeToggle_GatedByInputting(t *testing.T) {
	m := scopeFixtureSingle()
	m.input = newSearchBar(ListFilter{})
	out, _ := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'R'}})
	nm := out.(Model)
	if nm.scope.allProjects {
		t.Fatal("R toggled scope while bar was active; should be gated")
	}
	if v := nm.input.activeField().value(); v != "R" {
		t.Fatalf("bar buffer = %q, want %q (rune must reach prompt)", v, "R")
	}
}

// TestEmptyState_RendersHint: viewEmpty renders the onboarding hint
// containing both the "no kata projects" line and the kata init hint.
func TestEmptyState_RendersHint(t *testing.T) {
	m := initialModel(Options{})
	m.view = viewEmpty
	m.width = 80
	m.height = 24
	out := m.View()
	for _, want := range []string{
		"no kata projects registered yet",
		"kata init",
		"press q to quit",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("empty view missing %q in output\n%s", want, out)
		}
	}
}

// TestEmptyState_QuitsOnQ: q from viewEmpty opens the M3.5b
// quit-confirm modal. Y from there commits to quit. ctrl+c remains
// the immediate-quit escape hatch so the user is never trapped.
func TestEmptyState_QuitsOnQ(t *testing.T) {
	m := initialModel(Options{})
	m.view = viewEmpty
	out, _ := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'q'}})
	if out.(Model).modal != modalQuitConfirm {
		t.Fatalf("q from viewEmpty did not open quit-confirm: %v", out.(Model).modal)
	}
	out, cmd := out.(Model).Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'y'}})
	_ = out
	if cmd == nil {
		t.Fatal("y in modal produced nil cmd, want tea.Quit")
	}
	if _, ok := cmd().(tea.QuitMsg); !ok {
		t.Fatalf("y in modal cmd = %T, want tea.QuitMsg", cmd())
	}
}

// TestEmptyState_OtherKeysIgnored: j, ?, R in viewEmpty are no-ops so an
// unbound user can't accidentally fall into a partially-functional help
// or list view from a state with no projects.
func TestEmptyState_OtherKeysIgnored(t *testing.T) {
	m := initialModel(Options{})
	m.view = viewEmpty
	for _, k := range []rune{'j', '?', 'R', 'k', 's'} {
		out, cmd := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{k}})
		if out.(Model).view != viewEmpty {
			t.Fatalf("key %q changed view from empty to %v", k, out.(Model).view)
		}
		if cmd != nil {
			t.Fatalf("key %q in viewEmpty returned non-nil cmd", k)
		}
	}
}

// TestRenderEmpty_ZeroDims: a zero-sized terminal renders the message
// without panicking inside lipgloss.Place. Defensive: the model emits
// width/height from WindowSizeMsg, which can lag on first frame.
func TestRenderEmpty_ZeroDims(t *testing.T) {
	out := renderEmpty(0, 0)
	if !strings.Contains(out, "no kata projects registered yet") {
		t.Fatalf("zero-dim render missing hint:\n%s", out)
	}
}
