package tui

import "time"

// initialFetchMsg is delivered after the first ListIssues call returns.
type initialFetchMsg struct {
	issues []Issue
	err    error
}

// refetchedMsg carries the result of a debounced or scope-change refetch.
type refetchedMsg struct {
	issues []Issue
	err    error
}

// detailFetchedMsg carries the result of a single-issue refetch. It is
// produced by the Enter-jump path (Task 8) when a user navigates to a
// referenced issue, and will also be produced by the SSE-driven refetch
// in Task 11. gen tags the detail-open generation that dispatched the
// fetch — applyFetched discards messages whose gen no longer matches
// dm.gen so a fetch in flight when the user pops/jumps cannot pollute
// the new view with stale data.
type detailFetchedMsg struct {
	gen   int64
	issue *Issue
	err   error
}

// commentsFetchedMsg, eventsFetchedMsg, and linksFetchedMsg carry the
// per-tab fetch results dispatched in parallel by openDetailMsg. gen
// is the detail-open generation; see detailFetchedMsg for the rationale.
type commentsFetchedMsg struct {
	gen      int64
	comments []CommentEntry
	err      error
}

type eventsFetchedMsg struct {
	gen    int64
	events []EventLogEntry
	err    error
}

type linksFetchedMsg struct {
	gen   int64
	links []LinkEntry
	err   error
}

// openDetailMsg is emitted by the list view when Enter selects a row.
// The top-level Model handles it: switches m.view to viewDetail, seeds
// m.detail.issue, and dispatches the three concurrent tab fetches.
type openDetailMsg struct {
	issue Issue
}

// popDetailMsg reverts the top-level Model from viewDetail back to
// viewList. The list cursor and filter state are preserved because
// listModel is held by value and never reset on the round trip.
type popDetailMsg struct{}

// mutationDoneMsg is the result of any single mutation (create now,
// close/reopen/label/owner in Task 9). kind names which mutation so the
// list/detail Update can route to the right post-success behavior.
//
// origin discriminates which view dispatched the mutation: "list"
// mutations land in listModel.applyMutation, "detail" mutations land in
// detailModel.applyMutation. Without this tag, a list-side close
// completing after the user opened detail (or a detail close that
// arrives after Esc) would route the response to the wrong view, churn
// the wrong status line, and (for detail) trigger an unwanted refetch.
//
// gen is the detail-open generation that dispatched the mutation, set
// only when origin == "detail". The detail Update path drops responses
// whose gen does not match dm.gen so a mutation in flight when the user
// jumps or pops cannot apply to the new view.
type mutationDoneMsg struct {
	origin string
	gen    int64
	kind   string
	resp   *MutationResp
	err    error
}

// editorReturnedMsg carries the result of a $EDITOR suspend/resume
// cycle. kind discriminates which mutation should run on the trimmed
// content: "create" (new-issue body, handled by listModel), "edit"
// (issue body, handled by detailModel), or "comment" (new comment,
// handled by detailModel). err is non-nil when the editor exited with
// a non-zero status or the tmpfile read-back failed.
type editorReturnedMsg struct {
	kind, content string
	err           error
}

// eventReceivedMsg is the per-frame SSE message forwarded to the TEA
// loop by startSSE. issueNumber is zero when the event has no
// associated issue (project-level events).
type eventReceivedMsg struct {
	eventType              string
	projectID, issueNumber int64
}

// resetRequiredMsg signals sync.reset_required: the daemon's purge
// gap means the consumer's cursor is too old. The TEA loop drops the
// cache and refetches from scratch.
//
// We deliberately don't carry reset_after_id on this message: the
// daemon's contract (see internal/api/events.go EventReset) is that
// EventID == ResetAfterID, so the SSE frame's id: line — which the
// consumer already uses to update its Last-Event-ID resume cursor — is
// the authoritative checkpoint. A second copy of the same value on the
// envelope would invite drift if either path lagged.
type resetRequiredMsg struct{}

// sseStatusMsg carries connection-state transitions from the SSE
// goroutine to the TEA loop so the status bar can render the
// reconnect indicator.
type sseStatusMsg struct{ state sseConnState }

// sseConnState is the SSE consumer's connection state.
type sseConnState int

const (
	sseConnected sseConnState = iota
	sseReconnecting
	sseDisconnected
)

// refetchTickMsg fires after the 150ms debounce window so a single
// fetch covers a burst of events.
type refetchTickMsg struct{}

// toastExpiredMsg fires after a toast's TTL so Update can clear it.
type toastExpiredMsg struct{}

// toast is a transient status notification rendered below the active
// view. Task 11 uses it for the 'resynced' notice; Task 12 will own
// stacked toasts for mutation feedback.
type toast struct {
	text      string
	level     toastLevel
	expiresAt time.Time
}

// toastLevel discriminates toast styling.
type toastLevel int

const (
	toastInfo toastLevel = iota
	//nolint:unused // Task 12
	toastSuccess
	//nolint:unused // Task 12
	toastError
)
