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

// detailFetchedMsg carries the result of a single-issue refetch (Task 11
// SSE-driven; Task 7 wires the consumer side so the detail view ignores
// stale-projection drift once that lands).
//
//nolint:unused // dispatch lives in Task 11; the consumer is in detail.go.
type detailFetchedMsg struct {
	issue *Issue
	err   error
}

// commentsFetchedMsg, eventsFetchedMsg, and linksFetchedMsg carry the
// per-tab fetch results dispatched in parallel by openDetailMsg.
type commentsFetchedMsg struct {
	comments []CommentEntry
	err      error
}

type eventsFetchedMsg struct {
	events []EventLogEntry
	err    error
}

type linksFetchedMsg struct {
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
type mutationDoneMsg struct {
	kind string
	resp *MutationResp
	err  error
}

//nolint:unused // Task 10
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
