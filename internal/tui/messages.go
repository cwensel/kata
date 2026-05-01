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

//nolint:unused // Task 7
type detailFetchedMsg struct {
	issue *Issue
	err   error
}

//nolint:unused // Task 9
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
