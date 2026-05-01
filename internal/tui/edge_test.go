package tui

import (
	"io"
	"strings"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"
)

// TestEdge_WindowResize_NoPanic feeds a wide-then-narrow WindowSizeMsg
// pair through Model.Update and verifies the list still renders without
// panic at the smaller width. This exercises the row-truncation path
// with a 40-cell terminal where the title column flexes to its 20-cell
// floor. The fixture is the same three-row mix used by snapshot tests
// so the comparison stays deterministic.
func TestEdge_WindowResize_NoPanic(t *testing.T) {
	t.Setenv("KATA_COLOR_MODE", "none")
	applyDefaultColorMode(io.Discard)
	prior := renderNow
	renderNow = func() time.Time { return snapshotFixedNow }
	defer func() { renderNow = prior; applyDefaultColorMode(io.Discard) }()

	m := initialModel(Options{})
	m.list.loading = false
	m.list.issues = snapListFixture()

	out, _ := m.Update(tea.WindowSizeMsg{Width: 120, Height: 30})
	m = out.(Model)
	wide := m.View()
	if !strings.Contains(wide, "fix login bug on Safari") {
		t.Fatalf("wide render missing full title:\n%s", wide)
	}

	out, _ = m.Update(tea.WindowSizeMsg{Width: 40, Height: 30})
	m = out.(Model)
	if m.width != 40 || m.height != 30 {
		t.Fatalf("resize not applied: width=%d height=%d", m.width, m.height)
	}
	narrow := m.View()
	// At 40 cols the title column flexes to 20 (floor) and titles get
	// truncated to roughly 20 cells with an ellipsis. We don't pin the
	// exact truncation length (the table inserts column padding) but the
	// row marker for the second row must still be present.
	if !strings.Contains(narrow, "›") {
		t.Fatalf("narrow render missing cursor marker:\n%s", narrow)
	}
	// The full title would not fit at width=40 — the truncate helper
	// either replaces the tail with "…" or leaves the row narrower than
	// the wide render's row. We assert the ellipsis to lock in the
	// truncation behavior — if the renderer ever stops truncating, this
	// is a useful regression catch.
	if !strings.Contains(narrow, "…") {
		t.Fatalf("narrow render did not truncate any title (no ellipsis):\n%s", narrow)
	}
}

// TestEdge_SSEDuringSearchPrompt: a list-view inline prompt is open;
// an SSE eventReceivedMsg arrives mid-typing. The keystroke that comes
// next must reach the buffer (canQuit gates the global keys), and the
// SSE-driven pendingRefetch may have been set in the background but
// must NOT have churned the prompt. After Enter commits the buffer,
// the post-commit refetch is dispatched as expected.
//
// The test drives Model.Update directly rather than through teatest so
// we can assert on cmd shape and lm.search.buffer without timing.
func TestEdge_SSEDuringSearchPrompt(t *testing.T) {
	m := initialModel(Options{})
	m.api = &Client{}
	m.scope = scope{projectID: 7}
	m.list.loading = false
	m.list.issues = []Issue{{ProjectID: 7, Number: 1, Title: "x"}}

	// Open the search prompt with '/'.
	out, _ := m.Update(runeKey('/'))
	m = out.(Model)
	if !m.list.search.inputting {
		t.Fatal("prompt did not open on '/'")
	}

	// Type 'a' before the SSE event arrives.
	out, cmd := m.Update(runeKey('a'))
	m = out.(Model)
	if cmd != nil {
		t.Fatalf("typing 'a' must not return a cmd, got %T", cmd)
	}
	if m.list.search.buffer != "a" {
		t.Fatalf("buffer = %q, want %q", m.list.search.buffer, "a")
	}

	// SSE event lands while the prompt is still open. The handler runs;
	// pendingRefetch flips and a debounce tick is queued. The prompt
	// state is untouched.
	out, sseCmd := m.Update(eventReceivedMsg{projectID: 7, issueNumber: 0})
	m = out.(Model)
	if !m.list.search.inputting {
		t.Fatal("SSE event closed the prompt; should be transparent to it")
	}
	if m.list.search.buffer != "a" {
		t.Fatalf("SSE event mutated buffer: %q, want %q",
			m.list.search.buffer, "a")
	}
	if !m.pendingRefetch {
		t.Fatal("pendingRefetch must be set by SSE event regardless of prompt")
	}
	if sseCmd == nil {
		t.Fatal("SSE event must return a cmd (debounce tick)")
	}

	// Continue typing 'b': keystroke wins and lands in the buffer. No
	// command should fire from this keystroke because the prompt swallows
	// printable runes.
	out, cmd = m.Update(runeKey('b'))
	m = out.(Model)
	if cmd != nil {
		t.Fatalf("typing 'b' after SSE must not return a cmd, got %T", cmd)
	}
	if m.list.search.buffer != "ab" {
		t.Fatalf("buffer = %q, want %q after second keystroke",
			m.list.search.buffer, "ab")
	}

	// Enter commits — refetch fires; prompt closes.
	out, cmd = m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	m = out.(Model)
	if m.list.search.inputting {
		t.Fatal("Enter did not close the prompt")
	}
	if m.list.filter.Search != "ab" {
		t.Fatalf("filter.Search = %q, want %q", m.list.filter.Search, "ab")
	}
	if cmd == nil {
		t.Fatal("Enter must dispatch a refetch cmd")
	}
}

// TestEdge_DetailJumpBack: open issue A → press Enter on a link to
// jump to B → press Esc to go back. The post-Esc detail must restore A
// verbatim (issue, activeTab, tabCursor) so the user is exactly where
// they left off. Regression for the navStack roundtrip.
func TestEdge_DetailJumpBack(t *testing.T) {
	api := &fakeDetailAPI{
		getIssueResult: &Issue{Number: 7, Title: "linked target"},
	}
	km := newKeymap()

	// Build A with one link to issue #7. We seed activeTab=tabLinks and
	// tabCursor=0 so Enter has a jump target on the first row.
	original := detailModel{
		issue:     &Issue{Number: 42, Title: "current", Status: "open"},
		scopePID:  7,
		activeTab: tabLinks,
		tabCursor: 0,
		gen:       1,
		links: []LinkEntry{
			{ID: 1, Type: "blocks", FromNumber: 42, ToNumber: 7, Author: "wesm"},
		},
	}

	// Press Enter on the link → jump.
	jumped, cmd := original.Update(tea.KeyMsg{Type: tea.KeyEnter}, km, api)
	if cmd == nil {
		t.Fatal("expected jump cmd from Enter")
	}
	if len(jumped.navStack) != 1 {
		t.Fatalf("navStack length = %d, want 1", len(jumped.navStack))
	}
	if jumped.issue != nil {
		t.Fatalf("expected post-jump dm to be loading (issue=nil), got %+v",
			jumped.issue)
	}
	if jumped.gen == original.gen {
		t.Fatal("gen must advance on jump")
	}

	// Apply the in-flight detailFetchedMsg so the stacked view has data.
	jumped, _ = jumped.Update(detailFetchedMsg{
		gen: jumped.gen, issue: &Issue{Number: 7, Title: "linked target"},
	}, km, api)
	if jumped.issue == nil || jumped.issue.Number != 7 {
		t.Fatalf("post-fetch dm.issue.Number = %v, want 7", jumped.issue)
	}

	// Press Esc → pop to original.
	popped, popCmd := jumped.Update(tea.KeyMsg{Type: tea.KeyEsc}, km, api)
	if popCmd != nil {
		t.Fatalf("Esc on stacked detail must not emit popDetailMsg, got %T",
			popCmd)
	}
	if popped.issue == nil || popped.issue.Number != 42 {
		t.Fatalf("post-pop issue = %v, want #42 (original)", popped.issue)
	}
	if popped.activeTab != tabLinks {
		t.Fatalf("activeTab not restored: got %d, want tabLinks", popped.activeTab)
	}
	if popped.tabCursor != 0 {
		t.Fatalf("tabCursor not restored: got %d, want 0", popped.tabCursor)
	}
	if len(popped.navStack) != 0 {
		t.Fatalf("navStack should be empty after pop, got %d", len(popped.navStack))
	}
	// The original issue's links slice should also be intact.
	if len(popped.links) != 1 || popped.links[0].ToNumber != 7 {
		t.Fatalf("links not restored: %+v", popped.links)
	}
}
