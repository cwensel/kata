package tui

import (
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
)

// listFilterEqual compares two ListFilter values by their commit-side
// fields (Status/Owner/Search). The Labels axis is deferred to commit
// 5b; until then the filter modal does not write to it. Direct
// equality fails because ListFilter contains a []string slice.
func listFilterEqual(a, b ListFilter) bool {
	return a.Status == b.Status && a.Owner == b.Owner &&
		a.Author == b.Author && a.Search == b.Search
}

// filterFormFixture returns a Model already at the list view with a
// resolved actor and a single-project scope so the `f` keybinding
// opens the centered filter modal. Mirrors newIssueFormFixture.
func filterFormFixture() Model {
	return Model{
		view:   viewList,
		keymap: newKeymap(),
		scope:  scope{projectID: 7, projectName: "kata"},
		list:   listModel{actor: "tester"},
		cache:  newIssueCache(),
	}
}

// openFilterForm presses `f`, drives the resulting openInputCmd, and
// returns the model with m.input.kind == inputFilterForm. Mirrors
// openNewIssueForm.
func openFilterForm(t *testing.T, m Model) Model {
	t.Helper()
	out, cmd := m.Update(runeKey('f'))
	m = out.(Model)
	if cmd == nil {
		t.Fatalf("press f produced no cmd; expected openInputCmd")
	}
	msg := cmd()
	out, _ = m.Update(msg)
	m = out.(Model)
	if m.input.kind != inputFilterForm {
		t.Fatalf("openInput did not land inputFilterForm; got %v", m.input.kind)
	}
	return m
}

// TestFilterForm_OpensOnFKey: pressing f on the list view opens the
// centered three-axis filter modal. Field labels are Status / Owner /
// Search in order.
func TestFilterForm_OpensOnFKey(t *testing.T) {
	m := openFilterForm(t, filterFormFixture())
	if len(m.input.fields) != 3 {
		t.Fatalf("form fields = %d, want 3 (Status/Owner/Search)", len(m.input.fields))
	}
	wantLabels := []string{"Status", "Owner", "Search"}
	for i, f := range m.input.fields {
		if f.label != wantLabels[i] {
			t.Fatalf("field[%d].label = %q, want %q", i, f.label, wantLabels[i])
		}
	}
	if m.input.fields[0].kind != fieldRadio {
		t.Fatalf("field[0].kind = %v, want fieldRadio", m.input.fields[0].kind)
	}
}

// TestFilterForm_AllProjectsScopeStillRenders: the filter modal works
// in cross-project mode too — it's filter-only, no project-scoped
// mutation. Unlike the new-issue form, no all-projects gate fires.
func TestFilterForm_AllProjectsScopeStillRenders(t *testing.T) {
	m := filterFormFixture()
	m.scope = scope{allProjects: true}
	out, cmd := m.Update(runeKey('f'))
	if cmd == nil {
		t.Fatal("press f in all-projects mode must dispatch openInputCmd")
	}
	msg := cmd()
	out, _ = out.(Model).Update(msg)
	nm := out.(Model)
	if nm.input.kind != inputFilterForm {
		t.Fatalf("filter form did not open in all-projects mode: kind=%v", nm.input.kind)
	}
}

// TestFilterForm_TabCyclesThreeFields_WithWrap: tab cycles 0→1→2→0.
func TestFilterForm_TabCyclesThreeFields_WithWrap(t *testing.T) {
	m := openFilterForm(t, filterFormFixture())
	wants := []int{1, 2, 0}
	for i, want := range wants {
		out, _ := m.Update(tea.KeyMsg{Type: tea.KeyTab})
		m = out.(Model)
		if m.input.active != want {
			t.Fatalf("step %d: active = %d, want %d", i, m.input.active, want)
		}
	}
}

// TestFilterForm_StatusFieldRadioCycle_LeftRightSpace: with Status
// active (the default), → cycles forward, ← backward, space cycles
// forward. Choices are all/open/closed.
func TestFilterForm_StatusFieldRadioCycle_LeftRightSpace(t *testing.T) {
	m := openFilterForm(t, filterFormFixture())
	if got := m.input.fields[0].radio.value(); got != "all" {
		t.Fatalf("initial radio = %q, want all", got)
	}
	// → all → open
	out, _ := m.Update(tea.KeyMsg{Type: tea.KeyRight})
	m = out.(Model)
	if got := m.input.fields[0].radio.value(); got != "open" {
		t.Fatalf("after right: %q, want open", got)
	}
	// space open → closed
	out, _ = m.Update(tea.KeyMsg{Type: tea.KeySpace})
	m = out.(Model)
	if got := m.input.fields[0].radio.value(); got != "closed" {
		t.Fatalf("after space: %q, want closed", got)
	}
	// ← closed → open
	out, _ = m.Update(tea.KeyMsg{Type: tea.KeyLeft})
	m = out.(Model)
	if got := m.input.fields[0].radio.value(); got != "open" {
		t.Fatalf("after left: %q, want open", got)
	}
}

// TestFilterForm_CommitUsesDedicatedPath (load-bearing): commit goes
// through commitFilterForm, NOT applyLiveBarFilter or commitFormInput.
// Sets Status=open, Owner=alice, Search=login on the form, calls
// commitInput, asserts the full ListFilter lands in lm.filter and a
// refetch cmd is dispatched.
//
// applyLiveBarFilter would only set ONE field (the active bar); the
// dedicated path sets all three atomically.
func TestFilterForm_CommitUsesDedicatedPath(t *testing.T) {
	m := openFilterForm(t, filterFormFixture())
	// Set Status=open via a right-arrow cycle.
	out, _ := m.Update(tea.KeyMsg{Type: tea.KeyRight})
	m = out.(Model)
	// Tab to Owner; type alice.
	out, _ = m.Update(tea.KeyMsg{Type: tea.KeyTab})
	m = out.(Model)
	for _, r := range "alice" {
		m, _ = stepModel(m, runeKey(r))
	}
	// Tab to Search; type login.
	out, _ = m.Update(tea.KeyMsg{Type: tea.KeyTab})
	m = out.(Model)
	for _, r := range "login" {
		m, _ = stepModel(m, runeKey(r))
	}
	// ctrl+s commits.
	out, cmd := m.Update(tea.KeyMsg{Type: tea.KeyCtrlS})
	nm := out.(Model)
	if cmd == nil {
		t.Fatal("commit produced no cmd; expected refetch")
	}
	want := ListFilter{Status: "open", Owner: "alice", Search: "login"}
	if !listFilterEqual(nm.list.filter, want) {
		t.Fatalf("list.filter = %+v, want %+v", nm.list.filter, want)
	}
	if nm.input.kind != inputNone {
		t.Fatalf("form did not close on commit: kind=%v", nm.input.kind)
	}
}

// TestFilterForm_CommitZeroesSelectedNumberAndResetsCursor: a filter
// commit zeros selectedNumber and resets cursor to 0 — matches the
// s/c convention. Predictable fresh-view behavior beats trying to
// pin selection across a filter change.
func TestFilterForm_CommitZeroesSelectedNumberAndResetsCursor(t *testing.T) {
	m := openFilterForm(t, filterFormFixture())
	m.list.cursor = 5
	m.list.selectedNumber = 42
	out, _ := m.Update(tea.KeyMsg{Type: tea.KeyCtrlS})
	nm := out.(Model)
	if nm.list.cursor != 0 {
		t.Fatalf("cursor = %d, want 0 after commit", nm.list.cursor)
	}
	if nm.list.selectedNumber != 0 {
		t.Fatalf("selectedNumber = %d, want 0 after commit", nm.list.selectedNumber)
	}
}

// TestFilterForm_CommitClearsLmStatus: any prior list-status hint is
// cleared on commit so the new filtered view doesn't read with a
// stale "closed #42" or similar.
func TestFilterForm_CommitClearsLmStatus(t *testing.T) {
	m := openFilterForm(t, filterFormFixture())
	m.list.status = "closed #99"
	out, _ := m.Update(tea.KeyMsg{Type: tea.KeyCtrlS})
	nm := out.(Model)
	if nm.list.status != "" {
		t.Fatalf("list.status = %q, want empty after commit", nm.list.status)
	}
}

// TestFilterForm_CommitDispatchesRefetch: commit always returns a
// non-nil cmd that fetches the list under the new filter. Status is
// daemon-side; Owner/Search are client-side but the refetch is
// uniform so the cache stays warm.
func TestFilterForm_CommitDispatchesRefetch(t *testing.T) {
	m := openFilterForm(t, filterFormFixture())
	out, cmd := m.Update(tea.KeyMsg{Type: tea.KeyCtrlS})
	if cmd == nil {
		t.Fatal("expected non-nil refetch cmd")
	}
	_ = out
}

// TestFilterForm_CommitResetsCursorToZero is a more explicit form of
// the cursor=0 invariant — separate test pins the contract per the
// per-step assertion list (5a.17).
func TestFilterForm_CommitResetsCursorToZero(t *testing.T) {
	m := openFilterForm(t, filterFormFixture())
	m.list.cursor = 17
	out, _ := m.Update(tea.KeyMsg{Type: tea.KeyCtrlS})
	nm := out.(Model)
	if nm.list.cursor != 0 {
		t.Fatalf("cursor not reset: got %d, want 0", nm.list.cursor)
	}
}

// TestFilterForm_CtrlRResetsFieldsOnly_PreFilterIntact: ctrl+r zeros
// every field on the form but leaves preFilter intact so a subsequent
// esc still restores the at-open snapshot.
func TestFilterForm_CtrlRResetsFieldsOnly_PreFilterIntact(t *testing.T) {
	m := filterFormFixture()
	m.list.filter = ListFilter{Status: "open", Owner: "wesm", Search: "bug"}
	m = openFilterForm(t, m)
	// preFilter snapshot should match.
	wantPre := ListFilter{Status: "open", Owner: "wesm", Search: "bug"}
	if !listFilterEqual(m.input.preFilter, wantPre) {
		t.Fatalf("preFilter = %+v, want %+v", m.input.preFilter, wantPre)
	}
	// ctrl+r resets.
	out, _ := m.Update(tea.KeyMsg{Type: tea.KeyCtrlR})
	m = out.(Model)
	if got := m.input.fields[0].radio.value(); got != "all" {
		t.Fatalf("Status not reset: %q, want all", got)
	}
	if got := m.input.fields[1].input.Value(); got != "" {
		t.Fatalf("Owner not reset: %q, want empty", got)
	}
	if got := m.input.fields[2].input.Value(); got != "" {
		t.Fatalf("Search not reset: %q, want empty", got)
	}
	if !listFilterEqual(m.input.preFilter, wantPre) {
		t.Fatalf("preFilter mutated by ctrl+r: %+v, want %+v",
			m.input.preFilter, wantPre)
	}
}

// TestFilterForm_EscRestoresPreFilter: esc closes the form AND
// restores lm.filter to the preFilter snapshot (in case a future
// "live preview" path mutated it; today the commit is the only mutator
// but the symmetry is locked down for safety).
func TestFilterForm_EscRestoresPreFilter(t *testing.T) {
	m := filterFormFixture()
	m.list.filter = ListFilter{Status: "open", Owner: "wesm"}
	m = openFilterForm(t, m)
	// Make a change inside the form (just type into Owner via tab+keys).
	out, _ := m.Update(tea.KeyMsg{Type: tea.KeyTab})
	m = out.(Model)
	for _, r := range "alice" {
		m, _ = stepModel(m, runeKey(r))
	}
	// Esc cancels.
	out, _ = m.Update(tea.KeyMsg{Type: tea.KeyEsc})
	nm := out.(Model)
	if nm.input.kind != inputNone {
		t.Fatalf("form not closed after esc: kind=%v", nm.input.kind)
	}
	want := ListFilter{Status: "open", Owner: "wesm"}
	if !listFilterEqual(nm.list.filter, want) {
		t.Fatalf("filter not restored: got %+v, want %+v", nm.list.filter, want)
	}
}

// TestFilterForm_CtrlSCommitsViaCommitInputBranch_NotCommitFormInput
// (load-bearing): ctrl+s on the filter modal MUST go through the
// dedicated commitFilterForm path, not commitFormInput. The latter
// would set saving=true and wait for a mutationDoneMsg that never
// arrives. The assertion is direct: after ctrl+s, the form is closed
// (kind=inputNone) and saving is NOT true.
func TestFilterForm_CtrlSCommitsViaCommitInputBranch_NotCommitFormInput(
	t *testing.T,
) {
	m := openFilterForm(t, filterFormFixture())
	out, _ := m.Update(tea.KeyMsg{Type: tea.KeyCtrlS})
	nm := out.(Model)
	if nm.input.kind != inputNone {
		t.Fatalf("filter form did not clear on ctrl+s: kind=%v", nm.input.kind)
	}
	if nm.input.saving {
		t.Fatal("saving=true after ctrl+s; filter form fell into commitFormInput")
	}
}

// TestFilterForm_NoBranchInRouteFormMutation: a stray form mutation
// arriving while the filter form is open MUST be dropped without
// touching the filter form's state. The filter form has its own
// formGen (allocated by openInput); a stray mutationDoneMsg whose
// formGen does not match is dropped harmlessly by routeFormMutation's
// formGen guard (jobs 242/244 fix).
//
// Pre-fix behavior: routeFormMutation fell through to the default
// detail-routing path, clearing the filter form (m.input = inputState{})
// and re-classifying the message as origin=detail — which silently
// closed the open filter modal whenever any unrelated form's response
// landed late. The new contract: stale form responses are dropped
// before they can touch a different form's state.
func TestFilterForm_NoBranchInRouteFormMutation(t *testing.T) {
	m := openFilterForm(t, filterFormFixture())
	preInput := m.input
	// formGen that cannot match the filter form's freshly-allocated one.
	mut := mutationDoneMsg{
		origin: "form", kind: "form.bogus",
		formGen: m.input.formGen + 999,
	}
	out, _ := m.routeFormMutation(mut)
	nm := out.(Model)
	// The new contract: filter form stays OPEN with state unchanged.
	if nm.input.kind != inputFilterForm {
		t.Fatalf("filter form was closed by stale form mutation; "+
			"the formGen guard must drop the message before the "+
			"isCenteredForm() fall-through clears it (kind=%v)",
			nm.input.kind)
	}
	if nm.input.formGen != preInput.formGen {
		t.Fatalf("filter form formGen mutated: got %d, want %d",
			nm.input.formGen, preInput.formGen)
	}
	if nm.input.saving != preInput.saving {
		t.Fatalf("filter form saving flag flipped: got %v, want %v",
			nm.input.saving, preInput.saving)
	}
}

// TestKeymap_OKeyGone (Plan 8 commit 5a): the FilterOwner field on
// keymap is gone (or no longer matches the `o` rune). `f` is bound
// to the filter modal in its place.
func TestKeymap_OKeyGone(t *testing.T) {
	km := newKeymap()
	if km.FilterForm.Help == "" {
		t.Fatal("FilterForm keymap entry missing")
	}
	if !km.FilterForm.matches(runeKey('f')) {
		t.Fatal("`f` rune does not match FilterForm key")
	}
	// Negative: any leftover key MUST not bind to `o`.
	for _, k := range []key{
		km.FilterStatus, km.FilterForm, km.ClearFilters,
		km.NewIssue, km.Search,
	} {
		if k.matches(runeKey('o')) {
			t.Fatalf("a list-filter keymap entry still binds o: %+v", k)
		}
	}
	// Negative: source must not reference inputOwnerBar / newOwnerBar /
	// FilterOwner.
	assertNoSourceReference(t, "inputOwnerBar")
	assertNoSourceReference(t, "newOwnerBar")
	assertNoSourceReference(t, "FilterOwner")
}

// TestHelpScreen_NoLongerMentionsO: the rendered help overlay must
// not contain the string "filter by owner" (the prior FilterOwner
// help text). It MUST contain "filter (form)" so the new entry is
// discoverable.
func TestHelpScreen_NoLongerMentionsO(t *testing.T) {
	out := renderHelp(newKeymap(), 120, ListFilter{})
	if strings.Contains(out, "filter by owner") {
		t.Fatalf("help still mentions retired 'filter by owner':\n%s", out)
	}
	if !strings.Contains(out, "filter (form)") {
		t.Fatalf("help missing new 'filter (form)' entry:\n%s", out)
	}
}

// TestSnapshot_FilterForm_AllAxes locks in the rendered modal layout
// when every axis is populated: Status=open, Owner=alice, Search=login.
// Status field is active (default on open) so the radio renders with
// the active label bolded upstream.
func TestSnapshot_FilterForm_AllAxes(t *testing.T) {
	defer snapshotInit(t)()
	s := newFilterForm(ListFilter{
		Status: "open", Owner: "alice", Search: "login",
	})
	got := renderCenteredForm(s, 120, 30)
	assertGolden(t, "filter-form-all-axes", got)
}

// TestSnapshot_List_WithFilterChipsFromModal commits the filter form
// with all three axes populated and snapshots the resulting list view.
// The chip strip in chrome must reflect every axis the modal applied.
//
// The fixture row matches every axis (status=open, owner=alice, title
// contains "login") so the body renders the matching row rather than
// the empty-state hint — that exercises the chip strip + matching
// row simultaneously.
func TestSnapshot_List_WithFilterChipsFromModal(t *testing.T) {
	defer snapshotInit(t)()
	lm := newListModel()
	lm.loading = false
	lm.issues = []Issue{{
		Number: 42, Title: "fix login bug on Safari", Status: "open",
		Owner:     ptrString("alice"),
		UpdatedAt: snapshotFixedNow.Add(-30 * 60_000_000_000), // 30m
	}}
	// The modal commit produces this filter:
	lm.filter = ListFilter{Status: "open", Owner: "alice", Search: "login"}
	chrome := viewChrome{
		scope:     scope{projectID: 7, projectName: "kata"},
		sseStatus: sseConnected,
		version:   "v0.1.0",
	}
	got := lm.View(120, 30, chrome)
	assertGolden(t, "list-with-filter-chips-from-modal", got)
}
