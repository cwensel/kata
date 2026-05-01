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

//nolint:unused // Task 11
type eventReceivedMsg struct {
	eventType              string
	projectID, issueNumber int64
}

//nolint:unused // Task 11
type resetRequiredMsg struct{ resetAfterID int64 }

//nolint:unused // Task 11
type sseStatusMsg struct{ state sseConnState }

//nolint:unused // Task 11
type sseConnState int

//nolint:unused // Task 11
const (
	sseConnected sseConnState = iota
	sseReconnecting
	sseDisconnected
)

//nolint:unused // Task 11
type refetchTickMsg struct{}

//nolint:unused // Task 12
type toastExpiredMsg struct{}

//nolint:unused // Task 12
type toast struct {
	text      string
	level     toastLevel
	expiresAt time.Time
}

//nolint:unused // Task 12
type toastLevel int

//nolint:unused // Task 12
const (
	toastInfo toastLevel = iota
	toastSuccess
	toastError
)
