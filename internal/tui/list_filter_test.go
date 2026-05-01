package tui

import (
	"context"
	"errors"
	"strconv"
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
// Enter through Model.Update so the M3a inline command bar handles
// the keys. The buffer mirrors live into filter.Search on every
// keystroke; Enter closes the bar leaving the filter applied.
//
// The filter changes are *client-side* (filteredIssues), so no API
// refetch fires for Search/Owner — only Status filter changes
// dispatch a refetch.
func TestList_Search_AccumulatesAndCommits(t *testing.T) {
	m := mFixtureForBar()
	m, _ = stepModel(m, runeKey('/'))
	// Drive openInputMsg through the model so the bar opens.
	m = openBarFromCmd(t, m, '/')
	if m.input.kind != inputSearchBar {
		t.Fatalf("expected inputSearchBar active, got kind=%v", m.input.kind)
	}
	for _, r := range "abc" {
		m, _ = stepModel(m, runeKey(r))
	}
	if m.list.filter.Search != "abc" {
		t.Fatalf("filter.Search = %q, want abc (live mirror)", m.list.filter.Search)
	}
	m, _ = stepModel(m, tea.KeyMsg{Type: tea.KeyEnter})
	if m.input.kind != inputNone {
		t.Fatalf("input.kind = %v, want inputNone after Enter", m.input.kind)
	}
	if m.list.filter.Search != "abc" {
		t.Fatalf("filter.Search = %q, want abc (preserved on commit)", m.list.filter.Search)
	}
}

// TestList_Search_EscCancels confirms Esc reverts filter.Search to
// the pre-open snapshot and closes the bar.
//
// The bar pre-fills with the existing filter value so the user can
// refine an active search without retyping; appending "xyz" to a
// pre-filled "previous" produces "previousxyz" live, then Esc
// restores "previous".
func TestList_Search_EscCancels(t *testing.T) {
	m := mFixtureForBar()
	m.list.filter.Search = "previous"
	m = openBarFromCmd(t, m, '/')
	for _, r := range "xyz" {
		m, _ = stepModel(m, runeKey(r))
	}
	if m.list.filter.Search != "previousxyz" {
		t.Fatalf("filter.Search = %q, want previousxyz (live during edit)",
			m.list.filter.Search)
	}
	m, _ = stepModel(m, tea.KeyMsg{Type: tea.KeyEsc})
	if m.input.kind != inputNone {
		t.Fatal("input.kind must be inputNone after Esc")
	}
	if m.list.filter.Search != "previous" {
		t.Fatalf("filter.Search not restored: got %q, want %q",
			m.list.filter.Search, "previous")
	}
}

// mFixtureForBar builds a minimal Model with the bare-minimum state
// the M3a bar tests need: a list, a keymap, no api/sse goroutine, no
// scope. Used by every M3a-style test that drives Model.Update for
// search/owner bar behavior.
func mFixtureForBar() Model {
	return Model{
		view:   viewList,
		keymap: newKeymap(),
		list:   listModel{actor: "tester"},
		cache:  newIssueCache(),
	}
}

// stepModel is the test-side equivalent of dispatching one tea.Msg
// through Model.Update. Returns the new Model + any tea.Cmd the
// dispatch produced.
func stepModel(m Model, msg tea.Msg) (Model, tea.Cmd) {
	out, cmd := m.Update(msg)
	return out.(Model), cmd
}

// openBarFromCmd presses key, expects an openInputCmd to come back,
// invokes that cmd to obtain openInputMsg, and feeds the message
// back into Model.Update so the bar actually opens. Returns the
// resulting Model with the bar active.
func openBarFromCmd(t *testing.T, m Model, key rune) Model {
	t.Helper()
	out, cmd := m.Update(runeKey(key))
	m = out.(Model)
	if cmd == nil {
		t.Fatalf("press %q produced no cmd; expected openInputCmd", string(key))
	}
	msg := cmd()
	out, _ = m.Update(msg)
	return out.(Model)
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

// TestList_NewIssueRow_EmptyTitleDoesNotCallAPI: pressing `n` opens
// the M3.5c inline new-issue row at the top of the table. Pressing
// Enter on an empty buffer closes the row without calling
// api.CreateIssue.
func TestList_NewIssueRow_EmptyTitleDoesNotCallAPI(t *testing.T) {
	api := &fakeListAPI{}
	m := mFixtureForBar()
	m = openBarFromCmd(t, m, 'n')
	if m.input.kind != inputNewIssueRow {
		t.Fatalf("expected inputNewIssueRow, got %v", m.input.kind)
	}
	out, cmd := m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	nm := out.(Model)
	if cmd != nil {
		t.Fatalf("empty title must not dispatch a cmd, got %T", cmd)
	}
	if api.createCalls != 0 {
		t.Fatalf("createCalls = %d, want 0", api.createCalls)
	}
	if nm.input.kind != inputNone {
		t.Fatalf("row should close after empty Enter, got %v", nm.input.kind)
	}
}

// TestList_NewIssueRow_WhitespaceTitleDoesNotCallAPI: a buffer of
// only spaces/tabs trims to "" — same no-op path.
func TestList_NewIssueRow_WhitespaceTitleDoesNotCallAPI(t *testing.T) {
	api := &fakeListAPI{}
	m := mFixtureForBar()
	m.api = nil // force test path even though the empty branch returns nil cmd
	_ = api
	m = openBarFromCmd(t, m, 'n')
	for _, r := range "   \t  " {
		m, _ = stepModel(m, runeKey(r))
	}
	_, cmd := m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	if cmd != nil {
		t.Fatalf("whitespace-only title must not dispatch a cmd, got %T", cmd)
	}
}

// TestList_Cursor_MovesInFilteredSpace: with a filter active, j/k
// moves the cursor through filtered rows. Regression for finding 29:
// previously j moved through all issues and the marker landed on the
// wrong (sometimes invisible) row.
func TestList_Cursor_MovesInFilteredSpace(t *testing.T) {
	api := &fakeListAPI{}
	km := newKeymap()
	sc := scope{projectID: 7}
	lm := listModel{
		filter: ListFilter{Owner: "alice"},
		issues: []Issue{
			{Number: 1, Owner: ptrString("alice"), Title: "a"},
			{Number: 2, Owner: ptrString("bob"), Title: "b"},
			{Number: 3, Owner: ptrString("alice"), Title: "c"},
			{Number: 4, Owner: ptrString("bob"), Title: "d"},
		},
	}
	// Two filtered rows (1 and 3). j once → cursor=1 (the second
	// filtered row, #3). j again clamps at len(filtered)-1=1.
	lm, _ = lm.Update(runeKey('j'), km, api, sc)
	if lm.cursor != 1 {
		t.Fatalf("after j: cursor = %d, want 1", lm.cursor)
	}
	lm, _ = lm.Update(runeKey('j'), km, api, sc)
	if lm.cursor != 1 {
		t.Fatalf("after second j: cursor = %d, want 1 (clamped)", lm.cursor)
	}
	// targetRow must point at filtered[1] = issue #3.
	iss, ok := lm.targetRow()
	if !ok || iss.Number != 3 {
		t.Fatalf("targetRow = (%+v, %v), want #3", iss, ok)
	}
}

// TestList_NewIssueRow_TitleCreatesImmediately: typing a title and
// pressing Enter dispatches CreateIssue with that title and an
// empty body. M4 will chain a centered body form after success for
// optional refinement; M3.5c's contract is "create now, refine later
// or never."
func TestList_NewIssueRow_TitleCreatesImmediately(t *testing.T) {
	api := &fakeListAPI{
		createResult: &MutationResp{Issue: &Issue{Number: 42}},
	}
	m := mFixtureForBar()
	m.api = (*Client)(nil) // commitInput dispatches via m.list — needs scope only
	m.scope = scope{projectID: 7}
	m.list.actor = "tester"
	m = openBarFromCmd(t, m, 'n')
	for _, r := range "fix bug" {
		m, _ = stepModel(m, runeKey(r))
	}
	out, cmd := m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	nm := out.(Model)
	if cmd == nil {
		t.Fatal("expected create cmd from Enter on non-empty title")
	}
	if nm.input.kind != inputNone {
		t.Fatalf("row should close after Enter, got %v", nm.input.kind)
	}
	// Drive the cmd against the fake API to confirm the wire shape.
	// Model.list.dispatchCreateIssue captures the api param via
	// closure; we drive lm.dispatchCreateIssue directly here for the
	// API assertion since the Model.api is *Client (not detailAPI-
	// fitable). The behavior under test is dispatchCreateIssue's
	// title/body assembly.
	_, dispatchCmd := nm.list.dispatchCreateIssue(api, nm.scope, "fix bug")
	if dispatchCmd == nil {
		t.Fatal("expected dispatch cmd from non-empty title")
	}
	dispatchCmd()
	if api.createCalls != 1 {
		t.Fatalf("createCalls = %d, want 1", api.createCalls)
	}
	if api.lastCreateBody.Title != "fix bug" {
		t.Fatalf("title = %q, want %q", api.lastCreateBody.Title, "fix bug")
	}
	if api.lastCreateBody.Body != "" {
		t.Fatalf("body = %q, want empty (M3.5c contract)", api.lastCreateBody.Body)
	}
	if api.lastCreateBody.Actor != "tester" {
		t.Fatalf("actor = %q, want tester", api.lastCreateBody.Actor)
	}
}

// TestList_NewIssueRow_PreservesTitleWhitespace: leading/trailing
// whitespace the user typed reaches the wire untrimmed. dispatchCreateIssue
// only TrimSpace's for the emptiness gate.
func TestList_NewIssueRow_PreservesTitleWhitespace(t *testing.T) {
	api := &fakeListAPI{
		createResult: &MutationResp{Issue: &Issue{Number: 42}},
	}
	lm := listModel{actor: "tester"}
	_, cmd := lm.dispatchCreateIssue(api, scope{projectID: 7}, "  spaced title  ")
	if cmd == nil {
		t.Fatal("expected create cmd")
	}
	cmd()
	if api.lastCreateBody.Title != "  spaced title  " {
		t.Fatalf("title = %q, want %q (whitespace preserved)",
			api.lastCreateBody.Title, "  spaced title  ")
	}
}

// TestList_NewIssueRow_EscCancels: pressing Esc closes the row with
// no API call.
func TestList_NewIssueRow_EscCancels(t *testing.T) {
	api := &fakeListAPI{}
	m := mFixtureForBar()
	m = openBarFromCmd(t, m, 'n')
	for _, r := range "fix bug" {
		m, _ = stepModel(m, runeKey(r))
	}
	out, cmd := m.Update(tea.KeyMsg{Type: tea.KeyEsc})
	nm := out.(Model)
	if cmd != nil {
		t.Fatalf("Esc must not dispatch a cmd, got %T", cmd)
	}
	if nm.input.kind != inputNone {
		t.Fatal("Esc should close the new-issue row")
	}
	if api.createCalls != 0 {
		t.Fatalf("createCalls = %d, want 0", api.createCalls)
	}
}

// TestList_NewIssue_AllProjectsModeIsNoOp: in cross-project view there
// is no projectID to create against, so 'n' should not open the prompt
// and should leave a status hint.
func TestList_NewIssue_AllProjectsModeIsNoOp(t *testing.T) {
	api := &fakeListAPI{}
	km := newKeymap()
	sc := scope{allProjects: true}

	lm, cmd := lmFromUpdate(listModel{}, runeKey('n'), km, api, sc)
	if cmd != nil {
		t.Fatalf("expected no openInputCmd in all-projects mode, got %T", cmd)
	}
	if lm.status == "" {
		t.Fatal("expected status hint explaining the no-op")
	}
	if api.createCalls != 0 {
		t.Fatalf("createCalls = %d, want 0", api.createCalls)
	}
}

// TestList_NewIssueRow_RendersFromTopOfList: opening the new-issue
// row when the cursor is mid-list must still show the create row
// above the freshest issues at index 0, not above an arbitrary middle
// slice. Recency-sorted lists put the soon-to-be-created peer at the
// top; the user expects to see what their new issue will sit next to.
// Regression for roborev #113 finding 1.
func TestList_NewIssueRow_RendersFromTopOfList(t *testing.T) {
	issues := make([]Issue, 30)
	for i := range issues {
		issues[i] = Issue{
			Number: int64(i + 1),
			Title:  "issue " + strconv.Itoa(i+1),
			Status: "open",
		}
	}
	lm := listModel{issues: issues, cursor: 20}
	in := newNewIssueRow()
	out := lm.renderBodyWithNewIssueRow(120, 12, in)
	// Top-of-list anchoring means issue #1 must appear in the rendered
	// output even though cursor=20. If windowing still followed cursor,
	// the slice would be issues 11..20-ish and #1 would be off-screen.
	if !strings.Contains(out, "issue 1 ") {
		t.Fatalf("expected 'issue 1' in rendered window when new-issue row is open; got:\n%s", out)
	}
}

// TestList_NewIssueRow_ScrollIndicatorMatchesAnchoredWindow: the
// info-line scroll indicator must use the same top-anchored window
// as the rendered body when the inline new-issue row is open. With
// cursor mid-list, the body anchors at index 0; the indicator must
// report `[1-K of N]` (top slice) instead of a middle slice. The
// budget shrinks by 1 to account for the synthetic row taking the
// first data slot. Regression for roborev #121.
func TestList_NewIssueRow_ScrollIndicatorMatchesAnchoredWindow(t *testing.T) {
	issues := make([]Issue, 30)
	for i := range issues {
		issues[i] = Issue{Number: int64(i + 1), Title: "x", Status: "open"}
	}
	lm := listModel{issues: issues, cursor: 20}
	chrome := viewChrome{
		sseStatus: sseConnected,
		input:     newNewIssueRow(),
	}
	info := renderListInfoLine(120, chrome, lm, 10)
	// Anchored at top with budget 10-1=9 visible: window must be [1-9].
	if !strings.Contains(info, "[1-9 of 30]") {
		t.Fatalf("indicator mismatch; want [1-9 of 30], got %q", info)
	}
}

// TestList_NewIssueRow_CommitSeedsSelectionToNewIssue: after a
// successful create, the next refetch must land the cursor on the
// newly-created issue (which lands at index 0 in a recency-sorted
// list) instead of snapping back to whatever was selected before.
// Regression for roborev #113 finding 2.
func TestList_NewIssueRow_CommitSeedsSelectionToNewIssue(t *testing.T) {
	api := &fakeListAPI{}
	lm := listModel{
		issues:         []Issue{{Number: 5}, {Number: 4}, {Number: 3}},
		cursor:         2,
		selectedNumber: 3,
	}
	mut := mutationDoneMsg{
		origin: "list", kind: "create",
		resp: &MutationResp{Issue: &Issue{Number: 99}},
	}
	out, _ := lm.applyMutation(mut, api, scope{projectID: 7})
	if out.selectedNumber != 99 {
		t.Fatalf("selectedNumber = %d, want 99 (seeded to new issue)",
			out.selectedNumber)
	}
	if out.cursor != 0 {
		t.Fatalf("cursor = %d, want 0 (new issue at top of recency list)",
			out.cursor)
	}
}

// TestList_OwnerPrompt_AccumulatesAndCommits drives `o`, "claude",
// Enter through the M3a inline command bar. Owner filter is
// client-side so no API refetch fires; the bar mirrors live into
// filter.Owner each keystroke.
func TestList_OwnerPrompt_AccumulatesAndCommits(t *testing.T) {
	m := mFixtureForBar()
	m = openBarFromCmd(t, m, 'o')
	if m.input.kind != inputOwnerBar {
		t.Fatalf("expected inputOwnerBar active, got kind=%v", m.input.kind)
	}
	for _, r := range "claude" {
		m, _ = stepModel(m, runeKey(r))
	}
	if m.list.filter.Owner != "claude" {
		t.Fatalf("filter.Owner = %q, want claude (live mirror)", m.list.filter.Owner)
	}
	m, _ = stepModel(m, tea.KeyMsg{Type: tea.KeyEnter})
	if m.input.kind != inputNone {
		t.Fatal("input.kind must be inputNone after Enter commit")
	}
	if m.list.filter.Owner != "claude" {
		t.Fatalf("filter.Owner = %q, want claude (preserved on commit)", m.list.filter.Owner)
	}
}

// TestList_LabelKey_NoLongerOpensPrompt: pressing 'l' from the list
// must NOT open any input shell. The label-filter UI was retired
// because the wire doesn't carry Labels yet (matchesFilter could not
// honor it). Regression catch for accidentally rebinding 'l' before
// the wire surface lands.
func TestList_LabelKey_NoLongerOpensPrompt(t *testing.T) {
	m := mFixtureForBar()
	m, _ = stepModel(m, runeKey('l'))
	if m.input.kind != inputNone {
		t.Fatalf("'l' opened input shell: kind=%v", m.input.kind)
	}
}

// TestList_BackspaceTrimsBuffer: backspace inside the active inline
// command bar deletes the last rune. The bubbles textinput handles
// the actual edit; Model.routeInputKey forwards the key through
// inputState.Update.
func TestList_BackspaceTrimsBuffer(t *testing.T) {
	m := mFixtureForBar()
	m = openBarFromCmd(t, m, '/')
	for _, r := range "abc" {
		m, _ = stepModel(m, runeKey(r))
	}
	m, _ = stepModel(m, tea.KeyMsg{Type: tea.KeyBackspace})
	if got := m.input.activeField().value(); got != "ab" {
		t.Fatalf("bar value = %q, want ab", got)
	}
	if m.list.filter.Search != "ab" {
		t.Fatalf("filter.Search = %q, want ab (mirrored after backspace)", m.list.filter.Search)
	}
}

// TestList_QuitGate_RoutesQuitToBuffer covers the model-level gate: a
// 'q' keystroke while the inline command bar is open must reach the
// bar's buffer instead of triggering tea.Quit. After M3a, the bar
// lives on m.input — canQuit() returns false when m.input.kind !=
// inputNone so routeGlobalKey doesn't match.
func TestList_QuitGate_RoutesQuitToBuffer(t *testing.T) {
	m := initialModel(Options{})
	m.scope = scope{projectID: 7}
	m.list.loading = false
	m = openBarFromCmd(t, m, '/')
	if m.input.kind != inputSearchBar {
		t.Fatalf("bar did not open, got kind=%v", m.input.kind)
	}
	out, cmd := m.Update(runeKey('q'))
	m = out.(Model)
	if cmd != nil {
		if msg := cmd(); msg != nil {
			if _, isQuit := msg.(tea.QuitMsg); isQuit {
				t.Fatalf("q produced tea.Quit; should have reached the bar buffer")
			}
		}
	}
	if got := m.input.activeField().value(); got != "q" {
		t.Fatalf("bar buffer = %q, want q (q routed to input)", got)
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
