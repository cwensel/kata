package tui

import (
	"context"
	"fmt"
	"strings"
	"time"

	tea "github.com/charmbracelet/bubbletea"
)

// listAPI is the subset of *Client the list-view Update path needs.
// Defining it as an interface (instead of taking *Client directly) lets
// list_filter_test.go drive Update with a fake without standing up a
// httptest server — the client tests already cover the wire format.
type listAPI interface {
	ListIssues(ctx context.Context, projectID int64, f ListFilter) ([]Issue, error)
	ListAllIssues(ctx context.Context, f ListFilter) ([]Issue, error)
	CreateIssue(
		ctx context.Context, projectID int64, body CreateIssueBody,
	) (*MutationResp, error)
	Close(ctx context.Context, projectID, number int64, actor string) (*MutationResp, error)
	Reopen(ctx context.Context, projectID, number int64, actor string) (*MutationResp, error)
}

// listModel owns list-view state: the current rows, cursor, the
// filter in effect, the resolved actor for mutations, and a one-shot
// status line that the View renders inside the chrome's info-line
// slot. The keymap lives on the parent Model and is passed into
// Update; one instance keeps the help view in lockstep with what
// handlers actually do.
//
// selectedNumber tracks the issue.Number under the cursor for
// identity-based selection: when a refetch reorders rows (issues
// are sorted by updated_at DESC, so any background mutation can
// shuffle them), the cursor is restored onto the same issue rather
// than the same index. Zero means "no selection" (empty list,
// pre-fetch state).
//
// M3.5c retired lm.search and lm.pendingTitle: the inline command
// bar (M3a) covers search/owner; the inline new-issue row (M3.5c)
// covers `n`. All input flows now live on Model.input.
type listModel struct {
	issues         []Issue
	cursor         int
	selectedNumber int64
	filter         ListFilter
	actor          string
	status         string
	err            error
	loading        bool
}

// newListModel returns a listModel waiting for its first fetch. loading=true
// keeps the spinner-equivalent on screen until initialFetchMsg lands.
func newListModel() listModel {
	return listModel{loading: true}
}

// Update handles list-view keys and fetch results. The top-level
// Model keeps responsibility for global keys (q, ?, R), input shells
// (Model.input), modals (Model.modal), and SSE messages.
//
// initialFetchMsg/refetchedMsg are also applied by Model.populateCache
// before dispatch (so help/detail-overlay refetches keep lm.issues in
// sync); applyFetched is idempotent so re-applying here for the
// viewList path — and from drainCmd-style tests that drive lm.Update
// directly — is harmless.
func (lm listModel) Update(
	msg tea.Msg, km keymap, api listAPI, sc scope,
) (listModel, tea.Cmd) {
	switch m := msg.(type) {
	case tea.KeyMsg:
		return lm.applyNavKey(m, km, api, sc)
	case initialFetchMsg, refetchedMsg:
		lm = lm.applyFetched(m)
	case mutationDoneMsg:
		return lm.applyMutation(m, api, sc)
	}
	return lm, nil
}

// applyNavKey routes a top-level keystroke through the cursor, filter,
// and prompt handlers. It returns early when a sub-handler reports it
// has consumed the key so the cyclomatic budget per function stays
// inside the project's ≤8 limit.
func (lm listModel) applyNavKey(
	msg tea.KeyMsg, km keymap, api listAPI, sc scope,
) (listModel, tea.Cmd) {
	if next, ok := lm.applyCursorKey(msg, km); ok {
		return next, nil
	}
	if next, cmd, ok := lm.applyFilterKey(msg, km, api, sc); ok {
		return next, cmd
	}
	if next, cmd, ok := lm.applyPromptKey(msg, km, sc); ok {
		return next, cmd
	}
	if next, cmd, ok := lm.applyMutationKey(msg, km, api, sc); ok {
		return next, cmd
	}
	if next, cmd, ok := lm.applyOpenKey(msg, km); ok {
		return next, cmd
	}
	return lm, nil
}

// applyMutationKey handles list-side mutation bindings: close (x) and
// reopen (r) act on the highlighted row. Empty list is a quiet no-op
// so a stray keystroke on the empty-state hint does nothing.
func (lm listModel) applyMutationKey(
	msg tea.KeyMsg, km keymap, api listAPI, sc scope,
) (listModel, tea.Cmd, bool) {
	switch {
	case km.Close.matches(msg):
		next, cmd := lm.dispatchListClose(api, sc)
		return next, cmd, true
	case km.Reopen.matches(msg):
		next, cmd := lm.dispatchListReopen(api, sc)
		return next, cmd, true
	}
	return lm, nil, false
}

// dispatchListClose closes the issue under the cursor. Empty list is a
// no-op (returns lm unchanged with a nil cmd).
func (lm listModel) dispatchListClose(
	api listAPI, sc scope,
) (listModel, tea.Cmd) {
	iss, ok := lm.targetRow()
	if !ok {
		return lm, nil
	}
	lm.status = ""
	return lm, closeIssueCmd(api, projectIDForRow(iss, sc), iss.Number, lm.actor)
}

// dispatchListReopen mirrors dispatchListClose for the reopen action.
func (lm listModel) dispatchListReopen(
	api listAPI, sc scope,
) (listModel, tea.Cmd) {
	iss, ok := lm.targetRow()
	if !ok {
		return lm, nil
	}
	lm.status = ""
	return lm, reopenIssueCmd(api, projectIDForRow(iss, sc), iss.Number, lm.actor)
}

// targetRow returns the currently highlighted issue, accounting for the
// client-side filter that hides rows the cursor still indexes. ok=false
// when the visible list is empty.
func (lm listModel) targetRow() (Issue, bool) {
	rows := filteredIssues(lm.issues, lm.filter)
	if len(rows) == 0 {
		return Issue{}, false
	}
	idx := lm.cursor
	if idx >= len(rows) {
		idx = len(rows) - 1
	}
	return rows[idx], true
}

// projectIDForRow picks the right project_id for the row's mutation.
// In all-projects scope the issue carries its own ProjectID; in single-
// project scope sc.projectID wins.
func projectIDForRow(iss Issue, sc scope) int64 {
	if sc.allProjects && iss.ProjectID != 0 {
		return iss.ProjectID
	}
	return sc.projectID
}

// closeIssueCmd wraps Close into a mutationDoneMsg-emitting tea.Cmd.
// origin="list" routes the response to listModel.applyMutation even if
// the user has switched to detail view between dispatch and arrival.
func closeIssueCmd(api listAPI, pid, num int64, actor string) tea.Cmd {
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		resp, err := api.Close(ctx, pid, num, actor)
		return mutationDoneMsg{origin: "list", kind: "close", resp: resp, err: err}
	}
}

// reopenIssueCmd is the reopen counterpart of closeIssueCmd.
func reopenIssueCmd(api listAPI, pid, num int64, actor string) tea.Cmd {
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		resp, err := api.Reopen(ctx, pid, num, actor)
		return mutationDoneMsg{origin: "list", kind: "reopen", resp: resp, err: err}
	}
}

// applyOpenKey handles Enter on a list row: emit openDetailMsg with the
// issue under the cursor. The top-level Model handles the message by
// switching to the detail view and dispatching the three tab fetches.
// Empty list (cursor would point past the slice) is a quiet no-op so a
// stray Enter on the empty-state hint does nothing.
func (lm listModel) applyOpenKey(
	msg tea.KeyMsg, km keymap,
) (listModel, tea.Cmd, bool) {
	if !km.Open.matches(msg) {
		return lm, nil, false
	}
	rows := filteredIssues(lm.issues, lm.filter)
	if len(rows) == 0 {
		return lm, nil, true
	}
	idx := lm.cursor
	if idx >= len(rows) {
		idx = len(rows) - 1
	}
	iss := rows[idx]
	return lm, func() tea.Msg { return openDetailMsg{issue: iss} }, true
}

// applyCursorKey handles the j/k/g/G/pgup/pgdown family. ok=true means
// the key was consumed. The cursor moves in filtered-space (the slice
// the user actually sees) so a hidden row preceding the cursor cannot
// desync the marker from the highlighted row. lm.cursor is therefore
// an index into filteredIssues(lm.issues, lm.filter), and every render
// path that needs the unfiltered slice must re-look-up via that
// helper.
//
// Each cursor change also updates lm.selectedNumber so an SSE-driven
// refetch can put the cursor back on the same issue rather than the
// same index — see syncSelection.
func (lm listModel) applyCursorKey(msg tea.KeyMsg, km keymap) (listModel, bool) {
	rows := filteredIssues(lm.issues, lm.filter)
	n := len(rows)
	switch {
	case km.Up.matches(msg):
		if lm.cursor > 0 {
			lm.cursor--
		}
	case km.Down.matches(msg):
		if lm.cursor < n-1 {
			lm.cursor++
		}
	case km.PageUp.matches(msg):
		lm.cursor -= pageStep(n)
		if lm.cursor < 0 {
			lm.cursor = 0
		}
	case km.PageDown.matches(msg):
		lm.cursor += pageStep(n)
		if lm.cursor > n-1 {
			lm.cursor = n - 1
		}
		if lm.cursor < 0 {
			lm.cursor = 0
		}
	case km.Home.matches(msg):
		lm.cursor = 0
	case km.End.matches(msg):
		if n > 0 {
			lm.cursor = n - 1
		}
	default:
		return lm, false
	}
	lm = lm.syncSelection(rows)
	return lm, true
}

// pageStepRows is the row delta for pgup/pgdown. We don't have access
// to the rendered viewport height here, so we use a constant matching
// roughly half a screen on a typical terminal — large enough to feel
// like a page, small enough to keep context. The cap prevents an
// outright jump-to-end on small lists where pgdown is functionally
// equivalent to End.
const pageStepRows = 10

func pageStep(n int) int {
	if pageStepRows > n {
		return n
	}
	return pageStepRows
}

// syncSelection records the issue.Number under the cursor so a later
// refetch can restore the cursor onto the same issue rather than the
// same index. Empty filtered list zeroes selectedNumber so we don't
// pin to a row that no longer exists.
func (lm listModel) syncSelection(rows []Issue) listModel {
	if len(rows) == 0 {
		lm.selectedNumber = 0
		return lm
	}
	idx := lm.cursor
	if idx >= len(rows) {
		idx = len(rows) - 1
	}
	if idx < 0 {
		idx = 0
	}
	lm.selectedNumber = rows[idx].Number
	return lm
}

// applyFilterKey handles s (cycle status) and c (clear). Both dispatch
// a refetch so the daemon is the source of truth for status filtering.
// The cursor is reset to 0 because the filtered-row count (and thus
// the index space lm.cursor lives in) changes with every filter
// adjustment.
//
// selectedNumber is also cleared on each commit so the identity-
// based restore in applyFetched (after the refetch lands) doesn't
// fight the cursor=0 reset by jumping the cursor back to the
// previously-selected issue if it survived the new filter. The
// explicit "I changed the filter" intent overrides the implicit
// "follow the same issue across refetches" intent.
func (lm listModel) applyFilterKey(
	msg tea.KeyMsg, km keymap, api listAPI, sc scope,
) (listModel, tea.Cmd, bool) {
	switch {
	case km.FilterStatus.matches(msg):
		lm.filter.Status = nextStatus(lm.filter.Status)
		lm.cursor = 0
		lm.selectedNumber = 0
		lm.status = ""
		return lm, lm.refetchCmd(api, sc), true
	case km.ClearFilters.matches(msg):
		lm.filter = ListFilter{}
		lm.cursor = 0
		lm.selectedNumber = 0
		lm.status = ""
		return lm, lm.refetchCmd(api, sc), true
	}
	return lm, nil, false
}

// applyPromptKey opens an input shell. `/` and `o` open the inline
// command bar (M3a); `n` opens the inline new-issue row at the top
// of the table (M3.5c). All three hand off via openInputMsg so
// Model.openInput constructs the inputState centrally.
//
// The new-issue row is gated to non-all-projects scopes because the
// daemon's create endpoint is project-scoped; in all-projects mode
// (which is itself gated until daemon support lands) we surface a
// status hint instead of opening the row.
func (lm listModel) applyPromptKey(
	msg tea.KeyMsg, km keymap, sc scope,
) (listModel, tea.Cmd, bool) {
	switch {
	case km.Search.matches(msg):
		return lm, openInputCmd(inputSearchBar), true
	case km.FilterOwner.matches(msg):
		return lm, openInputCmd(inputOwnerBar), true
	case km.NewIssue.matches(msg):
		if sc.allProjects {
			lm.status = "cannot create from all-projects view; cd into a project"
			return lm, nil, true
		}
		return lm, openInputCmd(inputNewIssueRow), true
	}
	return lm, nil, false
}

// openInputCmd emits openInputMsg{kind} so Model.routeTopLevel can
// hoist the state into m.input via the centralised openInput
// constructor. Sub-views never construct inputState directly — the
// centralisation keeps focus, snapshot/restore, and render
// integration in one place.
func openInputCmd(k inputKind) tea.Cmd {
	return func() tea.Msg { return openInputMsg{kind: k} }
}

// clampCursorToFilter recomputes lm.cursor against the visible
// filtered slice so a live filter change can't leave the cursor past
// the end. Used by Model.applyLiveBarFilter / commitInput / cancelInput
// when the bar mutates lm.filter on the user's behalf.
func (lm listModel) clampCursorToFilter() listModel {
	visible := len(filteredIssues(lm.issues, lm.filter))
	if visible == 0 {
		lm.cursor = 0
		return lm
	}
	if lm.cursor >= visible {
		lm.cursor = visible - 1
	}
	return lm
}

// nextStatus cycles "" → "open" → "closed" → "".
func nextStatus(s string) string {
	switch s {
	case "":
		return "open"
	case "open":
		return "closed"
	default:
		return ""
	}
}

// applyFetched stores the latest issue list and restores the cursor
// onto the same issue (by Number) when possible — identity-based
// selection. Issue lists come back sorted by updated_at DESC, so any
// background mutation can shuffle row order under agent churn; pinning
// to the index would silently move the highlight to a different issue.
//
// When the previously-selected issue is no longer visible (filtered
// out, deleted, or scope changed), the cursor falls back to the same
// index clamped to the new visible-row count. Empty list zeroes both
// cursor and selectedNumber.
func (lm listModel) applyFetched(msg tea.Msg) listModel {
	switch m := msg.(type) {
	case initialFetchMsg:
		lm.loading = false
		lm.err = m.err
		if m.err == nil {
			lm.issues = m.issues
		}
	case refetchedMsg:
		lm.err = m.err
		if m.err == nil {
			lm.issues = m.issues
		}
	}
	rows := filteredIssues(lm.issues, lm.filter)
	if len(rows) == 0 {
		lm.cursor = 0
		lm.selectedNumber = 0
		return lm
	}
	if lm.selectedNumber != 0 {
		for i, iss := range rows {
			if iss.Number == lm.selectedNumber {
				lm.cursor = i
				return lm
			}
		}
	}
	// Selection lost — clamp the prior index into the new visible range
	// and re-record the issue under it so the next refetch tries to
	// follow that one instead.
	if lm.cursor >= len(rows) {
		lm.cursor = len(rows) - 1
	}
	if lm.cursor < 0 {
		lm.cursor = 0
	}
	lm.selectedNumber = rows[lm.cursor].Number
	return lm
}

// applyMutation handles a mutationDoneMsg arriving at the list view.
// "create", "close", "reopen" kinds all seed the status line and (on
// success) dispatch a refetch so the row updates without waiting for
// SSE invalidation (Task 11). Mutations whose origin is "detail" are
// dropped here so a detail-side close that completes after the user
// pops back to the list does not steal the list status line; SSE-
// driven invalidation will keep the list cache in sync once Task 11
// lands.
//
// TODO(task-12): replace lm.status string with Model-level toast
// machinery (messages.go::toastExpiredMsg + toast). The status line is
// a placeholder; toasts will own auto-expiry and stacked notifications.
func (lm listModel) applyMutation(
	m mutationDoneMsg, api listAPI, sc scope,
) (listModel, tea.Cmd) {
	if m.origin != "list" {
		return lm, nil
	}
	if m.err != nil {
		lm.status = errorStyle.Render(
			fmt.Sprintf("%s failed: %s", m.kind, m.err.Error()),
		)
		return lm, nil
	}
	lm.status = listMutationSuccessText(m)
	return lm, lm.refetchCmd(api, sc)
}

// listMutationSuccessText is the per-kind status hint after a successful
// mutation. The number comes from m.resp.Issue when present; otherwise
// the hint omits it so we don't print "#0".
func listMutationSuccessText(m mutationDoneMsg) string {
	num := int64(0)
	if m.resp != nil && m.resp.Issue != nil {
		num = m.resp.Issue.Number
	}
	switch m.kind {
	case "create":
		return fmt.Sprintf("created #%d", num)
	case "close":
		return fmt.Sprintf("closed #%d", num)
	case "reopen":
		return fmt.Sprintf("reopened #%d", num)
	}
	return ""
}

// dispatchCreateIssue is the M3.5c commit path for the inline new-issue
// row. Called from Model.commitInput when the user presses enter on
// the new-issue title field. Empty/whitespace-only titles short-
// circuit so accidental Enter in an empty row is a quiet no-op. The
// untrimmed title reaches the wire so leading/trailing whitespace
// the user deliberately typed survives.
//
// Body is left empty; M4 will chain a centered body form after the
// successful create for optional refinement. For now, an immediate
// create with body="" is the contract.
func (lm listModel) dispatchCreateIssue(
	api listAPI, sc scope, title string,
) (listModel, tea.Cmd) {
	if strings.TrimSpace(title) == "" {
		return lm, nil
	}
	actor := lm.actor
	pid := sc.projectID
	return lm, func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		resp, err := api.CreateIssue(ctx, pid, CreateIssueBody{
			Title: title, Body: "", Actor: actor,
		})
		return mutationDoneMsg{origin: "list", kind: "create", resp: resp, err: err}
	}
}

// refetchCmd returns a tea.Cmd that re-fetches the issue list using
// lm.filter for client-side fields while the wire still only honors
// Status. The command path mirrors fetchInitial. Owner/Author/Search
// narrow the result via filteredIssues at render time.
//
// dispatchKey captures the scope/filter at dispatch time;
// Model.populateCache compares it against the current state and drops
// stale responses so a slow refetch can't overwrite the list after
// the user has changed filter, switched scope, or another refetch
// reordered ahead of it.
func (lm listModel) refetchCmd(api listAPI, sc scope) tea.Cmd {
	filter := lm.filter
	dispatchKey := cacheKey{
		allProjects: sc.allProjects, projectID: sc.projectID, filter: filter,
	}
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		var (
			issues []Issue
			err    error
		)
		if sc.allProjects {
			issues, err = api.ListAllIssues(ctx, filter)
		} else {
			issues, err = api.ListIssues(ctx, sc.projectID, filter)
		}
		return refetchedMsg{dispatchKey: dispatchKey, issues: issues, err: err}
	}
}

// filteredIssues returns the subset of issues that satisfy the
// client-side filters (Owner/Author/Search). Status is filtered
// server-side via the daemon's status query param and is already
// reflected in lm.issues, so it is not re-checked here. The fast path
// returns the input slice unchanged when no client-side filter is set
// — render runs every keystroke, so this matters.
func filteredIssues(issues []Issue, f ListFilter) []Issue {
	if f.Owner == "" && f.Author == "" && f.Search == "" {
		return issues
	}
	out := make([]Issue, 0, len(issues))
	for _, iss := range issues {
		if matchesFilter(iss, f) {
			out = append(out, iss)
		}
	}
	return out
}

// matchesFilter reports whether iss satisfies the client-side filters.
// Owner is *string on the wire, so a nil pointer never matches a set
// owner. Search is case-insensitive over Title — body search would need
// the detail fetch and is out of scope for the list view.
//
// Labels are deliberately not checked: the Issue projection drops the
// labels field (Task 3 wire-vs-spec adaptation #1), so a label filter
// can't actually narrow until the wire carries them. The chip strip
// hides the label chip for the same reason; see renderChips.
func matchesFilter(iss Issue, f ListFilter) bool {
	if f.Owner != "" {
		if iss.Owner == nil || *iss.Owner != f.Owner {
			return false
		}
	}
	if f.Author != "" && iss.Author != f.Author {
		return false
	}
	if f.Search != "" {
		if !strings.Contains(
			strings.ToLower(iss.Title), strings.ToLower(f.Search),
		) {
			return false
		}
	}
	return true
}
