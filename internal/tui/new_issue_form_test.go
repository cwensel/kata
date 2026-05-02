package tui

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
)

// assertNoSourceReference scans every non-test .go file in the
// current package directory and fails when sym appears in any of
// them. Used by the negative-grep tests to guard against accidental
// re-introduction of removed symbols.
func assertNoSourceReference(t *testing.T, sym string) {
	t.Helper()
	entries, err := os.ReadDir(".")
	if err != nil {
		t.Fatalf("read dir: %v", err)
	}
	for _, e := range entries {
		name := e.Name()
		if e.IsDir() || !strings.HasSuffix(name, ".go") ||
			strings.HasSuffix(name, "_test.go") {
			continue
		}
		path := filepath.Join(".", name)
		buf, err := os.ReadFile(path) //nolint:gosec // path is under cwd
		if err != nil {
			t.Fatalf("read %s: %v", path, err)
		}
		if strings.Contains(string(buf), sym) {
			t.Fatalf("symbol %q must not appear in source file %s "+
				"(commit 4 dropped it)", sym, name)
		}
	}
}

// newIssueFormFixture returns a Model already at the list view with a
// resolved actor and a single-project scope so the `n` keybinding can
// open the form. Mirrors mFixtureForBar but seeds the scope so the
// all-projects gate doesn't fire.
func newIssueFormFixture() Model {
	return Model{
		view:   viewList,
		keymap: newKeymap(),
		scope:  scope{projectID: 7, projectName: "kata"},
		list:   listModel{actor: "tester"},
		cache:  newIssueCache(),
	}
}

// openNewIssueForm opens the form via the n keystroke + the resulting
// openInputCmd, returning a model with m.input.kind == inputNewIssueForm.
func openNewIssueForm(t *testing.T, m Model) Model {
	t.Helper()
	out, cmd := m.Update(runeKey('n'))
	m = out.(Model)
	if cmd == nil {
		t.Fatalf("press n produced no cmd; expected openInputCmd")
	}
	msg := cmd()
	out, _ = m.Update(msg)
	m = out.(Model)
	if m.input.kind != inputNewIssueForm {
		t.Fatalf("openInput did not land inputNewIssueForm; got %v", m.input.kind)
	}
	return m
}

// TestNewIssueForm_OpensOnNKey_ListView: pressing n on the list view
// opens the centered multi-field form (replaces the M3.5c inline row).
func TestNewIssueForm_OpensOnNKey_ListView(t *testing.T) {
	m := openNewIssueForm(t, newIssueFormFixture())
	if len(m.input.fields) != 4 {
		t.Fatalf("form fields = %d, want 4 (Title/Body/Labels/Owner)", len(m.input.fields))
	}
	wantLabels := []string{"Title", "Body", "Labels", "Owner"}
	for i, f := range m.input.fields {
		if f.label != wantLabels[i] {
			t.Fatalf("field[%d].label = %q, want %q", i, f.label, wantLabels[i])
		}
	}
	if !m.input.fields[0].required {
		t.Fatal("Title field must be required")
	}
}

// TestNewIssueForm_AllProjectsScopeIsNoOp: in cross-project view there
// is no projectID to create against, so n surfaces a status hint and
// does NOT open the form.
func TestNewIssueForm_AllProjectsScopeIsNoOp(t *testing.T) {
	m := newIssueFormFixture()
	m.scope = scope{allProjects: true}
	out, cmd := m.Update(runeKey('n'))
	nm := out.(Model)
	if cmd != nil {
		t.Fatalf("expected nil cmd in all-projects mode, got %T", cmd)
	}
	if nm.input.kind != inputNone {
		t.Fatalf("input opened in all-projects mode: kind=%v", nm.input.kind)
	}
	if nm.list.status == "" {
		t.Fatal("expected a status hint explaining the no-op")
	}
}

// TestNewIssueForm_ConstructorBlursAllFieldsFocusesField0: every
// non-active field is blurred so only the focused field renders the
// bubbles cursor, and the active field starts at index 0 (Title).
func TestNewIssueForm_ConstructorBlursAllFieldsFocusesField0(t *testing.T) {
	s := newNewIssueForm()
	if s.active != 0 {
		t.Fatalf("active = %d, want 0 (Title)", s.active)
	}
	if !s.fields[0].input.Focused() {
		t.Fatal("field[0] (Title) must be focused")
	}
	if s.fields[1].area.Focused() {
		t.Fatal("field[1] (Body) must be blurred")
	}
	if s.fields[2].input.Focused() {
		t.Fatal("field[2] (Labels) must be blurred")
	}
	if s.fields[3].input.Focused() {
		t.Fatal("field[3] (Owner) must be blurred")
	}
}

// TestNewIssueForm_TabCyclesFieldsWithWrap: tab cycles 0→1→2→3→0 and
// blurs/focuses the right fields each step.
func TestNewIssueForm_TabCyclesFieldsWithWrap(t *testing.T) {
	m := openNewIssueForm(t, newIssueFormFixture())
	wants := []int{1, 2, 3, 0}
	for i, want := range wants {
		out, _ := m.Update(tea.KeyMsg{Type: tea.KeyTab})
		m = out.(Model)
		if m.input.active != want {
			t.Fatalf("step %d: active = %d, want %d", i, m.input.active, want)
		}
	}
}

// TestNewIssueForm_ShiftTabReverseCyclesWithWrap: shift+tab cycles
// 0→3→2→1→0 with wrap.
func TestNewIssueForm_ShiftTabReverseCyclesWithWrap(t *testing.T) {
	m := openNewIssueForm(t, newIssueFormFixture())
	wants := []int{3, 2, 1, 0}
	for i, want := range wants {
		out, _ := m.Update(tea.KeyMsg{Type: tea.KeyShiftTab})
		m = out.(Model)
		if m.input.active != want {
			t.Fatalf("step %d: active = %d, want %d", i, m.input.active, want)
		}
	}
}

// TestNewIssueForm_EnterInSingleLineAdvancesField: enter on a single-
// line field advances to the next field instead of committing. Title
// → Body, Labels → Owner, Owner → Title (wrap).
func TestNewIssueForm_EnterInSingleLineAdvancesField(t *testing.T) {
	m := openNewIssueForm(t, newIssueFormFixture())
	out, cmd := m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	m = out.(Model)
	if cmd != nil {
		t.Fatalf("enter on Title dispatched a cmd %T; expected advance only", cmd)
	}
	if m.input.active != 1 {
		t.Fatalf("after enter on Title: active = %d, want 1 (Body)", m.input.active)
	}
	// Skip Body — enter inserts a newline there. Cycle to Labels (idx 2).
	out, _ = m.Update(tea.KeyMsg{Type: tea.KeyTab})
	m = out.(Model)
	if m.input.active != 2 {
		t.Fatalf("after tab from Body: active = %d, want 2 (Labels)", m.input.active)
	}
	out, _ = m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	m = out.(Model)
	if m.input.active != 3 {
		t.Fatalf("after enter on Labels: active = %d, want 3 (Owner)", m.input.active)
	}
	// Enter on Owner wraps to Title.
	out, _ = m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	m = out.(Model)
	if m.input.active != 0 {
		t.Fatalf("after enter on Owner: active = %d, want 0 (wrap to Title)", m.input.active)
	}
}

// TestNewIssueForm_EnterInBodyInsertsNewline: enter on the body field
// stays as a textarea newline insert (no advance).
func TestNewIssueForm_EnterInBodyInsertsNewline(t *testing.T) {
	m := openNewIssueForm(t, newIssueFormFixture())
	// Tab to Body.
	out, _ := m.Update(tea.KeyMsg{Type: tea.KeyTab})
	m = out.(Model)
	if m.input.active != 1 {
		t.Fatalf("setup: active = %d, want 1 (Body)", m.input.active)
	}
	// Type a line then enter then another line.
	for _, r := range "line1" {
		m, _ = stepModel(m, runeKey(r))
	}
	out, _ = m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	m = out.(Model)
	if m.input.active != 1 {
		t.Fatalf("enter on Body advanced field: active = %d, want 1", m.input.active)
	}
	for _, r := range "line2" {
		m, _ = stepModel(m, runeKey(r))
	}
	body := m.input.fields[1].area.Value()
	if !strings.Contains(body, "line1") || !strings.Contains(body, "line2") {
		t.Fatalf("body missing one of the lines: %q", body)
	}
	if !strings.Contains(body, "\n") {
		t.Fatalf("body missing newline; got %q", body)
	}
}

// TestNewIssueForm_CtrlSEmptyTitleSetsErrNoDispatch: ctrl+s with a
// blank Title sets the in-form err and does NOT dispatch.
func TestNewIssueForm_CtrlSEmptyTitleSetsErrNoDispatch(t *testing.T) {
	m := openNewIssueForm(t, newIssueFormFixture())
	out, cmd := m.Update(tea.KeyMsg{Type: tea.KeyCtrlS})
	nm := out.(Model)
	if cmd != nil {
		t.Fatalf("empty-title ctrl+s dispatched cmd %T; want nil", cmd)
	}
	if nm.input.kind != inputNewIssueForm {
		t.Fatalf("form closed on empty commit: kind=%v", nm.input.kind)
	}
	if nm.input.err == "" {
		t.Fatal("expected err on empty-title commit")
	}
	if nm.input.saving {
		t.Fatal("saving must NOT flip true on empty-title commit")
	}
}

// TestNewIssueForm_CtrlSTitleOnly_DispatchesWithMinimalPayload:
// ctrl+s with only a Title dispatches CreateIssue with empty body,
// nil owner, nil labels.
func TestNewIssueForm_CtrlSTitleOnly_DispatchesWithMinimalPayload(t *testing.T) {
	api := &fakeListAPI{createResult: &MutationResp{Issue: &Issue{Number: 99}}}
	m := openNewIssueForm(t, newIssueFormFixture())
	for _, r := range "fix bug" {
		m, _ = stepModel(m, runeKey(r))
	}
	// Drive dispatchCreateIssue directly to assert the wire shape; the
	// commit cmd uses Model.api which is *Client and unfittable to
	// fakeListAPI without major plumbing.
	_, cmd := m.list.dispatchCreateIssue(
		api, m.scope, "fix bug", "", nil, nil,
	)
	if cmd == nil {
		t.Fatal("expected dispatch cmd from non-empty title")
	}
	cmd()
	if api.createCalls != 1 {
		t.Fatalf("createCalls = %d, want 1", api.createCalls)
	}
	if api.lastCreateBody.Title != "fix bug" {
		t.Fatalf("title = %q, want fix bug", api.lastCreateBody.Title)
	}
	if api.lastCreateBody.Body != "" {
		t.Fatalf("body = %q, want empty", api.lastCreateBody.Body)
	}
	if api.lastCreateBody.Owner != nil {
		t.Fatalf("owner = %v, want nil", api.lastCreateBody.Owner)
	}
	if api.lastCreateBody.Labels != nil {
		t.Fatalf("labels = %v, want nil", api.lastCreateBody.Labels)
	}
}

// TestNewIssueForm_CtrlSAllFields_NormalizedPayload: with every field
// populated, commit produces the normalized payload — title sent
// untrimmed, owner trimmed, empty label tokens dropped, whitespace-
// only owner omitted.
func TestNewIssueForm_CtrlSAllFields_NormalizedPayload(t *testing.T) {
	api := &fakeListAPI{createResult: &MutationResp{Issue: &Issue{Number: 99}}}
	owner := "  alice  "
	labels := normalizeLabels("bug, , prio-1 ,  , feature")
	owned := normalizeOwner(owner)
	if owned == nil || *owned != "alice" {
		t.Fatalf("normalizeOwner mishandled trim: %v", owned)
	}
	wantLabels := []string{"bug", "prio-1", "feature"}
	if len(labels) != len(wantLabels) {
		t.Fatalf("normalizeLabels = %v, want %v", labels, wantLabels)
	}
	for i, w := range wantLabels {
		if labels[i] != w {
			t.Fatalf("labels[%d] = %q, want %q", i, labels[i], w)
		}
	}
	// Title sent untrimmed.
	_, cmd := listModel{actor: "tester"}.dispatchCreateIssue(
		api, scope{projectID: 7},
		"  spaced title  ", "body content", labels, owned,
	)
	if cmd == nil {
		t.Fatal("expected dispatch cmd")
	}
	cmd()
	if api.lastCreateBody.Title != "  spaced title  " {
		t.Fatalf("title = %q, want untrimmed", api.lastCreateBody.Title)
	}
	if api.lastCreateBody.Body != "body content" {
		t.Fatalf("body = %q, want body content", api.lastCreateBody.Body)
	}
	if api.lastCreateBody.Owner == nil || *api.lastCreateBody.Owner != "alice" {
		t.Fatalf("owner = %v, want trimmed alice", api.lastCreateBody.Owner)
	}
	if got := api.lastCreateBody.Labels; len(got) != 3 ||
		got[0] != "bug" || got[1] != "prio-1" || got[2] != "feature" {
		t.Fatalf("labels = %v, want [bug prio-1 feature]", got)
	}
	// Whitespace-only owner must yield nil.
	if normalizeOwner("   ") != nil {
		t.Fatal("normalizeOwner of whitespace must be nil")
	}
	// Empty labels must yield nil (so omitempty drops on the wire).
	if normalizeLabels(" , , ") != nil {
		t.Fatal("normalizeLabels of all-empty must be nil")
	}
}

// TestNewIssueForm_CtrlEOnlyWhenBodyFocused: ctrl+e produces an
// editor handoff cmd only when the Body field has focus; on Title /
// Labels / Owner it is a silent no-op (and the form stays open).
func TestNewIssueForm_CtrlEOnlyWhenBodyFocused(t *testing.T) {
	m := openNewIssueForm(t, newIssueFormFixture())
	// Title focused — ctrl+e is a no-op.
	out, cmd := m.Update(tea.KeyMsg{Type: tea.KeyCtrlE})
	m = out.(Model)
	if cmd != nil {
		t.Fatalf("ctrl+e on Title dispatched cmd %T; want nil (gated)", cmd)
	}
	if m.input.kind != inputNewIssueForm {
		t.Fatalf("ctrl+e on Title closed the form: kind=%v", m.input.kind)
	}
	// Tab to Body — ctrl+e fires.
	out, _ = m.Update(tea.KeyMsg{Type: tea.KeyTab})
	m = out.(Model)
	out, cmd = m.Update(tea.KeyMsg{Type: tea.KeyCtrlE})
	m = out.(Model)
	if cmd == nil {
		t.Fatal("ctrl+e on Body produced no editor handoff cmd")
	}
	if m.input.kind != inputNewIssueForm {
		t.Fatalf("ctrl+e on Body closed the form: kind=%v", m.input.kind)
	}
	// Tab through Labels and Owner — ctrl+e is a no-op for both.
	for _, want := range []int{2, 3} {
		out, _ = m.Update(tea.KeyMsg{Type: tea.KeyTab})
		m = out.(Model)
		if m.input.active != want {
			t.Fatalf("setup: active = %d, want %d", m.input.active, want)
		}
		_, cmd = m.Update(tea.KeyMsg{Type: tea.KeyCtrlE})
		if cmd != nil {
			t.Fatalf("ctrl+e on field[%d] dispatched cmd %T; want nil", want, cmd)
		}
	}
}

// TestNewIssueForm_StaleEditorReturnDropped: an editor return whose
// formGen mismatches the open form is silently discarded. Mirrors
// the existing single-field form's stale-return guard.
func TestNewIssueForm_StaleEditorReturnDropped(t *testing.T) {
	m := openNewIssueForm(t, newIssueFormFixture())
	// Tab to Body and seed it.
	out, _ := m.Update(tea.KeyMsg{Type: tea.KeyTab})
	m = out.(Model)
	m.input.fields[1].area.SetValue("user-typed body")
	staleGen := m.input.formGen + 999 // any non-matching value
	out, _ = m.Update(editorReturnedMsg{
		kind: "create", content: "stale editor content", formGen: staleGen,
	})
	nm := out.(Model)
	if got := nm.input.fields[1].area.Value(); got != "user-typed body" {
		t.Fatalf("body = %q, want unchanged (stale return must not write)", got)
	}
}

// TestNewIssueForm_MutationFailureLeavesFormOpenWithErr: a failed
// form-side create leaves the form open with err set and saving
// cleared so the user can retry.
func TestNewIssueForm_MutationFailureLeavesFormOpenWithErr(t *testing.T) {
	m := openNewIssueForm(t, newIssueFormFixture())
	m.input.saving = true
	out, _ := m.Update(mutationDoneMsg{
		origin: "form", kind: "create", err: errStub("daemon 500"),
	})
	nm := out.(Model)
	if nm.input.kind != inputNewIssueForm {
		t.Fatalf("form closed on failure: kind=%v", nm.input.kind)
	}
	if nm.input.saving {
		t.Fatal("saving stayed true after failure; user can't retry")
	}
	if !strings.Contains(nm.input.err, "daemon 500") {
		t.Fatalf("err = %q, want it to mention daemon 500", nm.input.err)
	}
}

// TestNewIssueForm_EscDiscardsAndReturnsToList: esc closes the form
// and does NOT auto-open detail (the M3.5c-era inline-row + M4 post-
// create chain forced detail open; the multi-field form does not).
func TestNewIssueForm_EscDiscardsAndReturnsToList(t *testing.T) {
	m := openNewIssueForm(t, newIssueFormFixture())
	for _, r := range "draft" {
		m, _ = stepModel(m, runeKey(r))
	}
	out, cmd := m.Update(tea.KeyMsg{Type: tea.KeyEsc})
	nm := out.(Model)
	if nm.input.kind != inputNone {
		t.Fatalf("esc did not close form: kind=%v", nm.input.kind)
	}
	if nm.view != viewList {
		t.Fatalf("view = %v, want viewList (no auto-detail)", nm.view)
	}
	if cmd != nil {
		if msg := cmd(); msg != nil {
			if _, isOpen := msg.(openDetailMsg); isOpen {
				t.Fatal("esc on new-issue form emitted openDetailMsg; must not auto-open detail")
			}
		}
	}
}

// TestNewIssueForm_MutationSuccessRoutesToList pins the hard
// invariant: a successful new-issue mutation closes the form, seeds
// the list selection with the new issue's number, and does NOT
// auto-open detail. The success path goes through list create
// handling (lm.applyMutation), not the detail re-classification used
// by the body-edit and comment forms.
func TestNewIssueForm_MutationSuccessRoutesToList(t *testing.T) {
	m := openNewIssueForm(t, newIssueFormFixture())
	m.input.saving = true
	mut := mutationDoneMsg{
		origin: "form", kind: "create",
		resp: &MutationResp{Issue: &Issue{Number: 99}},
	}
	out, _ := m.Update(mut)
	nm := out.(Model)
	if nm.input.kind != inputNone {
		t.Fatalf("form did not close on success: kind=%v", nm.input.kind)
	}
	if nm.list.selectedNumber != 99 {
		t.Fatalf("selectedNumber = %d, want 99 (seeded by lm.applyMutation)",
			nm.list.selectedNumber)
	}
	if nm.view == viewDetail {
		t.Fatal("view = viewDetail; new-issue form must NOT auto-open detail")
	}
}

// TestSnapshot_NewIssueForm_AllFields locks in the rendered modal
// layout when every field is populated. Body field is focused so the
// footer hint advertises the unrestricted ctrl+e handoff.
func TestSnapshot_NewIssueForm_AllFields(t *testing.T) {
	defer snapshotInit(t)()
	s := newNewIssueForm()
	s.fields[0].input.SetValue("fix login bug on Safari")
	s.fields[1].area.SetValue("Reproduces in Safari 17 only.\nClick login twice.")
	s.fields[2].input.SetValue("bug, prio-1")
	s.fields[3].input.SetValue("alice")
	// Focus Body so the footer says "ctrl+e $EDITOR" (unrestricted).
	s.fields[0].blur()
	s.active = 1
	_ = s.fields[1].focus()
	got := renderCenteredForm(s, 120, 30)
	assertGolden(t, "new-issue-form-all-fields", got)
}

// TestNewIssueForm_MutationSuccessRefreshesLabelCache pins the
// invariant that a successful form-side create routes through the
// label-cache refresh hook the same way list/detail mutations do.
//
// Bug it guards against (commit 4 follow-up I-1): routeFormMutation
// short-circuited at the top of routeMutation, so the
// mutAffectsLabelCounts → batchLabelRefresh wiring on the regular
// path was bypassed for inputNewIssueForm. Combined with the daemon
// emitting only issue.created (not issue.labeled) for create-with-
// labels, the per-project cache stayed stale until the next project
// switch / restart / unrelated label SSE event.
//
// Setup primes the cache for pid=7 with a known gen so the assertion
// can confirm dispatchLabelFetch ran (gen advanced + fetching=true).
func TestNewIssueForm_MutationSuccessRefreshesLabelCache(t *testing.T) {
	m := openNewIssueForm(t, newIssueFormFixture())
	m.input.saving = true
	// Prime the label cache for pid=7 as if the user had opened the
	// `+` menu against this project once already. Without an entry
	// present, batchLabelRefresh's existence gate would skip the
	// dispatch — but the test scenario is "user already opened menu,
	// then created with labels, expects fresh counts on next open".
	m.projectLabels = newLabelCache()
	m.projectLabels.byProject[7] = labelCacheEntry{
		pid: 7, gen: 1,
		labels: []LabelCount{{Label: "old", Count: 1}},
	}
	m.nextLabelsGen = 1
	mut := mutationDoneMsg{
		origin: "form", kind: "create",
		resp: &MutationResp{Issue: &Issue{Number: 99, ProjectID: 7}},
	}
	out, _ := m.Update(mut)
	nm := out.(Model)
	if nm.input.kind != inputNone {
		t.Fatalf("form did not close on success: kind=%v", nm.input.kind)
	}
	entry := nm.projectLabels.byProject[7]
	if !entry.fetching {
		t.Fatal("label cache for pid=7 did not enter fetching=true; " +
			"form-create success must dispatch a label refresh " +
			"(commit 4 follow-up I-1)")
	}
	if entry.gen <= 1 {
		t.Fatalf("label cache gen for pid=7 = %d, want > 1 "+
			"(dispatchLabelFetch must stamp a fresh gen)", entry.gen)
	}
}

// TestNoLingeringInlineRowReferences walks internal/tui/*.go (skipping
// test files) and asserts no source contains the symbol
// `inputNewIssueRow` — guards against accidental re-introduction of
// the M3.5c inline new-issue row code path.
func TestNoLingeringInlineRowReferences(t *testing.T) {
	assertNoSourceReference(t, "inputNewIssueRow")
}

// TestNoLingeringPostCreateChain mirrors TestNoLingeringInlineRowReferences
// for `openBodyEditPostCreate` — the M4 post-create chain symbol.
func TestNoLingeringPostCreateChain(t *testing.T) {
	assertNoSourceReference(t, "openBodyEditPostCreate")
}
