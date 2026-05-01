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
// reports the result as a mutationDoneMsg{kind: "body.edit"}.
func (dm detailModel) dispatchEditBody(api detailAPI, body string) tea.Cmd {
	if dm.issue == nil {
		return nil
	}
	pid, num, actor := dm.scopePID, dm.issue.Number, dm.actor
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		resp, err := api.EditBody(ctx, pid, num, body, actor)
		return mutationDoneMsg{kind: "body.edit", resp: resp, err: err}
	}
}

// dispatchAddComment posts a new comment and reports the result as a
// mutationDoneMsg{kind: "comment.add"}.
func (dm detailModel) dispatchAddComment(api detailAPI, body string) tea.Cmd {
	if dm.issue == nil {
		return nil
	}
	pid, num, actor := dm.scopePID, dm.issue.Number, dm.actor
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		resp, err := api.AddComment(ctx, pid, num, body, actor)
		return mutationDoneMsg{kind: "comment.add", resp: resp, err: err}
	}
}
