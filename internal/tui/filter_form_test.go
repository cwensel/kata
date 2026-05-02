package tui

import (
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
)

// listFilterEqual compares two ListFilter values by every field. The
// Labels axis (Plan 8 commit 5b) is included; the slice comparison
// uses element-wise equality so two label sets with the same contents
// in the same order match. Direct == fails because ListFilter
// contains a []string slice.
func listFilterEqual(a, b ListFilter) bool {
	if a.Status != b.Status || a.Owner != b.Owner ||
		a.Author != b.Author || a.Search != b.Search {
		return false
	}
	if len(a.Labels) != len(b.Labels) {
		return false
	}
	for i := range a.Labels {
		if a.Labels[i] != b.Labels[i] {
			return false
		}
	}
	return true
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
// centered four-axis filter modal. Field labels are Status / Owner /
// Search / Labels in order (Labels axis added in Plan 8 commit 5b).
func TestFilterForm_OpensOnFKey(t *testing.T) {
	m := openFilterForm(t, filterFormFixture())
	if len(m.input.fields) != 4 {
		t.Fatalf("form fields = %d, want 4 (Status/Owner/Search/Labels)",
			len(m.input.fields))
	}
	wantLabels := []string{"Status", "Owner", "Search", "Labels"}
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

// TestFilterForm_TabCyclesFourFields_WithWrap: tab cycles
// 0→1→2→3→0 (Status → Owner → Search → Labels → Status). Plan 8
// commit 5b added the Labels axis as the 4th field.
func TestFilterForm_TabCyclesFourFields_WithWrap(t *testing.T) {
	m := openFilterForm(t, filterFormFixture())
	wants := []int{1, 2, 3, 0}
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
// commitInput, and asserts the full ListFilter lands in lm.filter without
// dispatching a refetch.
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
	if cmd != nil {
		t.Fatalf("commit produced cmd %T; filters should apply client-side", cmd)
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

// TestFilterForm_CommitDoesNotRefetch: commit applies filters over the
// cached all-status working set and returns no command.
func TestFilterForm_CommitDoesNotRefetch(t *testing.T) {
	m := openFilterForm(t, filterFormFixture())
	out, cmd := m.Update(tea.KeyMsg{Type: tea.KeyCtrlS})
	if cmd != nil {
		t.Fatalf("expected nil cmd, got %T", cmd)
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
// esc still restores the at-open snapshot. Plan 8 commit 5b: the
// Labels field is now part of the reset.
func TestFilterForm_CtrlRResetsFieldsOnly_PreFilterIntact(t *testing.T) {
	m := filterFormFixture()
	m.list.filter = ListFilter{
		Status: "open", Owner: "wesm", Search: "bug",
		Labels: []string{"prio-1", "needs-review"},
	}
	m = openFilterForm(t, m)
	// preFilter snapshot should match.
	wantPre := ListFilter{
		Status: "open", Owner: "wesm", Search: "bug",
		Labels: []string{"prio-1", "needs-review"},
	}
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
	if got := m.input.fields[3].input.Value(); got != "" {
		t.Fatalf("Labels not reset: %q, want empty", got)
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

// TestFilterForm_LabelsField_AnyOfSemantics_AppliesViaCommit
// (Plan 8 commit 5b): typing labels into the form's Labels field
// commits via commitFilterForm — the resulting lm.filter.Labels is
// populated AND the any-of filter narrows the visible rows to issues
// carrying any of the typed labels.
func TestFilterForm_LabelsField_AnyOfSemantics_AppliesViaCommit(t *testing.T) {
	m := openFilterForm(t, filterFormFixture())
	// Tab three times so Labels (idx 3) is the active field.
	for i := 0; i < 3; i++ {
		out, _ := m.Update(tea.KeyMsg{Type: tea.KeyTab})
		m = out.(Model)
	}
	if m.input.active != 3 {
		t.Fatalf("active = %d, want 3 (Labels)", m.input.active)
	}
	for _, r := range "bug, prio-1" {
		m, _ = stepModel(m, runeKey(r))
	}
	out, _ := m.Update(tea.KeyMsg{Type: tea.KeyCtrlS})
	nm := out.(Model)
	want := []string{"bug", "prio-1"}
	if len(nm.list.filter.Labels) != len(want) {
		t.Fatalf("filter.Labels = %v, want %v", nm.list.filter.Labels, want)
	}
	for i := range want {
		if nm.list.filter.Labels[i] != want[i] {
			t.Fatalf("filter.Labels[%d] = %q, want %q",
				i, nm.list.filter.Labels[i], want[i])
		}
	}

	// Verify the any-of filter actually narrows: feed two issues, one
	// with "bug", one with "feature"; only the bug row survives.
	issues := []Issue{
		{Number: 1, Labels: []string{"bug"}},
		{Number: 2, Labels: []string{"feature"}},
	}
	got := filteredIssues(issues, nm.list.filter)
	if len(got) != 1 || got[0].Number != 1 {
		t.Fatalf("filteredIssues = %+v, want only #1 (any-of bug)", got)
	}
}

// TestFilterForm_LabelsField_FreeTypedInAllProjectsScope (Plan 8
// commit 5b): in all-projects mode, the Labels field accepts free
// text without a suggestion menu (no project label cache to source
// from). The form still opens and commits cleanly. Suggestion-menu
// wiring inside the form is deferred regardless of scope, but this
// test pins the all-projects fallback contract.
func TestFilterForm_LabelsField_FreeTypedInAllProjectsScope(t *testing.T) {
	m := filterFormFixture()
	m.scope = scope{allProjects: true}
	out, cmd := m.Update(runeKey('f'))
	if cmd == nil {
		t.Fatal("press f in all-projects mode must dispatch openInputCmd")
	}
	out, _ = out.(Model).Update(cmd())
	m = out.(Model)
	if m.input.kind != inputFilterForm {
		t.Fatalf("filter form did not open in all-projects mode: kind=%v", m.input.kind)
	}
	// Tab to Labels (idx 3); type free text.
	for i := 0; i < 3; i++ {
		out, _ := m.Update(tea.KeyMsg{Type: tea.KeyTab})
		m = out.(Model)
	}
	for _, r := range "ad-hoc-label" {
		m, _ = stepModel(m, runeKey(r))
	}
	out, _ = m.Update(tea.KeyMsg{Type: tea.KeyCtrlS})
	nm := out.(Model)
	if len(nm.list.filter.Labels) != 1 || nm.list.filter.Labels[0] != "ad-hoc-label" {
		t.Fatalf("filter.Labels = %v, want [ad-hoc-label]", nm.list.filter.Labels)
	}
}

// TestRenderChips_IncludesLabelChips (Plan 8 commit 5b): the chip
// strip in chrome renders one chip per label. Pre-fix the label
// chips were intentionally omitted (the wire didn't carry labels);
// commit 5b unlocks them.
func TestRenderChips_IncludesLabelChips(t *testing.T) {
	defer snapshotInit(t)()
	out := renderChips(ListFilter{
		Status: "open", Owner: "alice",
		Labels: []string{"bug", "prio-1"},
	})
	for _, want := range []string{"label:bug", "label:prio-1", "status:open", "owner:alice"} {
		if !strings.Contains(out, want) {
			t.Errorf("renderChips missing %q in:\n%s", want, out)
		}
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

// TestSnapshot_FilterForm_WithLabelsAxis (Plan 8 commit 5b) locks in
// the rendered modal with all four axes populated, including the
// Labels row pre-filled from a non-empty current.Labels slice.
func TestSnapshot_FilterForm_WithLabelsAxis(t *testing.T) {
	defer snapshotInit(t)()
	s := newFilterForm(ListFilter{
		Status: "open", Owner: "alice", Search: "login",
		Labels: []string{"bug", "prio-1"},
	})
	got := renderCenteredForm(s, 120, 30)
	assertGolden(t, "filter-form-with-labels-axis", got)
}

// TestSnapshot_List_WithFilterChipsFromModal commits the filter form
// with all four axes populated and snapshots the resulting list view.
// The chip strip in chrome must reflect every axis the modal applied,
// including the Plan 8 commit 5b Labels chips.
//
// The fixture row matches every axis (status=open, owner=alice, title
// contains "login", labels include "bug") so the body renders the
// matching row rather than the empty-state hint — that exercises the
// chip strip + matching row simultaneously.
func TestSnapshot_List_WithFilterChipsFromModal(t *testing.T) {
	defer snapshotInit(t)()
	lm := newListModel()
	lm.loading = false
	lm.issues = []Issue{{
		Number: 42, Title: "fix login bug on Safari", Status: "open",
		Owner:     ptrString("alice"),
		Labels:    []string{"bug", "prio-1"},
		UpdatedAt: snapshotFixedNow.Add(-30 * 60_000_000_000), // 30m
	}}
	// The modal commit produces this filter:
	lm.filter = ListFilter{
		Status: "open", Owner: "alice", Search: "login",
		Labels: []string{"bug"},
	}
	chrome := viewChrome{
		scope:     scope{projectID: 7, projectName: "kata"},
		sseStatus: sseConnected,
		version:   "v0.1.0",
	}
	got := lm.View(120, 30, chrome)
	assertGolden(t, "list-with-filter-chips-from-modal", got)
}
