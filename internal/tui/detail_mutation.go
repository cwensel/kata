package tui

import (
	"context"
	"fmt"
	"strconv"
	"strings"
	"time"

	tea "github.com/charmbracelet/bubbletea"
)

// handleMutationKey dispatches the mutation bindings. Close/reopen and
// clear-owner fire immediately; the others open a modal and the actual
// mutation runs from handleModalKey on Enter. ok=true means the key was
// consumed.
func (dm detailModel) handleMutationKey(
	msg tea.KeyMsg, km keymap, api detailAPI,
) (detailModel, tea.Cmd, bool) {
	if next, cmd, ok := dm.handleStatusKey(msg, km, api); ok {
		return next, cmd, true
	}
	return dm.handleModalOpenKey(msg, km)
}

// handleStatusKey routes the keys that don't open a modal: close,
// reopen, clear-owner. Each fires a single mutation immediately.
func (dm detailModel) handleStatusKey(
	msg tea.KeyMsg, km keymap, api detailAPI,
) (detailModel, tea.Cmd, bool) {
	switch {
	case km.Close.matches(msg):
		return dm, dm.dispatchClose(api), true
	case km.Reopen.matches(msg):
		return dm, dm.dispatchReopen(api), true
	case km.ClearOwner.matches(msg):
		return dm, dm.dispatchAssign(api, ""), true
	}
	return dm, nil, false
}

// handleModalOpenKey routes the keys that need a text prompt before the
// mutation can fire. Opening a modal mutates dm.modal in place; the
// actual mutation goes through handleModalKey on Enter.
func (dm detailModel) handleModalOpenKey(
	msg tea.KeyMsg, km keymap,
) (detailModel, tea.Cmd, bool) {
	switch {
	case km.AddLabel.matches(msg):
		dm.modal = openModal(modalAddLabel)
	case km.RemoveLabel.matches(msg):
		dm.modal = openModal(modalRemoveLabel)
	case km.AssignOwner.matches(msg):
		dm.modal = openModal(modalAssignOwner)
	case km.SetParent.matches(msg):
		dm.modal = openModal(modalSetParent)
	case km.AddBlocker.matches(msg):
		dm.modal = openModal(modalAddBlocker)
	case km.AddLink.matches(msg):
		dm.modal = openModal(modalAddLink)
	default:
		return dm, nil, false
	}
	dm.status = ""
	return dm, nil, true
}

// handleModalKey routes keystrokes while a modal is open. Enter commits
// the buffer to the kind-specific dispatch; Esc cancels.
func (dm detailModel) handleModalKey(
	msg tea.KeyMsg, api detailAPI,
) (detailModel, tea.Cmd) {
	next, action := dm.modal.HandleKey(msg)
	dm.modal = next
	switch action {
	case modalCommit:
		return dm.commitModal(api)
	case modalCancel:
		dm.modal = modal{}
		return dm, nil
	}
	return dm, nil
}

// commitModal dispatches the mutation that the modal kind selected. The
// buffer is the user's input — empty buffers no-op so accidental Enter
// in a fresh modal doesn't churn the daemon.
func (dm detailModel) commitModal(api detailAPI) (detailModel, tea.Cmd) {
	kind := dm.modal.kind
	buf := strings.TrimSpace(dm.modal.buffer)
	dm.modal = modal{}
	if buf == "" {
		return dm, nil
	}
	return dm, dm.dispatchForKind(api, kind, buf)
}

// dispatchForKind routes the trimmed buffer through the right client
// method. Parse failures (a non-numeric "blocker #") surface as a status
// hint; the modal already closed so the user can retry.
func (dm detailModel) dispatchForKind(
	api detailAPI, kind modalKind, buf string,
) tea.Cmd {
	switch kind {
	case modalAddLabel:
		return dm.dispatchLabel(api, buf, true)
	case modalRemoveLabel:
		return dm.dispatchLabel(api, buf, false)
	case modalAssignOwner:
		return dm.dispatchAssign(api, buf)
	case modalSetParent:
		return dm.dispatchLink(api, "parent", buf)
	case modalAddBlocker:
		return dm.dispatchLink(api, "blocks", buf)
	case modalAddLink:
		return dm.dispatchAddLinkSyntax(api, buf)
	}
	return nil
}

// applyMutation handles mutationDoneMsg arriving back at the detail
// view. Success seeds a status hint and dispatches a single-issue
// refetch (so the body, comments, events, and links reflect the new
// state); failure surfaces an error toast in dm.status.
func (dm detailModel) applyMutation(
	m mutationDoneMsg, api detailAPI,
) (detailModel, tea.Cmd) {
	if m.err != nil {
		dm.err = m.err
		dm.status = errorStyle.Render(
			fmt.Sprintf("%s failed: %s", m.kind, m.err.Error()),
		)
		return dm, nil
	}
	dm.status = mutationSuccessText(m, dm.issue)
	return dm, dm.refetchAfterMutation(api)
}

// successTemplates maps mutation-kind to the printf template used by
// mutationSuccessText. Keeping the dispatch table-driven keeps the
// formatter at cyclomatic ≤8 and makes adding kinds (Task 11+) trivial.
var successTemplates = map[string]string{
	"close":        "closed #%d",
	"reopen":       "reopened #%d",
	"label.add":    "added label to #%d",
	"label.remove": "removed label from #%d",
	"owner.assign": "assigned #%d",
	"owner.clear":  "unassigned #%d",
	"link.parent":  "linked #%d",
	"link.blocks":  "linked #%d",
	"link.relates": "linked #%d",
	"body.edit":    "updated body of #%d",
	"comment.add":  "added comment to #%d",
}

// mutationSuccessText is the per-kind toast for a successful mutation.
// The issue number is read off dm.issue because the resp may not carry
// it (the daemon's mutation envelope embeds the issue, but the test
// fakes don't always populate that — and dm.issue is authoritative).
func mutationSuccessText(m mutationDoneMsg, iss *Issue) string {
	num := int64(0)
	if iss != nil {
		num = iss.Number
	}
	if m.resp != nil && m.resp.Issue != nil {
		num = m.resp.Issue.Number
	}
	if tpl, ok := successTemplates[m.kind]; ok {
		return fmt.Sprintf(tpl, num)
	}
	return ""
}

// refetchAfterMutation re-fetches the issue and the three tabs so the
// rendered detail reflects the new state without waiting for the SSE
// consumer (Task 11) to invalidate. The four fetches run in parallel
// via tea.Batch — the order they land doesn't matter because each
// fetch updates a distinct slice on dm.
func (dm detailModel) refetchAfterMutation(api detailAPI) tea.Cmd {
	if dm.issue == nil {
		return nil
	}
	pid := dm.scopePID
	num := dm.issue.Number
	return tea.Batch(
		fetchIssue(api, pid, num),
		fetchComments(api, pid, num),
		fetchEvents(api, pid, num),
		fetchLinks(api, pid, num),
	)
}

// dispatchClose returns a tea.Cmd that calls api.Close and reports the
// result via mutationDoneMsg. Returns nil if the issue isn't seeded.
func (dm detailModel) dispatchClose(api detailAPI) tea.Cmd {
	if dm.issue == nil {
		return nil
	}
	pid, num, actor := dm.scopePID, dm.issue.Number, dm.actor
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		resp, err := api.Close(ctx, pid, num, actor)
		return mutationDoneMsg{kind: "close", resp: resp, err: err}
	}
}

// dispatchReopen mirrors dispatchClose for the reopen action.
func (dm detailModel) dispatchReopen(api detailAPI) tea.Cmd {
	if dm.issue == nil {
		return nil
	}
	pid, num, actor := dm.scopePID, dm.issue.Number, dm.actor
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		resp, err := api.Reopen(ctx, pid, num, actor)
		return mutationDoneMsg{kind: "reopen", resp: resp, err: err}
	}
}

// dispatchLabel routes to AddLabel or RemoveLabel by add/!add.
func (dm detailModel) dispatchLabel(
	api detailAPI, label string, add bool,
) tea.Cmd {
	if dm.issue == nil {
		return nil
	}
	pid, num, actor := dm.scopePID, dm.issue.Number, dm.actor
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		var (
			resp *MutationResp
			err  error
			kind string
		)
		if add {
			resp, err = api.AddLabel(ctx, pid, num, label, actor)
			kind = "label.add"
		} else {
			resp, err = api.RemoveLabel(ctx, pid, num, label, actor)
			kind = "label.remove"
		}
		return mutationDoneMsg{kind: kind, resp: resp, err: err}
	}
}

// dispatchAssign calls Assign with the given owner. Empty owner is the
// clear case; the client routes that to /actions/unassign automatically.
func (dm detailModel) dispatchAssign(api detailAPI, owner string) tea.Cmd {
	if dm.issue == nil {
		return nil
	}
	pid, num, actor := dm.scopePID, dm.issue.Number, dm.actor
	kind := "owner.assign"
	if owner == "" {
		kind = "owner.clear"
	}
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		resp, err := api.Assign(ctx, pid, num, owner, actor)
		return mutationDoneMsg{kind: kind, resp: resp, err: err}
	}
}

// dispatchLink calls AddLink with the given type and a numeric target.
// Non-numeric input surfaces as an error in the status line — the
// daemon enforces the type vocabulary so we don't pre-validate it here.
func (dm detailModel) dispatchLink(
	api detailAPI, linkType, target string,
) tea.Cmd {
	if dm.issue == nil {
		return nil
	}
	to, err := strconv.ParseInt(strings.TrimSpace(target), 10, 64)
	if err != nil {
		return parseFailedCmd(linkType, target)
	}
	pid, num, actor := dm.scopePID, dm.issue.Number, dm.actor
	kind := "link." + linkType
	if linkType == "related" {
		kind = "link.relates"
	}
	body := LinkBody{Type: linkType, ToNumber: to}
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		resp, err := api.AddLink(ctx, pid, num, body, actor)
		return mutationDoneMsg{kind: kind, resp: resp, err: err}
	}
}

// dispatchAddLinkSyntax parses "kind number" out of buf. Empty kind or
// missing number surfaces a parse error via mutationDoneMsg so the
// status line gets it.
func (dm detailModel) dispatchAddLinkSyntax(
	api detailAPI, buf string,
) tea.Cmd {
	parts := strings.Fields(buf)
	if len(parts) != 2 {
		return parseFailedCmd("link", buf)
	}
	return dm.dispatchLink(api, parts[0], parts[1])
}

// parseFailedCmd surfaces a parse error as a synthetic mutationDoneMsg
// so the standard error-handling path renders the status line.
func parseFailedCmd(kind, input string) tea.Cmd {
	return func() tea.Msg {
		return mutationDoneMsg{
			kind: "link." + kind,
			err:  fmt.Errorf("parse %q failed: expected '<kind> <number>'", input),
		}
	}
}
