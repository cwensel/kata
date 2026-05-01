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

// TestEdge_StaleRefetch_DroppedAfterFilterChange: a refetch dispatched
// under filter X arrives after the user has already moved to filter Y
// (e.g. typed a search term). The stale response must NOT clobber the
// list — populateCache compares the response's dispatchKey against the
// current state and drops mismatches. Without this guard, the stale
// reply's issues would overwrite the post-filter snapshot.
func TestEdge_StaleRefetch_DroppedAfterFilterChange(t *testing.T) {
	m := initialModel(Options{})
	m.api = &Client{}
	m.scope = scope{projectID: 7}
	m.list.loading = false
	m.list.issues = []Issue{{Number: 99, Title: "current-filter row"}}
	// Current filter is Status="open"; the stale reply was dispatched
	// when filter was empty.
	m.list.filter = ListFilter{Status: "open"}

	stale := refetchedMsg{
		dispatchKey: cacheKey{projectID: 7, filter: ListFilter{}},
		issues:      []Issue{{Number: 1, Title: "old-filter row"}},
	}
	out, _ := m.Update(stale)
	nm := out.(Model)
	if len(nm.list.issues) != 1 || nm.list.issues[0].Number != 99 {
		t.Fatalf("stale refetch overwrote current filter view: %+v", nm.list.issues)
	}
}

// TestEdge_StaleRefetch_DroppedAcrossScopeToggle: a refetch dispatched
// under single-project scope arrives after the user toggled to all-
// projects. dispatchKey carries the original scope; populateCache
// drops the response so the new scope's list isn't polluted by single-
// project rows.
func TestEdge_StaleRefetch_DroppedAcrossScopeToggle(t *testing.T) {
	m := initialModel(Options{})
	m.api = &Client{}
	// Currently in all-projects scope.
	m.scope = scope{allProjects: true}
	m.list.loading = false
	m.list.issues = []Issue{{Number: 99, Title: "all-projects row"}}

	stale := refetchedMsg{
		dispatchKey: cacheKey{projectID: 7}, // single-project at dispatch
		issues:      []Issue{{Number: 1, Title: "single-project row"}},
	}
	out, _ := m.Update(stale)
	nm := out.(Model)
	if len(nm.list.issues) != 1 || nm.list.issues[0].Number != 99 {
		t.Fatalf("stale single-project refetch leaked into all-projects view: %+v",
			nm.list.issues)
	}
}

// TestEdge_ListMutation_CompletesAfterDetailOpen: the user closes an
// issue from the list view (mutationDoneMsg origin="list" is in
// flight), then opens a different issue in detail view before the
// mutation completes. When the mutation result lands, it must still
// reach listModel.applyMutation (so the list status line updates and
// the post-success refetch fires) — without top-level routing,
// dispatchToView would forward to detail and the result would be
// silently dropped.
func TestEdge_ListMutation_CompletesAfterDetailOpen(t *testing.T) {
	m := initialModel(Options{})
	m.api = &Client{}
	m.scope = scope{projectID: 7}
	m.list.loading = false
	m.list.actor = "tester"
	m.list.issues = []Issue{{ProjectID: 7, Number: 1, Title: "x"}}
	// Simulate having opened detail view after dispatching the close.
	m.view = viewDetail
	m.detail.issue = &Issue{ProjectID: 7, Number: 99, Title: "other"}

	mut := mutationDoneMsg{origin: "list", kind: "close",
		resp: &MutationResp{Issue: &Issue{Number: 1}}}
	out, _ := m.Update(mut)
	nm := out.(Model)
	if nm.list.status == "" {
		t.Fatal("list mutation completion was dropped while detail was active")
	}
	if !strings.Contains(nm.list.status, "closed #1") {
		t.Fatalf("list.status = %q, want hint about closed #1", nm.list.status)
	}
}

// TestEdge_DetailMutation_CompletesAfterPopToList: the user closes an
// issue from detail view (mutationDoneMsg origin="detail" in flight),
// then pops back to the list before the mutation completes. The
// result must still reach detailModel.applyMutation so dm.status
// reflects the close and the post-success refetch is dispatched.
// Without top-level routing, dispatchToView would forward to the list
// and the response would be silently dropped.
func TestEdge_DetailMutation_CompletesAfterPopToList(t *testing.T) {
	m := initialModel(Options{})
	m.api = &Client{}
	m.scope = scope{projectID: 7}
	m.list.loading = false
	// Detail is initialized with a current issue and gen=5 from a recent
	// open; after popping, m.view is viewList but m.detail still holds
	// the prior state until the next open.
	m.detail.issue = &Issue{ProjectID: 7, Number: 42, Title: "to close"}
	m.detail.scopePID = 7
	m.detail.gen = 5
	m.view = viewList

	mut := mutationDoneMsg{origin: "detail", gen: 5, kind: "close",
		resp: &MutationResp{Issue: &Issue{Number: 42}}}
	out, _ := m.Update(mut)
	nm := out.(Model)
	if nm.detail.status == "" {
		t.Fatal("detail mutation completion was dropped while list was active")
	}
	if !strings.Contains(nm.detail.status, "closed #42") {
		t.Fatalf("detail.status = %q, want hint about closed #42", nm.detail.status)
	}
}

// TestEdge_DetailJumpBack: open issue A → press Enter on a link to
// jump to B → press Esc to go back. The post-Esc detail must restore A
// verbatim (issue, activeTab, tabCursor) so the user is exactly where
// they left off. Regression for the navStack roundtrip.
//
// Driven through Model.Update so the jumpDetailMsg flow exercises
// Model.handleJumpDetail end-to-end (gen comes from m.nextGen).
func TestEdge_DetailJumpBack(t *testing.T) {
	m := initialModel(Options{})
	m.api = &Client{}
	m.scope = scope{projectID: 7}
	m.view = viewDetail

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
	m.detail = original
	m.nextGen = 1

	// Press Enter on the link → emits jumpDetailMsg(7).
	out, cmd := m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	m = out.(Model)
	if cmd == nil {
		t.Fatal("expected jump cmd from Enter")
	}
	jm, ok := cmd().(jumpDetailMsg)
	if !ok || jm.number != 7 {
		t.Fatalf("expected jumpDetailMsg(7), got %T (%v)", cmd(), cmd())
	}

	// Feed the jumpDetailMsg back so Model.handleJumpDetail performs
	// the navStack push and gen advance.
	out, _ = m.Update(jm)
	m = out.(Model)
	if len(m.detail.navStack) != 1 {
		t.Fatalf("navStack length = %d, want 1", len(m.detail.navStack))
	}
	if m.detail.issue != nil {
		t.Fatalf("expected post-jump dm to be loading (issue=nil), got %+v",
			m.detail.issue)
	}
	if m.detail.gen == original.gen {
		t.Fatal("gen must advance on jump")
	}

	// Apply the in-flight detailFetchedMsg so the stacked view has data.
	out, _ = m.Update(detailFetchedMsg{
		gen: m.detail.gen, issue: &Issue{Number: 7, Title: "linked target"},
	})
	m = out.(Model)
	if m.detail.issue == nil || m.detail.issue.Number != 7 {
		t.Fatalf("post-fetch dm.issue.Number = %v, want 7", m.detail.issue)
	}

	// Press Esc → pop to original (handleBack restores from navStack).
	out, popCmd := m.Update(tea.KeyMsg{Type: tea.KeyEsc})
	m = out.(Model)
	if popCmd != nil {
		t.Fatalf("Esc on stacked detail must not emit a cmd, got %T", popCmd)
	}
	if m.detail.issue == nil || m.detail.issue.Number != 42 {
		t.Fatalf("post-pop issue = %v, want #42 (original)", m.detail.issue)
	}
	if m.detail.activeTab != tabLinks {
		t.Fatalf("activeTab not restored: got %d, want tabLinks", m.detail.activeTab)
	}
	if m.detail.tabCursor != 0 {
		t.Fatalf("tabCursor not restored: got %d, want 0", m.detail.tabCursor)
	}
	if len(m.detail.navStack) != 0 {
		t.Fatalf("navStack should be empty after pop, got %d", len(m.detail.navStack))
	}
	// The original issue's links slice should also be intact.
	if len(m.detail.links) != 1 || m.detail.links[0].ToNumber != 7 {
		t.Fatalf("links not restored: %+v", m.detail.links)
	}
}
