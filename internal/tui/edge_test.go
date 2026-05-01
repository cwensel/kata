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

// TestEdge_IdentitySelection_FollowsIssueAcrossReorder: a refetch
// reorders rows (issues come back sorted by updated_at DESC, so any
// background mutation can shuffle them). The cursor must stay on the
// same issue rather than the same index.
func TestEdge_IdentitySelection_FollowsIssueAcrossReorder(t *testing.T) {
	m := initialModel(Options{})
	m.api = &Client{}
	m.scope = scope{projectID: 7}
	m.list.loading = false
	m.list.issues = []Issue{
		{Number: 1, Title: "alpha"},
		{Number: 2, Title: "beta"},
		{Number: 3, Title: "gamma"},
	}
	m.list.cursor = 1
	m.list.selectedNumber = 2 // cursor is on #2 ("beta")

	// Simulate an SSE-driven refetch that reorders: #2 moved to row 0
	// because it was just updated. With positional selection the cursor
	// would still point at index 1 (now "alpha"), silently changing
	// what the user sees as selected.
	out, _ := m.Update(refetchedMsg{
		dispatchKey: m.currentCacheKey(),
		issues: []Issue{
			{Number: 2, Title: "beta"},
			{Number: 1, Title: "alpha"},
			{Number: 3, Title: "gamma"},
		},
	})
	nm := out.(Model)
	if nm.list.cursor != 0 {
		t.Fatalf("cursor = %d, want 0 (#2 moved to row 0)", nm.list.cursor)
	}
	if nm.list.selectedNumber != 2 {
		t.Fatalf("selectedNumber = %d, want 2 (identity preserved)",
			nm.list.selectedNumber)
	}
}

// TestEdge_IdentitySelection_FallsBackWhenIssueDisappears: when the
// previously-selected issue is no longer in the refetched list (e.g.
// soft-deleted, or filter narrowed it out), the cursor falls back to
// the same index clamped to the new visible range and re-records the
// issue under it.
func TestEdge_IdentitySelection_FallsBackWhenIssueDisappears(t *testing.T) {
	m := initialModel(Options{})
	m.api = &Client{}
	m.scope = scope{projectID: 7}
	m.list.loading = false
	m.list.issues = []Issue{
		{Number: 1, Title: "alpha"},
		{Number: 2, Title: "beta"},
		{Number: 3, Title: "gamma"},
	}
	m.list.cursor = 1
	m.list.selectedNumber = 2

	out, _ := m.Update(refetchedMsg{
		dispatchKey: m.currentCacheKey(),
		issues: []Issue{
			{Number: 1, Title: "alpha"},
			// #2 disappeared.
			{Number: 3, Title: "gamma"},
		},
	})
	nm := out.(Model)
	if nm.list.cursor != 1 {
		t.Fatalf("cursor = %d, want 1 (clamped to new index 1)", nm.list.cursor)
	}
	if nm.list.selectedNumber != 3 {
		t.Fatalf("selectedNumber = %d, want 3 (re-pinned to issue at fallback row)",
			nm.list.selectedNumber)
	}
}

// TestEdge_PageUpPageDown_MovesCursorInChunks: pgup/pgdown shift the
// cursor by pageStep rows so navigating long lists doesn't require
// hundreds of j/k presses.
func TestEdge_PageUpPageDown_MovesCursorInChunks(t *testing.T) {
	m := initialModel(Options{})
	issues := make([]Issue, 50)
	for i := range issues {
		issues[i] = Issue{Number: int64(i + 1), Title: "row"}
	}
	m.list.loading = false
	m.list.issues = issues
	m.list.cursor = 5

	// pgdown advances by pageStep (10).
	out, _ := m.Update(tea.KeyMsg{Type: tea.KeyPgDown})
	nm := out.(Model)
	if nm.list.cursor != 15 {
		t.Fatalf("after pgdown, cursor = %d, want 15", nm.list.cursor)
	}
	if nm.list.selectedNumber != 16 {
		t.Fatalf("selectedNumber = %d, want 16 (issue at cursor 15)",
			nm.list.selectedNumber)
	}

	// pgup walks back by pageStep.
	out, _ = nm.Update(tea.KeyMsg{Type: tea.KeyPgUp})
	nm = out.(Model)
	if nm.list.cursor != 5 {
		t.Fatalf("after pgup, cursor = %d, want 5", nm.list.cursor)
	}
}

// TestEdge_PageDown_ClampsAtEnd: pgdown near the end clamps to the
// last row rather than walking past the slice.
func TestEdge_PageDown_ClampsAtEnd(t *testing.T) {
	m := initialModel(Options{})
	issues := make([]Issue, 12)
	for i := range issues {
		issues[i] = Issue{Number: int64(i + 1), Title: "row"}
	}
	m.list.loading = false
	m.list.issues = issues
	m.list.cursor = 8

	out, _ := m.Update(tea.KeyMsg{Type: tea.KeyPgDown})
	nm := out.(Model)
	if nm.list.cursor != 11 {
		t.Fatalf("cursor = %d, want 11 (clamped to last row)", nm.list.cursor)
	}
}

// TestEdge_ListViewport_KeepsCursorVisible: a list of 100 rows with a
// height budget of 10 must render only the cursor's neighborhood, not
// every row. We verify the rendered output contains the cursor row's
// title and excludes rows far from the cursor.
func TestEdge_ListViewport_KeepsCursorVisible(t *testing.T) {
	lm := newListModel()
	lm.loading = false
	issues := make([]Issue, 100)
	for i := range issues {
		issues[i] = Issue{
			Number: int64(i + 1),
			Title:  rowTitleFor(i + 1),
			Status: "open",
		}
	}
	lm.issues = issues
	lm.cursor = 50

	out := lm.View(120, 30, listChrome{}) // height=30 leaves enough room for chrome + ~14 data rows
	if !strings.Contains(out, rowTitleFor(51)) {
		t.Fatalf("cursor row missing from viewport:\n%s", out)
	}
	// A row 30+ away from the cursor must NOT be in the rendered output.
	if strings.Contains(out, rowTitleFor(1)) {
		t.Fatalf("first row leaked into a windowed render of 100 issues:\n%s", out)
	}
	if strings.Contains(out, rowTitleFor(100)) {
		t.Fatalf("last row leaked into windowed render with cursor in middle:\n%s", out)
	}
}

// rowTitleFor produces a unique, identifiable title for row n so the
// viewport test can grep for specific rows in the rendered output.
func rowTitleFor(n int) string {
	return "row-id-" + numToTag(n)
}

// numToTag formats n for use inside a test title without depending on
// fmt.Sprintf (keeps the helper's intent obvious in the test harness).
func numToTag(n int) string {
	if n == 0 {
		return "0"
	}
	digits := []byte{}
	for n > 0 {
		digits = append([]byte{byte('0' + n%10)}, digits...)
		n /= 10
	}
	return string(digits)
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
