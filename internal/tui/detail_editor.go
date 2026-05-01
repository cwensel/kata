package tui

import (
	"context"
	"time"

	tea "github.com/charmbracelet/bubbletea"
)

// handleEditorKey routes the keys that suspend the TUI to launch
// $EDITOR: 'e' edits the issue body, 'c' opens an empty buffer for a
// new comment. Returns ok=true when the key was consumed so the parent
// router can stop walking.
func (dm detailModel) handleEditorKey(
	msg tea.KeyMsg, km keymap,
) (detailModel, tea.Cmd, bool) {
	switch {
	case km.EditBody.matches(msg):
		if dm.issue == nil {
			return dm, nil, true
		}
		dm.status = ""
		return dm, editorCmd("edit", editorTemplate("edit", dm.issue.Body)), true
	case km.NewComment.matches(msg):
		if dm.issue == nil {
			return dm, nil, true
		}
		dm.status = ""
		return dm, editorCmd("comment", editorTemplate("comment", "")), true
	}
	return dm, nil, false
}

// applyEditorReturned routes an editorReturnedMsg back into a mutation
// dispatch. Empty content (after trimComments) cancels the operation
// without contacting the daemon. An editor exit error surfaces on the
// status line.
func (dm detailModel) applyEditorReturned(
	m editorReturnedMsg, api detailAPI,
) (detailModel, tea.Cmd) {
	if m.err != nil {
		dm.status = errorStyle.Render("editor: " + m.err.Error())
		return dm, nil
	}
	content := trimComments(m.content)
	if content == "" {
		return dm, nil
	}
	switch m.kind {
	case "edit":
		return dm, dm.dispatchEditBody(api, content)
	case "comment":
		return dm, dm.dispatchAddComment(api, content)
	}
	return dm, nil
}

// dispatchEditBody returns a tea.Cmd that calls api.EditBody and
// reports the result as a mutationDoneMsg{origin: "detail",
// kind: "body.edit"}. gen pins the response to the detail-open
// generation so a stale edit cannot apply to a newly-opened issue.
func (dm detailModel) dispatchEditBody(api detailAPI, body string) tea.Cmd {
	if dm.issue == nil {
		return nil
	}
	pid, num, actor, gen := dm.scopePID, dm.issue.Number, dm.actor, dm.gen
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		resp, err := api.EditBody(ctx, pid, num, body, actor)
		return mutationDoneMsg{
			origin: "detail", gen: gen, kind: "body.edit", resp: resp, err: err,
		}
	}
}

// dispatchAddComment posts a new comment and reports the result as a
// mutationDoneMsg{origin: "detail", kind: "comment.add"}.
func (dm detailModel) dispatchAddComment(api detailAPI, body string) tea.Cmd {
	if dm.issue == nil {
		return nil
	}
	pid, num, actor, gen := dm.scopePID, dm.issue.Number, dm.actor, dm.gen
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		resp, err := api.AddComment(ctx, pid, num, body, actor)
		return mutationDoneMsg{
			origin: "detail", gen: gen, kind: "comment.add", resp: resp, err: err,
		}
	}
}
