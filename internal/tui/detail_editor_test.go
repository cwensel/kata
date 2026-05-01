package tui

import (
	"strings"
	"testing"
)

// TestDetail_EditBody_Dispatches: pressing 'e' returns a non-nil cmd.
// We don't actually launch $EDITOR — tea.ExecProcess defers the work
// to the runtime. The presence of a cmd is what wires the suspend.
func TestDetail_EditBody_Dispatches(t *testing.T) {
	api := &fakeDetailAPI{}
	km := newKeymap()
	dm := dmFixture()

	out, cmd := dm.Update(runeKey('e'), km, api)
	if cmd == nil {
		t.Fatal("expected non-nil cmd from e (editor suspend)")
	}
	if out.modal.active() {
		t.Fatal("e must not open a modal")
	}
	if api.editBodyCalls != 0 {
		t.Fatalf("editBodyCalls = %d, want 0 (no API call until editor returns)",
			api.editBodyCalls)
	}
}

// TestDetail_NewComment_Dispatches: pressing 'c' returns a non-nil cmd.
func TestDetail_NewComment_Dispatches(t *testing.T) {
	api := &fakeDetailAPI{}
	km := newKeymap()
	dm := dmFixture()

	out, cmd := dm.Update(runeKey('c'), km, api)
	if cmd == nil {
		t.Fatal("expected non-nil cmd from c (editor suspend)")
	}
	if out.modal.active() {
		t.Fatal("c must not open a modal")
	}
	if api.addCommentCalls != 0 {
		t.Fatalf("addCommentCalls = %d, want 0", api.addCommentCalls)
	}
}

// TestDetail_EditorReturned_BodyEdit_PostsAPI: feeding an editor-returned
// message with kind=edit and content="new body" calls api.EditBody once
// with that body.
func TestDetail_EditorReturned_BodyEdit_PostsAPI(t *testing.T) {
	api := &fakeDetailAPI{
		mutationResult: &MutationResp{Issue: &Issue{Number: 42}},
	}
	km := newKeymap()
	dm := dmFixture()

	out, cmd := dm.Update(editorReturnedMsg{kind: "edit", content: "new body"}, km, api)
	if cmd == nil {
		t.Fatal("expected dispatch cmd from edit returned")
	}
	_ = runDetailCmd(t, out, cmd, km, api)
	if api.editBodyCalls != 1 {
		t.Fatalf("editBodyCalls = %d, want 1", api.editBodyCalls)
	}
	if api.lastBody != "new body" {
		t.Fatalf("lastBody = %q, want %q", api.lastBody, "new body")
	}
	if api.lastActor != "tester" {
		t.Fatalf("lastActor = %q, want tester", api.lastActor)
	}
	if api.lastProjectID != 7 || api.lastNumber != 42 {
		t.Fatalf("edit args wrong: pid=%d num=%d",
			api.lastProjectID, api.lastNumber)
	}
}

// TestDetail_EditorReturned_BodyEdit_EmptyCancels: an empty (post-trim)
// content cancels the operation — no API call.
func TestDetail_EditorReturned_BodyEdit_EmptyCancels(t *testing.T) {
	api := &fakeDetailAPI{}
	km := newKeymap()
	dm := dmFixture()

	_, cmd := dm.Update(editorReturnedMsg{kind: "edit", content: ""}, km, api)
	if cmd != nil {
		t.Fatalf("expected nil cmd on empty content, got %T", cmd)
	}
	if api.editBodyCalls != 0 {
		t.Fatalf("editBodyCalls = %d, want 0", api.editBodyCalls)
	}
}

// TestDetail_EditorReturned_AddComment_PostsAPI mirrors the body-edit
// test for the comment path.
func TestDetail_EditorReturned_AddComment_PostsAPI(t *testing.T) {
	api := &fakeDetailAPI{
		mutationResult: &MutationResp{Issue: &Issue{Number: 42}},
	}
	km := newKeymap()
	dm := dmFixture()

	out, cmd := dm.Update(editorReturnedMsg{kind: "comment", content: "hi"}, km, api)
	if cmd == nil {
		t.Fatal("expected dispatch cmd from comment returned")
	}
	_ = runDetailCmd(t, out, cmd, km, api)
	if api.addCommentCalls != 1 {
		t.Fatalf("addCommentCalls = %d, want 1", api.addCommentCalls)
	}
	if api.lastBody != "hi" {
		t.Fatalf("lastBody = %q, want hi", api.lastBody)
	}
}

// TestDetail_EditorReturned_AddComment_EmptyCancels: empty content
// cancels for comments too.
func TestDetail_EditorReturned_AddComment_EmptyCancels(t *testing.T) {
	api := &fakeDetailAPI{}
	km := newKeymap()
	dm := dmFixture()

	_, cmd := dm.Update(editorReturnedMsg{kind: "comment", content: ""}, km, api)
	if cmd != nil {
		t.Fatalf("expected nil cmd on empty content, got %T", cmd)
	}
	if api.addCommentCalls != 0 {
		t.Fatalf("addCommentCalls = %d, want 0", api.addCommentCalls)
	}
}

// TestDetail_EditorReturned_TrimsComments: # lines in the buffer are
// stripped before the body hits the wire so the comment prompt doesn't
// leak into the issue.
func TestDetail_EditorReturned_TrimsComments(t *testing.T) {
	api := &fakeDetailAPI{
		mutationResult: &MutationResp{Issue: &Issue{Number: 42}},
	}
	km := newKeymap()
	dm := dmFixture()

	in := "hello\n# comment\nworld"
	out, cmd := dm.Update(editorReturnedMsg{kind: "comment", content: in}, km, api)
	if cmd == nil {
		t.Fatal("expected dispatch cmd")
	}
	_ = runDetailCmd(t, out, cmd, km, api)
	if api.addCommentCalls != 1 {
		t.Fatalf("addCommentCalls = %d, want 1", api.addCommentCalls)
	}
	if api.lastBody != "hello\nworld" {
		t.Fatalf("lastBody = %q, want %q", api.lastBody, "hello\nworld")
	}
}

// TestDetail_EditorReturned_Error_SurfacesStatus: an editor error (e.g.
// the user crashed vim) lands as a status hint and skips the API call.
func TestDetail_EditorReturned_Error_SurfacesStatus(t *testing.T) {
	api := &fakeDetailAPI{}
	km := newKeymap()
	dm := dmFixture()

	out, cmd := dm.Update(editorReturnedMsg{kind: "edit", err: errStub("boom")}, km, api)
	if cmd != nil {
		t.Fatalf("expected nil cmd on editor error, got %T", cmd)
	}
	if !strings.Contains(out.status, "boom") {
		t.Fatalf("status = %q, expected to contain editor error", out.status)
	}
	if api.editBodyCalls != 0 {
		t.Fatalf("editBodyCalls = %d, want 0", api.editBodyCalls)
	}
}

// TestDetail_EditBody_NoIssue_NoOp: pressing 'e' before the first fetch
// lands (issue==nil) is a quiet no-op rather than panicking.
func TestDetail_EditBody_NoIssue_NoOp(t *testing.T) {
	api := &fakeDetailAPI{}
	km := newKeymap()
	dm := detailModel{actor: "tester"}

	_, cmd := dm.Update(runeKey('e'), km, api)
	if cmd != nil {
		t.Fatalf("expected nil cmd when issue is nil, got %T", cmd)
	}
}

// errStub is a minimal error type for the editor-error test. We avoid
// importing errors here because the test would not need it.
type errStub string

func (e errStub) Error() string { return string(e) }
