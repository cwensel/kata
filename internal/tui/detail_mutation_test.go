package tui

import (
	"errors"
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
)

// dmFixture seeds a minimal detailModel suitable for mutation tests:
// a single issue and a known scope/actor so the asserts are direct.
// No comments/events/links; the mutation paths don't touch those.
func dmFixture() detailModel {
	iss := Issue{ProjectID: 7, Number: 42, Title: "fix bug", Status: "open"}
	return detailModel{issue: &iss, scopePID: 7, actor: "tester"}
}

// runDetailCmd executes the tea.Cmd returned by Update once and feeds
// the resulting message back into Update so the test sees the post-
// fetch (or post-mutation-result) state. Returns the second-pass model.
// Some commands return tea.BatchMsg; those are handled by enumerating
// the children and dispatching only the first non-nil message back.
func runDetailCmd(
	t *testing.T, dm detailModel, cmd tea.Cmd, km keymap, api detailAPI,
) detailModel {
	t.Helper()
	if cmd == nil {
		return dm
	}
	msg := cmd()
	out, _ := dm.Update(msg, km, api)
	return out
}

// typeRunes feeds each rune of s through dm.Update with the modal-key
// path so the modal buffer accumulates as if the user typed live.
func typeRunes(
	t *testing.T, dm detailModel, s string, km keymap, api detailAPI,
) detailModel {
	t.Helper()
	for _, r := range s {
		dm, _ = dm.Update(runeKey(r), km, api)
	}
	return dm
}

// TestDetail_Close_DispatchesAPI: pressing 'x' calls api.Close exactly
// once with the fixture's projectID, number, and actor.
func TestDetail_Close_DispatchesAPI(t *testing.T) {
	api := &fakeDetailAPI{
		mutationResult: &MutationResp{Issue: &Issue{Number: 42, Status: "closed"}},
	}
	km := newKeymap()
	dm := dmFixture()

	out, cmd := dm.Update(runeKey('x'), km, api)
	if cmd == nil {
		t.Fatal("expected close cmd from x")
	}
	_ = runDetailCmd(t, out, cmd, km, api)
	if api.closeCalls != 1 {
		t.Fatalf("closeCalls = %d, want 1", api.closeCalls)
	}
	if api.lastProjectID != 7 || api.lastNumber != 42 || api.lastActor != "tester" {
		t.Fatalf("close args wrong: pid=%d num=%d actor=%q",
			api.lastProjectID, api.lastNumber, api.lastActor)
	}
}

// TestDetail_Reopen_DispatchesAPI: pressing 'r' calls api.Reopen.
func TestDetail_Reopen_DispatchesAPI(t *testing.T) {
	api := &fakeDetailAPI{
		mutationResult: &MutationResp{Issue: &Issue{Number: 42, Status: "open"}},
	}
	km := newKeymap()
	dm := dmFixture()

	out, cmd := dm.Update(runeKey('r'), km, api)
	if cmd == nil {
		t.Fatal("expected reopen cmd from r")
	}
	_ = runDetailCmd(t, out, cmd, km, api)
	if api.reopenCalls != 1 {
		t.Fatalf("reopenCalls = %d, want 1", api.reopenCalls)
	}
	if api.lastProjectID != 7 || api.lastNumber != 42 || api.lastActor != "tester" {
		t.Fatalf("reopen args wrong: pid=%d num=%d actor=%q",
			api.lastProjectID, api.lastNumber, api.lastActor)
	}
}

// TestDetail_AddLabel_OpensModal: '+' opens modalAddLabel; no API call
// should happen until Enter commits the buffer.
func TestDetail_AddLabel_OpensModal(t *testing.T) {
	api := &fakeDetailAPI{}
	km := newKeymap()
	dm := dmFixture()

	dm, cmd := dm.Update(runeKey('+'), km, api)
	if cmd != nil {
		t.Fatalf("opening modal should not dispatch a cmd, got %T", cmd)
	}
	if !dm.modal.active() {
		t.Fatal("modal not active after +")
	}
	if dm.modal.kind != modalAddLabel {
		t.Fatalf("modal.kind = %d, want modalAddLabel (%d)", dm.modal.kind, modalAddLabel)
	}
	if api.addLabelCalls != 0 {
		t.Fatalf("addLabelCalls = %d, want 0 (no commit yet)", api.addLabelCalls)
	}
}

// TestDetail_AddLabel_CommitCallsAPI: '+' then "bug" then Enter calls
// api.AddLabel("bug", "tester").
func TestDetail_AddLabel_CommitCallsAPI(t *testing.T) {
	api := &fakeDetailAPI{
		mutationResult: &MutationResp{Issue: &Issue{Number: 42}},
	}
	km := newKeymap()
	dm := dmFixture()

	dm, _ = dm.Update(runeKey('+'), km, api)
	dm = typeRunes(t, dm, "bug", km, api)
	if dm.modal.buffer != "bug" {
		t.Fatalf("modal.buffer = %q, want bug", dm.modal.buffer)
	}
	out, cmd := dm.Update(tea.KeyMsg{Type: tea.KeyEnter}, km, api)
	if cmd == nil {
		t.Fatal("expected dispatch cmd on Enter")
	}
	if out.modal.active() {
		t.Fatal("modal should close on commit")
	}
	_ = runDetailCmd(t, out, cmd, km, api)
	if api.addLabelCalls != 1 {
		t.Fatalf("addLabelCalls = %d, want 1", api.addLabelCalls)
	}
	if api.lastLabel != "bug" {
		t.Fatalf("lastLabel = %q, want bug", api.lastLabel)
	}
	if api.lastActor != "tester" {
		t.Fatalf("lastActor = %q, want tester", api.lastActor)
	}
}

// TestDetail_AddLabel_EscCancels: '+' then "bug" then Esc dispatches no
// API call and clears the modal.
func TestDetail_AddLabel_EscCancels(t *testing.T) {
	api := &fakeDetailAPI{}
	km := newKeymap()
	dm := dmFixture()

	dm, _ = dm.Update(runeKey('+'), km, api)
	dm = typeRunes(t, dm, "bug", km, api)
	out, cmd := dm.Update(tea.KeyMsg{Type: tea.KeyEsc}, km, api)
	if cmd != nil {
		t.Fatalf("Esc must not dispatch a cmd, got %T", cmd)
	}
	if out.modal.active() {
		t.Fatal("modal must close on Esc")
	}
	if api.addLabelCalls != 0 {
		t.Fatalf("addLabelCalls = %d, want 0", api.addLabelCalls)
	}
}

// TestDetail_RemoveLabel_CommitCallsAPI: '-' then "bug" then Enter calls
// api.RemoveLabel("bug", "tester").
func TestDetail_RemoveLabel_CommitCallsAPI(t *testing.T) {
	api := &fakeDetailAPI{
		mutationResult: &MutationResp{Issue: &Issue{Number: 42}},
	}
	km := newKeymap()
	dm := dmFixture()

	dm, _ = dm.Update(runeKey('-'), km, api)
	if dm.modal.kind != modalRemoveLabel {
		t.Fatalf("modal.kind = %d, want modalRemoveLabel", dm.modal.kind)
	}
	dm = typeRunes(t, dm, "bug", km, api)
	out, cmd := dm.Update(tea.KeyMsg{Type: tea.KeyEnter}, km, api)
	if cmd == nil {
		t.Fatal("expected dispatch cmd on Enter")
	}
	_ = runDetailCmd(t, out, cmd, km, api)
	if api.removeLabelCalls != 1 {
		t.Fatalf("removeLabelCalls = %d, want 1", api.removeLabelCalls)
	}
	if api.lastLabel != "bug" {
		t.Fatalf("lastLabel = %q, want bug", api.lastLabel)
	}
}

// TestDetail_AssignOwner_CommitCallsAPI: 'a' then "alice" then Enter
// calls api.Assign("alice", "tester").
func TestDetail_AssignOwner_CommitCallsAPI(t *testing.T) {
	api := &fakeDetailAPI{
		mutationResult: &MutationResp{Issue: &Issue{Number: 42}},
	}
	km := newKeymap()
	dm := dmFixture()

	dm, _ = dm.Update(runeKey('a'), km, api)
	if dm.modal.kind != modalAssignOwner {
		t.Fatalf("modal.kind = %d, want modalAssignOwner", dm.modal.kind)
	}
	dm = typeRunes(t, dm, "alice", km, api)
	out, cmd := dm.Update(tea.KeyMsg{Type: tea.KeyEnter}, km, api)
	if cmd == nil {
		t.Fatal("expected assign cmd on Enter")
	}
	_ = runDetailCmd(t, out, cmd, km, api)
	if api.assignCalls != 1 {
		t.Fatalf("assignCalls = %d, want 1", api.assignCalls)
	}
	if api.lastOwner != "alice" {
		t.Fatalf("lastOwner = %q, want alice", api.lastOwner)
	}
	if api.lastActor != "tester" {
		t.Fatalf("lastActor = %q, want tester", api.lastActor)
	}
}

// TestDetail_ClearOwner_DispatchesAPI: 'A' immediately calls
// api.Assign("", "tester") with no modal.
func TestDetail_ClearOwner_DispatchesAPI(t *testing.T) {
	api := &fakeDetailAPI{
		mutationResult: &MutationResp{Issue: &Issue{Number: 42}},
	}
	km := newKeymap()
	dm := dmFixture()

	out, cmd := dm.Update(runeKey('A'), km, api)
	if cmd == nil {
		t.Fatal("expected clear cmd from A")
	}
	if out.modal.active() {
		t.Fatal("clear should not open a modal")
	}
	_ = runDetailCmd(t, out, cmd, km, api)
	if api.assignCalls != 1 {
		t.Fatalf("assignCalls = %d, want 1", api.assignCalls)
	}
	if api.lastOwner != "" {
		t.Fatalf("lastOwner = %q, want empty (clear)", api.lastOwner)
	}
}

// TestDetail_AddLink_Parent: 'p' opens modalSetParent; "42" + Enter
// calls api.AddLink({Type: parent, ToNumber: 42}, "tester").
func TestDetail_AddLink_Parent(t *testing.T) {
	api := &fakeDetailAPI{
		mutationResult: &MutationResp{Issue: &Issue{Number: 42}},
	}
	km := newKeymap()
	dm := dmFixture()

	dm, _ = dm.Update(runeKey('p'), km, api)
	if dm.modal.kind != modalSetParent {
		t.Fatalf("modal.kind = %d, want modalSetParent", dm.modal.kind)
	}
	dm = typeRunes(t, dm, "42", km, api)
	out, cmd := dm.Update(tea.KeyMsg{Type: tea.KeyEnter}, km, api)
	if cmd == nil {
		t.Fatal("expected addlink cmd on Enter")
	}
	_ = runDetailCmd(t, out, cmd, km, api)
	if api.addLinkCalls != 1 {
		t.Fatalf("addLinkCalls = %d, want 1", api.addLinkCalls)
	}
	if api.lastLinkBody.Type != "parent" || api.lastLinkBody.ToNumber != 42 {
		t.Fatalf("lastLinkBody = %+v, want {parent 42}", api.lastLinkBody)
	}
}

// TestDetail_AddLink_Blocks: 'b' opens modalAddBlocker; "5" + Enter
// calls api.AddLink({Type: blocks, ToNumber: 5}, "tester").
func TestDetail_AddLink_Blocks(t *testing.T) {
	api := &fakeDetailAPI{
		mutationResult: &MutationResp{Issue: &Issue{Number: 42}},
	}
	km := newKeymap()
	dm := dmFixture()

	dm, _ = dm.Update(runeKey('b'), km, api)
	if dm.modal.kind != modalAddBlocker {
		t.Fatalf("modal.kind = %d, want modalAddBlocker", dm.modal.kind)
	}
	dm = typeRunes(t, dm, "5", km, api)
	out, cmd := dm.Update(tea.KeyMsg{Type: tea.KeyEnter}, km, api)
	if cmd == nil {
		t.Fatal("expected addlink cmd on Enter")
	}
	_ = runDetailCmd(t, out, cmd, km, api)
	if api.addLinkCalls != 1 {
		t.Fatalf("addLinkCalls = %d, want 1", api.addLinkCalls)
	}
	if api.lastLinkBody.Type != "blocks" || api.lastLinkBody.ToNumber != 5 {
		t.Fatalf("lastLinkBody = %+v, want {blocks 5}", api.lastLinkBody)
	}
}

// TestDetail_AddLink_Other: 'L' opens modalAddLink; "relates_to 7" +
// Enter parses as <kind> <number> and calls AddLink. The implementer's
// dispatchAddLinkSyntax splits on whitespace; whatever the first token is
// gets passed verbatim as Type to AddLink (the daemon enforces the
// allowed vocabulary).
func TestDetail_AddLink_Other(t *testing.T) {
	api := &fakeDetailAPI{
		mutationResult: &MutationResp{Issue: &Issue{Number: 42}},
	}
	km := newKeymap()
	dm := dmFixture()

	dm, _ = dm.Update(runeKey('L'), km, api)
	if dm.modal.kind != modalAddLink {
		t.Fatalf("modal.kind = %d, want modalAddLink", dm.modal.kind)
	}
	dm = typeRunes(t, dm, "relates_to 7", km, api)
	out, cmd := dm.Update(tea.KeyMsg{Type: tea.KeyEnter}, km, api)
	if cmd == nil {
		t.Fatal("expected addlink cmd on Enter")
	}
	_ = runDetailCmd(t, out, cmd, km, api)
	if api.addLinkCalls != 1 {
		t.Fatalf("addLinkCalls = %d, want 1", api.addLinkCalls)
	}
	if api.lastLinkBody.Type != "relates_to" || api.lastLinkBody.ToNumber != 7 {
		t.Fatalf("lastLinkBody = %+v, want {relates_to 7}", api.lastLinkBody)
	}
}

// TestDetail_AddLink_OtherParseFailure: a single-token buffer "noop"
// should not call api.AddLink and should surface a parse-failed status.
func TestDetail_AddLink_OtherParseFailure(t *testing.T) {
	api := &fakeDetailAPI{}
	km := newKeymap()
	dm := dmFixture()

	dm, _ = dm.Update(runeKey('L'), km, api)
	dm = typeRunes(t, dm, "noop", km, api)
	out, cmd := dm.Update(tea.KeyMsg{Type: tea.KeyEnter}, km, api)
	if cmd == nil {
		t.Fatal("expected synthetic-error cmd on Enter")
	}
	out = runDetailCmd(t, out, cmd, km, api)
	if api.addLinkCalls != 0 {
		t.Fatalf("addLinkCalls = %d, want 0 (parse failure path)", api.addLinkCalls)
	}
	if !strings.Contains(out.status, "failed") {
		t.Fatalf("status = %q, expected failure hint", out.status)
	}
}

// TestDetail_MutationError_SurfacesStatus: when the fake returns an
// *APIError, the resulting status line includes "failed" and the
// error's Code/Message.
func TestDetail_MutationError_SurfacesStatus(t *testing.T) {
	api := &fakeDetailAPI{
		mutationErr: &APIError{Code: "validation_error", Message: "bad label"},
	}
	km := newKeymap()
	dm := dmFixture()

	out, cmd := dm.Update(runeKey('x'), km, api)
	if cmd == nil {
		t.Fatal("expected close cmd")
	}
	out = runDetailCmd(t, out, cmd, km, api)
	if !strings.Contains(out.status, "failed") {
		t.Fatalf("status = %q, expected to contain 'failed'", out.status)
	}
	if !strings.Contains(out.status, "validation_error") {
		t.Fatalf("status = %q, expected to contain error code", out.status)
	}
}

// TestDetail_MutationError_PlainError: a plain error.New result still
// reaches the status line so non-typed daemons (or wrapped errors) are
// reported.
func TestDetail_MutationError_PlainError(t *testing.T) {
	api := &fakeDetailAPI{mutationErr: errors.New("boom")}
	km := newKeymap()
	dm := dmFixture()

	out, cmd := dm.Update(runeKey('x'), km, api)
	out = runDetailCmd(t, out, cmd, km, api)
	if !strings.Contains(out.status, "boom") {
		t.Fatalf("status = %q, expected to contain 'boom'", out.status)
	}
}

// TestDetail_MutationSuccess_DispatchesRefetch: after a successful
// mutation, the returned tea.Cmd is a Batch that runs four fetches
// (issue, comments, events, links). We assert at least the GetIssue
// call landed by inspecting api.lastGetIssue after running the batch.
func TestDetail_MutationSuccess_DispatchesRefetch(t *testing.T) {
	api := &fakeDetailAPI{
		mutationResult: &MutationResp{Issue: &Issue{Number: 42}},
		getIssueResult: &Issue{Number: 42, Status: "closed"},
	}
	km := newKeymap()
	dm := dmFixture()

	out, cmd := dm.Update(runeKey('x'), km, api)
	if cmd == nil {
		t.Fatal("expected close cmd")
	}
	doneMsg := cmd()
	out, refetch := out.Update(doneMsg, km, api)
	if refetch == nil {
		t.Fatal("expected refetch cmd after success")
	}
	if !strings.Contains(out.status, "closed #42") {
		t.Fatalf("status = %q, expected 'closed #42'", out.status)
	}
	runBatch(refetch)
	if api.lastGetIssue != 42 {
		t.Fatalf("api.lastGetIssue = %d, want 42 (refetch should have run)",
			api.lastGetIssue)
	}
}

// TestDetail_QuitGate_RoutesToBuffer: with a detail modal open, 'q'
// must reach the modal buffer instead of triggering tea.Quit.
func TestDetail_QuitGate_RoutesToBuffer(t *testing.T) {
	m := initialModel(Options{})
	m.scope = scope{projectID: 7}
	m.list.loading = false
	iss := Issue{ProjectID: 7, Number: 42, Title: "fix bug", Status: "open"}
	m.detail.issue = &iss
	m.detail.scopePID = 7
	m.detail.actor = "tester"
	m.view = viewDetail

	out, _ := m.Update(runeKey('+'))
	m = out.(Model)
	if !m.detail.modal.active() {
		t.Fatal("modal did not open on +")
	}
	out, cmd := m.Update(runeKey('q'))
	m = out.(Model)
	if cmd != nil {
		t.Fatalf("expected no command (q must reach modal buffer), got %T", cmd)
	}
	if m.detail.modal.buffer != "q" {
		t.Fatalf("modal.buffer = %q, want q", m.detail.modal.buffer)
	}
}

// TestDetail_EmptyBufferEnter_NoOp: opening a modal and pressing Enter
// without typing must close the modal without dispatching an API call.
func TestDetail_EmptyBufferEnter_NoOp(t *testing.T) {
	api := &fakeDetailAPI{}
	km := newKeymap()
	dm := dmFixture()

	dm, _ = dm.Update(runeKey('+'), km, api)
	out, cmd := dm.Update(tea.KeyMsg{Type: tea.KeyEnter}, km, api)
	if cmd != nil {
		t.Fatalf("empty-buffer Enter must return nil cmd, got %T", cmd)
	}
	if out.modal.active() {
		t.Fatal("modal must close on empty Enter")
	}
	if api.addLabelCalls != 0 {
		t.Fatalf("addLabelCalls = %d, want 0", api.addLabelCalls)
	}
}

// TestDetail_NoIssue_NoDispatch: if dm.issue is nil (e.g. boot before
// the first fetch lands), pressing 'x' must be a quiet no-op rather
// than panicking on a nil-deref.
func TestDetail_NoIssue_NoDispatch(t *testing.T) {
	api := &fakeDetailAPI{}
	km := newKeymap()
	dm := detailModel{actor: "tester"}

	out, cmd := dm.Update(runeKey('x'), km, api)
	if cmd != nil {
		t.Fatalf("expected nil cmd when issue is nil, got %T", cmd)
	}
	if out.modal.active() {
		t.Fatal("no modal should open from x")
	}
	if api.closeCalls != 0 {
		t.Fatalf("closeCalls = %d, want 0", api.closeCalls)
	}
}
