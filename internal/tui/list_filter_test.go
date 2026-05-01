package tui

import (
	"context"
	"errors"
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
)

// fakeListAPI is the test double for listAPI. Each method records its
// last input on the receiver and returns whatever the test seeded into
// the corresponding result fields. Counters surface "exactly N calls"
// assertions so empty-title regression tests stay direct.
type fakeListAPI struct {
	listIssuesCalls    int
	listAllCalls       int
	createCalls        int
	closeCalls         int
	reopenCalls        int
	lastListProjectID  int64
	lastListFilter     ListFilter
	lastCreateProject  int64
	lastCreateBody     CreateIssueBody
	lastCloseProjectID int64
	lastCloseNumber    int64
	lastCloseActor     string
	lastReopenProject  int64
	lastReopenNumber   int64
	lastReopenActor    string
	listIssuesResult   []Issue
	listAllResult      []Issue
	createResult       *MutationResp
	closeResult        *MutationResp
	reopenResult       *MutationResp
	listIssuesErr      error
	listAllErr         error
	createErr          error
	closeErr           error
	reopenErr          error
}

func (f *fakeListAPI) ListIssues(
	_ context.Context, projectID int64, filter ListFilter,
) ([]Issue, error) {
	f.listIssuesCalls++
	f.lastListProjectID = projectID
	f.lastListFilter = filter
	return f.listIssuesResult, f.listIssuesErr
}

func (f *fakeListAPI) ListAllIssues(
	_ context.Context, filter ListFilter,
) ([]Issue, error) {
	f.listAllCalls++
	f.lastListFilter = filter
	return f.listAllResult, f.listAllErr
}

func (f *fakeListAPI) CreateIssue(
	_ context.Context, projectID int64, body CreateIssueBody,
) (*MutationResp, error) {
	f.createCalls++
	f.lastCreateProject = projectID
	f.lastCreateBody = body
	return f.createResult, f.createErr
}

func (f *fakeListAPI) Close(
	_ context.Context, projectID, number int64, actor string,
) (*MutationResp, error) {
	f.closeCalls++
	f.lastCloseProjectID = projectID
	f.lastCloseNumber = number
	f.lastCloseActor = actor
	return f.closeResult, f.closeErr
}

func (f *fakeListAPI) Reopen(
	_ context.Context, projectID, number int64, actor string,
) (*MutationResp, error) {
	f.reopenCalls++
	f.lastReopenProject = projectID
	f.lastReopenNumber = number
	f.lastReopenActor = actor
	return f.reopenResult, f.reopenErr
}

// runeKey synthesizes a tea.KeyMsg for a single rune so tests don't
// have to repeat the struct construction. Multi-character buffers are
// fed one rune at a time to mirror real keystrokes.
func runeKey(r rune) tea.KeyMsg {
	return tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{r}}
}

// drainCmd executes the tea.Cmd returned by Update once and feeds the
// resulting message back into Update so the test sees the post-fetch
// state. Returns the second-pass model so chains stay one-line.
func drainCmd(
	t *testing.T, lm listModel, cmd tea.Cmd, km keymap, api listAPI, sc scope,
) listModel {
	t.Helper()
	if cmd == nil {
		return lm
	}
	msg := cmd()
	out, _ := lm.Update(msg, km, api, sc)
	return out
}

// TestList_StatusCycle confirms `s` cycles "" → open → closed → "" and
// each cycle dispatches a refetch. The third press lands back on "" so
// the chip strip empties.
func TestList_StatusCycle(t *testing.T) {
	api := &fakeListAPI{}
	km := newKeymap()
	sc := scope{projectID: 7}
	lm := listModel{}

	wants := []string{"open", "closed", ""}
	for i, want := range wants {
		var cmd tea.Cmd
		lm, cmd = lm.Update(runeKey('s'), km, api, sc)
		if lm.filter.Status != want {
			t.Fatalf("step %d: status = %q, want %q", i, lm.filter.Status, want)
		}
		if cmd == nil {
			t.Fatalf("step %d: expected refetch cmd, got nil", i)
		}
		_ = cmd() // execute so the fake records it
	}
	if api.listIssuesCalls != 3 {
		t.Fatalf("listIssuesCalls = %d, want 3", api.listIssuesCalls)
	}
}

// TestList_Search_AccumulatesAndCommits drives /, then "abc", then
// Enter. The buffer must reach filter.Search and a refetch must fire.
func TestList_Search_AccumulatesAndCommits(t *testing.T) {
	api := &fakeListAPI{}
	km := newKeymap()
	sc := scope{projectID: 7}

	lm, _ := lmFromUpdate(listModel{}, runeKey('/'), km, api, sc)
	if !lm.search.inputting || lm.search.field != searchFieldQuery {
		t.Fatalf("expected query prompt active, got %+v", lm.search)
	}
	for _, r := range "abc" {
		lm, _ = lmFromUpdate(lm, runeKey(r), km, api, sc)
	}
	if lm.search.buffer != "abc" {
		t.Fatalf("buffer = %q, want %q", lm.search.buffer, "abc")
	}
	lm, cmd := lmFromUpdate(lm, tea.KeyMsg{Type: tea.KeyEnter}, km, api, sc)
	if lm.filter.Search != "abc" {
		t.Fatalf("filter.Search = %q, want abc", lm.filter.Search)
	}
	if lm.search.inputting {
		t.Fatal("inputting should be cleared after Enter")
	}
	_ = cmd()
	if api.listIssuesCalls != 1 {
		t.Fatalf("listIssuesCalls = %d, want 1", api.listIssuesCalls)
	}
}

// TestList_Search_EscCancels confirms Esc clears the buffer without
// touching filter.Search and without dispatching a refetch.
func TestList_Search_EscCancels(t *testing.T) {
	api := &fakeListAPI{}
	km := newKeymap()
	sc := scope{projectID: 7}

	lm := listModel{filter: ListFilter{Search: "previous"}}
	lm, _ = lmFromUpdate(lm, runeKey('/'), km, api, sc)
	for _, r := range "xyz" {
		lm, _ = lmFromUpdate(lm, runeKey(r), km, api, sc)
	}
	lm, cmd := lmFromUpdate(lm, tea.KeyMsg{Type: tea.KeyEsc}, km, api, sc)
	if cmd != nil {
		t.Fatalf("Esc must not dispatch a refetch")
	}
	if lm.search.inputting {
		t.Fatal("inputting must be false after Esc")
	}
	if lm.search.buffer != "" {
		t.Fatalf("buffer = %q, want empty", lm.search.buffer)
	}
	if lm.filter.Search != "previous" {
		t.Fatalf("filter.Search clobbered: %q", lm.filter.Search)
	}
}

// TestList_ClearFilters_ZeroesEveryField: `c` zeroes every filter slot
// and dispatches a refetch. There is no IncludeDeleted slot today (see
// ListFilter doc) so the post-state is the zero value.
func TestList_ClearFilters_ZeroesEveryField(t *testing.T) {
	api := &fakeListAPI{}
	km := newKeymap()
	sc := scope{projectID: 7}

	lm := listModel{filter: ListFilter{
		Status: "open", Owner: "wes", Search: "bug",
		Labels: []string{"prio-1"},
	}}
	lm, cmd := lm.Update(runeKey('c'), km, api, sc)
	if lm.filter.Status != "" || lm.filter.Owner != "" || lm.filter.Search != "" ||
		len(lm.filter.Labels) != 0 {
		t.Fatalf("filters not cleared: %+v", lm.filter)
	}
	if cmd == nil {
		t.Fatal("expected refetch on clear")
	}
}

// TestList_NewIssue_EmptyTitleDoesNotCallAPI: the user opens `n`, hits
// Enter immediately, and the create endpoint must not be called.
func TestList_NewIssue_EmptyTitleDoesNotCallAPI(t *testing.T) {
	api := &fakeListAPI{}
	km := newKeymap()
	sc := scope{projectID: 7}

	lm, _ := lmFromUpdate(listModel{}, runeKey('n'), km, api, sc)
	if !lm.search.inputting || lm.search.field != searchFieldNewTitle {
		t.Fatalf("expected new-title prompt, got %+v", lm.search)
	}
	lm, cmd := lmFromUpdate(lm, tea.KeyMsg{Type: tea.KeyEnter}, km, api, sc)
	if cmd != nil {
		// commitPrompt returns nil for empty new title; cmd should be nil.
		t.Fatal("empty title must not return a command")
	}
	if api.createCalls != 0 {
		t.Fatalf("createCalls = %d, want 0", api.createCalls)
	}
	if lm.search.inputting {
		t.Fatal("prompt should close after Enter even on empty title")
	}
}

// TestList_NewIssue_NonEmptyTitleStagesAndDispatchesEditor: `n` then
// "fix bug" then Enter stages the title in lm.pendingTitle and returns
// a tea.Cmd that suspends to $EDITOR. CreateIssue must NOT have run
// yet — the body comes back via editorReturnedMsg in a second pass.
func TestList_NewIssue_NonEmptyTitleStagesAndDispatchesEditor(t *testing.T) {
	api := &fakeListAPI{}
	km := newKeymap()
	sc := scope{projectID: 7}

	lm, _ := lmFromUpdate(listModel{actor: "tester"}, runeKey('n'), km, api, sc)
	for _, r := range "fix bug" {
		lm, _ = lmFromUpdate(lm, runeKey(r), km, api, sc)
	}
	lm, cmd := lmFromUpdate(lm, tea.KeyMsg{Type: tea.KeyEnter}, km, api, sc)
	if cmd == nil {
		t.Fatal("expected editor cmd from Enter on non-empty title")
	}
	if lm.pendingTitle != "fix bug" {
		t.Fatalf("pendingTitle = %q, want %q", lm.pendingTitle, "fix bug")
	}
	if api.createCalls != 0 {
		t.Fatalf("createCalls = %d, want 0 (must wait for editor)",
			api.createCalls)
	}
}

// TestList_NewIssue_BodyFlow_PostsTitleAndBody: the editor returns
// content="some body". CreateIssue must be called with both the staged
// title and the body. lm.pendingTitle is cleared after dispatch.
func TestList_NewIssue_BodyFlow_PostsTitleAndBody(t *testing.T) {
	api := &fakeListAPI{
		createResult: &MutationResp{Issue: &Issue{Number: 42, Title: "fix bug"}},
	}
	km := newKeymap()
	sc := scope{projectID: 7}

	lm, _ := lmFromUpdate(listModel{actor: "tester"}, runeKey('n'), km, api, sc)
	for _, r := range "fix bug" {
		lm, _ = lmFromUpdate(lm, runeKey(r), km, api, sc)
	}
	lm, _ = lmFromUpdate(lm, tea.KeyMsg{Type: tea.KeyEnter}, km, api, sc)
	if lm.pendingTitle != "fix bug" {
		t.Fatalf("pendingTitle = %q, want %q", lm.pendingTitle, "fix bug")
	}
	msg := editorReturnedMsg{kind: "create", content: "some body"}
	lm, cmd := lmFromUpdate(lm, msg, km, api, sc)
	if cmd == nil {
		t.Fatal("expected create cmd from editorReturnedMsg")
	}
	if lm.pendingTitle != "" {
		t.Fatalf("pendingTitle = %q, want empty after dispatch", lm.pendingTitle)
	}
	lm = drainCmd(t, lm, cmd, km, api, sc)
	if api.createCalls != 1 {
		t.Fatalf("createCalls = %d, want 1", api.createCalls)
	}
	if api.lastCreateBody.Title != "fix bug" {
		t.Fatalf("title = %q, want %q", api.lastCreateBody.Title, "fix bug")
	}
	if api.lastCreateBody.Body != "some body" {
		t.Fatalf("body = %q, want %q", api.lastCreateBody.Body, "some body")
	}
	if api.lastCreateBody.Actor != "tester" {
		t.Fatalf("actor = %q, want tester", api.lastCreateBody.Actor)
	}
}

// TestList_NewIssue_EmptyBodyStillCreates: a saved-empty editor returns
// kind=create content="" — CreateIssue should still post (body="")
// because the title is what governs creation; body is optional.
func TestList_NewIssue_EmptyBodyStillCreates(t *testing.T) {
	api := &fakeListAPI{
		createResult: &MutationResp{Issue: &Issue{Number: 42}},
	}
	km := newKeymap()
	sc := scope{projectID: 7}

	lm := listModel{actor: "tester", pendingTitle: "fix bug"}
	msg := editorReturnedMsg{kind: "create", content: ""}
	lm, cmd := lm.Update(msg, km, api, sc)
	if cmd == nil {
		t.Fatal("expected create cmd even with empty body")
	}
	_ = drainCmd(t, lm, cmd, km, api, sc)
	if api.createCalls != 1 {
		t.Fatalf("createCalls = %d, want 1", api.createCalls)
	}
	if api.lastCreateBody.Body != "" {
		t.Fatalf("body = %q, want empty", api.lastCreateBody.Body)
	}
}

// TestList_NewIssue_EditorError_SurfacesStatus: if the editor exits
// with an error, no CreateIssue is dispatched and the user sees a
// hint on the status line.
func TestList_NewIssue_EditorError_SurfacesStatus(t *testing.T) {
	api := &fakeListAPI{}
	km := newKeymap()
	sc := scope{projectID: 7}

	lm := listModel{actor: "tester", pendingTitle: "fix bug"}
	msg := editorReturnedMsg{kind: "create", err: errStub("boom")}
	out, cmd := lm.Update(msg, km, api, sc)
	if cmd != nil {
		t.Fatalf("expected nil cmd on editor error, got %T", cmd)
	}
	if api.createCalls != 0 {
		t.Fatalf("createCalls = %d, want 0", api.createCalls)
	}
	if !strings.Contains(out.status, "boom") {
		t.Fatalf("status = %q, want to contain editor error", out.status)
	}
	if out.pendingTitle != "" {
		t.Fatalf("pendingTitle should clear on error, got %q", out.pendingTitle)
	}
}

// TestList_NewIssue_PreservesMarkdownHeadings: the create flow does
// NOT strip Markdown headings from the body. Earlier the # strip
// destroyed legitimate headings; the sentinel-block strip leaves them
// intact. The create-kind template seeds an empty buffer, so there is
// nothing to strip in this path.
func TestList_NewIssue_PreservesMarkdownHeadings(t *testing.T) {
	api := &fakeListAPI{
		createResult: &MutationResp{Issue: &Issue{Number: 42}},
	}
	km := newKeymap()
	sc := scope{projectID: 7}

	lm := listModel{actor: "tester", pendingTitle: "fix bug"}
	msg := editorReturnedMsg{kind: "create", content: "# Heading\nbody"}
	lm, cmd := lm.Update(msg, km, api, sc)
	if cmd == nil {
		t.Fatal("expected create cmd")
	}
	_ = drainCmd(t, lm, cmd, km, api, sc)
	if api.lastCreateBody.Body != "# Heading\nbody" {
		t.Fatalf("body = %q, want %q (heading preserved)",
			api.lastCreateBody.Body, "# Heading\nbody")
	}
}

// TestList_NewIssue_AllProjectsModeIsNoOp: in cross-project view there
// is no projectID to create against, so 'n' should not open the prompt
// and should leave a status hint.
func TestList_NewIssue_AllProjectsModeIsNoOp(t *testing.T) {
	api := &fakeListAPI{}
	km := newKeymap()
	sc := scope{allProjects: true}

	lm, _ := lmFromUpdate(listModel{}, runeKey('n'), km, api, sc)
	if lm.search.inputting {
		t.Fatal("must not open prompt in all-projects mode")
	}
	if lm.status == "" {
		t.Fatal("expected status hint explaining the no-op")
	}
	if api.createCalls != 0 {
		t.Fatalf("createCalls = %d, want 0", api.createCalls)
	}
}

// TestList_OwnerPrompt_AccumulatesAndCommits drives `o`, "claude", Enter
// and checks filter.Owner + refetch.
func TestList_OwnerPrompt_AccumulatesAndCommits(t *testing.T) {
	api := &fakeListAPI{}
	km := newKeymap()
	sc := scope{projectID: 7}

	lm, _ := lmFromUpdate(listModel{}, runeKey('o'), km, api, sc)
	if lm.search.field != searchFieldOwner {
		t.Fatalf("expected owner field, got %v", lm.search.field)
	}
	for _, r := range "claude" {
		lm, _ = lmFromUpdate(lm, runeKey(r), km, api, sc)
	}
	lm, cmd := lmFromUpdate(lm, tea.KeyMsg{Type: tea.KeyEnter}, km, api, sc)
	if lm.filter.Owner != "claude" {
		t.Fatalf("filter.Owner = %q, want %q", lm.filter.Owner, "claude")
	}
	if cmd == nil {
		t.Fatal("expected refetch")
	}
}

// TestList_LabelPrompt_SplitsCSV: user enters "bug, ui" → two labels.
func TestList_LabelPrompt_SplitsCSV(t *testing.T) {
	api := &fakeListAPI{}
	km := newKeymap()
	sc := scope{projectID: 7}

	lm, _ := lmFromUpdate(listModel{}, runeKey('l'), km, api, sc)
	for _, r := range "bug, ui" {
		lm, _ = lmFromUpdate(lm, runeKey(r), km, api, sc)
	}
	// Space arrives as KeyRunes too; the loop above already covers the
	// space-after-comma case.
	lm, _ = lmFromUpdate(lm, tea.KeyMsg{Type: tea.KeyEnter}, km, api, sc)
	if len(lm.filter.Labels) != 2 ||
		lm.filter.Labels[0] != "bug" || lm.filter.Labels[1] != "ui" {
		t.Fatalf("labels = %v, want [bug ui]", lm.filter.Labels)
	}
}

// TestList_BackspaceTrimsBuffer: backspace removes the last rune.
func TestList_BackspaceTrimsBuffer(t *testing.T) {
	api := &fakeListAPI{}
	km := newKeymap()
	sc := scope{projectID: 7}

	lm, _ := lmFromUpdate(listModel{}, runeKey('/'), km, api, sc)
	for _, r := range "abc" {
		lm, _ = lmFromUpdate(lm, runeKey(r), km, api, sc)
	}
	lm, _ = lmFromUpdate(lm, tea.KeyMsg{Type: tea.KeyBackspace}, km, api, sc)
	if lm.search.buffer != "ab" {
		t.Fatalf("buffer = %q, want ab", lm.search.buffer)
	}
}

// TestList_QuitGate_RoutesQuitToBuffer covers the model-level gate: a
// 'q' keystroke while a list prompt is open must reach lm.search.buffer
// instead of triggering tea.Quit. We drive Model.Update directly here.
// m.api is nil because the buffer-append path never touches it; if it
// did, the test would panic with a nil-deref instead of silently
// passing.
func TestList_QuitGate_RoutesQuitToBuffer(t *testing.T) {
	m := initialModel(Options{})
	m.scope = scope{projectID: 7}
	m.list.loading = false
	out, _ := m.Update(runeKey('/'))
	m = out.(Model)
	if !m.list.search.inputting {
		t.Fatal("prompt did not open")
	}
	out, cmd := m.Update(runeKey('q'))
	m = out.(Model)
	if cmd != nil {
		t.Fatalf("expected no command (q must reach buffer), got %T", cmd)
	}
	if m.list.search.buffer != "q" {
		t.Fatalf("buffer = %q, want q", m.list.search.buffer)
	}
}

// TestList_RefetchError_PutsErrOnModel ensures fetch failures surface in
// lm.err so View renders the error state and the user can retry.
func TestList_RefetchError_PutsErrOnModel(t *testing.T) {
	api := &fakeListAPI{listIssuesErr: errors.New("boom")}
	km := newKeymap()
	sc := scope{projectID: 7}

	lm, cmd := listModel{}.Update(runeKey('s'), km, api, sc)
	if cmd == nil {
		t.Fatal("expected refetch")
	}
	lm = drainCmd(t, lm, cmd, km, api, sc)
	if lm.err == nil || lm.err.Error() != "boom" {
		t.Fatalf("err = %v, want boom", lm.err)
	}
}

// lmFromUpdate is a one-line wrapper around lm.Update so the test code
// that doesn't care about the cmd doesn't have to declare extra vars.
// The signature mirrors lm.Update so callers can drop in whichever they
// need.
func lmFromUpdate(
	lm listModel, msg tea.Msg, km keymap, api listAPI, sc scope,
) (listModel, tea.Cmd) {
	return lm.Update(msg, km, api, sc)
}

// TestList_OwnerFilter_NarrowsDisplay confirms filteredIssues drops
// rows whose Owner does not match. The fixture exercises the *string
// branch (alice matches twice, bob is filtered out, nil-owner case is
// covered by TestList_NoFilter_PassThrough).
func TestList_OwnerFilter_NarrowsDisplay(t *testing.T) {
	issues := []Issue{
		{Number: 1, Owner: ptrString("alice"), Title: "a"},
		{Number: 2, Owner: ptrString("bob"), Title: "b"},
		{Number: 3, Owner: ptrString("alice"), Title: "c"},
	}
	out := filteredIssues(issues, ListFilter{Owner: "alice"})
	if len(out) != 2 {
		t.Fatalf("got %d, want 2", len(out))
	}
	if out[0].Number != 1 || out[1].Number != 3 {
		t.Fatalf("wrong issues filtered: %+v", out)
	}
}

// TestList_OwnerFilter_NilOwnerNeverMatches: a row with no owner can
// never satisfy a set Owner filter, even if the filter string is
// empty. (Empty filter is the no-filter fast path; non-empty plus nil
// owner is the case under test here.)
func TestList_OwnerFilter_NilOwnerNeverMatches(t *testing.T) {
	issues := []Issue{
		{Number: 1, Title: "no owner"},
		{Number: 2, Owner: ptrString("alice"), Title: "owned"},
	}
	out := filteredIssues(issues, ListFilter{Owner: "alice"})
	if len(out) != 1 || out[0].Number != 2 {
		t.Fatalf("expected only #2, got %+v", out)
	}
}

// TestList_SearchFilter_CaseInsensitive: the search box is forgiving
// about case so users typing "login" find "LOGIN bug" and vice versa.
func TestList_SearchFilter_CaseInsensitive(t *testing.T) {
	issues := []Issue{
		{Number: 1, Title: "Fix LOGIN bug"},
		{Number: 2, Title: "deploy"},
	}
	out := filteredIssues(issues, ListFilter{Search: "login"})
	if len(out) != 1 || out[0].Number != 1 {
		t.Fatalf("expected #1 only, got %+v", out)
	}
}

// TestList_NoFilter_PassThrough: with no client-side filter set the
// fast path returns the input unchanged so the steady state pays no
// per-render allocation.
func TestList_NoFilter_PassThrough(t *testing.T) {
	issues := []Issue{
		{Number: 1, Owner: ptrString("alice"), Title: "a"},
		{Number: 2, Title: "b"},
	}
	out := filteredIssues(issues, ListFilter{})
	if len(out) != 2 {
		t.Fatalf("expected pass-through, got %d", len(out))
	}
}

// TestList_AuthorFilter_NarrowsDisplay covers the Author branch even
// though there's no key binding for it yet — ListFilter.Author is on
// the struct (Task 6 left it in for forward compat) and matchesFilter
// honors it. When a future task adds an `a` keystroke to filter by
// author, this test guards the wiring.
func TestList_AuthorFilter_NarrowsDisplay(t *testing.T) {
	issues := []Issue{
		{Number: 1, Author: "wes", Title: "a"},
		{Number: 2, Author: "claude", Title: "b"},
		{Number: 3, Author: "wes", Title: "c"},
	}
	out := filteredIssues(issues, ListFilter{Author: "wes"})
	if len(out) != 2 || out[0].Number != 1 || out[1].Number != 3 {
		t.Fatalf("wrong issues filtered: %+v", out)
	}
}

// TestList_Close_DispatchesAPI: j to row 2, 'x' calls api.Close with
// the row 2 issue's number, threading the actor through. The fixture
// uses two rows so cursor!=0 is observable.
func TestList_Close_DispatchesAPI(t *testing.T) {
	api := &fakeListAPI{
		closeResult: &MutationResp{Issue: &Issue{Number: 2, Status: "closed"}},
	}
	km := newKeymap()
	sc := scope{projectID: 7}
	lm := listModel{
		actor: "tester",
		issues: []Issue{
			{ProjectID: 7, Number: 1, Title: "first"},
			{ProjectID: 7, Number: 2, Title: "second"},
		},
	}

	lm, _ = lm.Update(runeKey('j'), km, api, sc)
	if lm.cursor != 1 {
		t.Fatalf("cursor = %d, want 1 after j", lm.cursor)
	}
	lm, cmd := lm.Update(runeKey('x'), km, api, sc)
	if cmd == nil {
		t.Fatal("expected close cmd from x")
	}
	_ = drainCmd(t, lm, cmd, km, api, sc)
	if api.closeCalls != 1 {
		t.Fatalf("closeCalls = %d, want 1", api.closeCalls)
	}
	if api.lastCloseProjectID != 7 || api.lastCloseNumber != 2 {
		t.Fatalf("close args wrong: pid=%d num=%d",
			api.lastCloseProjectID, api.lastCloseNumber)
	}
	if api.lastCloseActor != "tester" {
		t.Fatalf("lastCloseActor = %q, want tester", api.lastCloseActor)
	}
}

// TestList_Reopen_DispatchesAPI mirrors TestList_Close_DispatchesAPI for
// the 'r' binding.
func TestList_Reopen_DispatchesAPI(t *testing.T) {
	api := &fakeListAPI{
		reopenResult: &MutationResp{Issue: &Issue{Number: 1, Status: "open"}},
	}
	km := newKeymap()
	sc := scope{projectID: 7}
	lm := listModel{
		actor: "tester",
		issues: []Issue{
			{ProjectID: 7, Number: 1, Title: "first"},
		},
	}

	lm, cmd := lm.Update(runeKey('r'), km, api, sc)
	if cmd == nil {
		t.Fatal("expected reopen cmd from r")
	}
	_ = drainCmd(t, lm, cmd, km, api, sc)
	if api.reopenCalls != 1 {
		t.Fatalf("reopenCalls = %d, want 1", api.reopenCalls)
	}
	if api.lastReopenNumber != 1 || api.lastReopenActor != "tester" {
		t.Fatalf("reopen args wrong: num=%d actor=%q",
			api.lastReopenNumber, api.lastReopenActor)
	}
}

// TestList_Close_EmptyListNoOp: 'x' on an empty list does not call
// api.Close and does not panic.
func TestList_Close_EmptyListNoOp(t *testing.T) {
	api := &fakeListAPI{}
	km := newKeymap()
	sc := scope{projectID: 7}
	lm := listModel{actor: "tester"}

	_, cmd := lm.Update(runeKey('x'), km, api, sc)
	if cmd != nil {
		t.Fatalf("expected nil cmd on empty list, got %T", cmd)
	}
	if api.closeCalls != 0 {
		t.Fatalf("closeCalls = %d, want 0", api.closeCalls)
	}
}
