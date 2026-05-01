package tui

import (
	"context"
	"errors"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
)

// fakeListAPI is the test double for listAPI. Each method records its
// last input on the receiver and returns whatever the test seeded into
// the corresponding result fields. Counters surface "exactly N calls"
// assertions so empty-title regression tests stay direct.
type fakeListAPI struct {
	listIssuesCalls                      int
	listAllCalls                         int
	createCalls                          int
	lastListProjectID                    int64
	lastListFilter                       ListFilter
	lastCreateProject                    int64
	lastCreateBody                       CreateIssueBody
	listIssuesResult                     []Issue
	listAllResult                        []Issue
	createResult                         *MutationResp
	listIssuesErr, listAllErr, createErr error
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

// TestList_ClearFilters_PreservesIncludeDeleted: `c` zeroes the filter
// fields except IncludeDeleted, which is set at boot from --include-deleted.
func TestList_ClearFilters_PreservesIncludeDeleted(t *testing.T) {
	api := &fakeListAPI{}
	km := newKeymap()
	sc := scope{projectID: 7}

	lm := listModel{filter: ListFilter{
		Status: "open", Owner: "wes", Search: "bug",
		Labels:         []string{"prio-1"},
		IncludeDeleted: true,
	}}
	lm, cmd := lm.Update(runeKey('c'), km, api, sc)
	if lm.filter.Status != "" || lm.filter.Owner != "" || lm.filter.Search != "" ||
		len(lm.filter.Labels) != 0 {
		t.Fatalf("filters not cleared: %+v", lm.filter)
	}
	if !lm.filter.IncludeDeleted {
		t.Fatal("IncludeDeleted should be preserved across clear")
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

// TestList_NewIssue_NonEmptyTitlePosts: `n` then "fix bug" then Enter
// must call CreateIssue exactly once and (on success) seed the
// "created #N" status line + dispatch a refetch.
func TestList_NewIssue_NonEmptyTitlePosts(t *testing.T) {
	api := &fakeListAPI{
		createResult: &MutationResp{Issue: &Issue{Number: 42, Title: "fix bug"}},
	}
	km := newKeymap()
	sc := scope{projectID: 7}

	lm, _ := lmFromUpdate(listModel{actor: "tester"}, runeKey('n'), km, api, sc)
	for _, r := range "fix bug" {
		lm, _ = lmFromUpdate(lm, runeKey(r), km, api, sc)
	}
	lm, cmd := lmFromUpdate(lm, tea.KeyMsg{Type: tea.KeyEnter}, km, api, sc)
	if cmd == nil {
		t.Fatal("expected create command")
	}
	// drainCmd runs the create cmd, then feeds the mutationDoneMsg back
	// into Update so the status line and follow-up refetch are observed.
	lm = drainCmd(t, lm, cmd, km, api, sc)
	if api.createCalls != 1 {
		t.Fatalf("createCalls = %d, want 1", api.createCalls)
	}
	if api.lastCreateBody.Title != "fix bug" {
		t.Fatalf("title = %q, want %q", api.lastCreateBody.Title, "fix bug")
	}
	if api.lastCreateBody.Actor != "tester" {
		t.Fatalf("actor = %q, want %q", api.lastCreateBody.Actor, "tester")
	}
	if api.lastCreateProject != 7 {
		t.Fatalf("projectID = %d, want 7", api.lastCreateProject)
	}
	if lm.status != "created #42" {
		t.Fatalf("status = %q, want %q", lm.status, "created #42")
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
