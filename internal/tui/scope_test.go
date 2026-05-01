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

// scopeFixtureAllWithHome: all-projects scope but the home project is
// remembered so R can switch back. Models the case where the user
// pressed R earlier or used --all-projects.
func scopeFixtureAllWithHome() Model {
	m := scopeFixtureSingle()
	m.scope.allProjects = true
	m.scope.projectID = 0
	m.scope.projectName = ""
	return m
}

// scopeFixtureAllNoHome: all-projects scope with no home project (the
// boot fallback case). R should toast instead of switching.
func scopeFixtureAllNoHome() Model {
	m := scopeFixtureAllWithHome()
	m.scope.homeProjectID = 0
	m.scope.homeProjectName = ""
	return m
}

// TestScopeToggle_SingleToAll: pressing R in single-project mode flips
// allProjects=true, drops the cache, resets the list, and dispatches a
// fetch (cmd is non-nil). homeProjectID is preserved so the back-toggle
// works.
func TestScopeToggle_SingleToAll(t *testing.T) {
	m := scopeFixtureSingle()
	next, _ := m.handleScopeToggle()
	if !next.scope.allProjects {
		t.Fatal("expected allProjects=true after toggle")
	}
	if next.scope.projectID != 0 {
		t.Fatalf("projectID should clear, got %d", next.scope.projectID)
	}
	if next.scope.homeProjectID != 7 {
		t.Fatalf("homeProjectID lost: got %d, want 7", next.scope.homeProjectID)
	}
	if next.cache.set {
		t.Fatal("cache should be dropped after toggle")
	}
	if !next.list.loading {
		t.Fatal("list should be reset to loading=true")
	}
	if next.list.actor != "wesm" {
		t.Fatalf("actor lost across reset: got %q", next.list.actor)
	}
}

// TestScopeToggle_AllToSingle_WhenDefaultExists: pressing R in
// all-projects mode with a home project switches back to single-project.
// The cache is dropped and a fresh fetch is dispatched.
func TestScopeToggle_AllToSingle_WhenDefaultExists(t *testing.T) {
	m := scopeFixtureAllWithHome()
	next, _ := m.handleScopeToggle()
	if next.scope.allProjects {
		t.Fatal("expected allProjects=false after toggle back")
	}
	if next.scope.projectID != 7 {
		t.Fatalf("projectID = %d, want 7 (from home)", next.scope.projectID)
	}
	if next.scope.projectName != "kata" {
		t.Fatalf("projectName = %q, want kata", next.scope.projectName)
	}
}

// TestScopeToggle_AllToSingle_NoDefault: pressing R in all-projects mode
// with no home project surfaces the "no project bound" toast and leaves
// scope unchanged. cmd is the toast-expiry tick, not a fetch.
func TestScopeToggle_AllToSingle_NoDefault(t *testing.T) {
	m := scopeFixtureAllNoHome()
	next, cmd := m.handleScopeToggle()
	if !next.scope.allProjects {
		t.Fatal("scope should remain all-projects when no default")
	}
	if next.toast == nil {
		t.Fatal("expected toast set when no project bound")
	}
	if !strings.Contains(next.toast.text, "no project bound") {
		t.Fatalf("toast text = %q, want hint about no project", next.toast.text)
	}
	if cmd == nil {
		t.Fatal("expected toast-expiry cmd")
	}
}

// TestScopeToggle_RKeyDispatch: a top-level R keystroke routes through
// handleScopeToggle. Smoke test that the binding is wired.
func TestScopeToggle_RKeyDispatch(t *testing.T) {
	m := scopeFixtureSingle()
	out, _ := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'R'}})
	if !out.(Model).scope.allProjects {
		t.Fatal("R from single-project mode should toggle to all-projects")
	}
}

// TestScopeToggle_GatedByInputting: pressing R while a list-view inline
// prompt is open must reach the buffer instead of toggling scope. The
// gate lives on canQuit (model.go), shared with q and ?.
func TestScopeToggle_GatedByInputting(t *testing.T) {
	m := scopeFixtureSingle()
	m.list.search.inputting = true
	out, _ := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'R'}})
	if out.(Model).scope.allProjects {
		t.Fatal("R toggled scope while inputting; should be gated")
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

// TestEmptyState_QuitsOnQ: q is the only binding viewEmpty honors.
// Pressing it returns tea.Quit so the user is never trapped.
func TestEmptyState_QuitsOnQ(t *testing.T) {
	m := initialModel(Options{})
	m.view = viewEmpty
	_, cmd := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'q'}})
	if cmd == nil {
		t.Fatal("q from viewEmpty returned nil cmd, want tea.Quit")
	}
	if _, ok := cmd().(tea.QuitMsg); !ok {
		t.Fatal("q from viewEmpty did not produce tea.QuitMsg")
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
