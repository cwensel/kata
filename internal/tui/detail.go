package tui

import (
	"context"
	"time"

	tea "github.com/charmbracelet/bubbletea"
)

// detailTab names which sub-tab the detail view is rendering. The three
// values are positional indices into the tab strip so the modulo math in
// the Tab/Shift-Tab handlers stays branch-free.
type detailTab int

const (
	tabComments detailTab = iota
	tabEvents
	tabLinks
)

// detailTabCount is the modulus for the cycle. Hand-rolled instead of
// len(...) because there's no slice to range over.
const detailTabCount = 3

// detailAPI is the subset of *Client the detail-view fetches need.
// Defining it as an interface mirrors listAPI so detail_test.go can
// drive Update with a fake without standing up an httptest server.
type detailAPI interface {
	ListComments(ctx context.Context, projectID, number int64) ([]CommentEntry, error)
	ListEvents(ctx context.Context, projectID, number int64) ([]EventLogEntry, error)
	ListLinks(ctx context.Context, projectID, number int64) ([]LinkEntry, error)
}

// detailModel owns detail-view state: the issue under display, the
// active tab, body scroll offset, and per-tab projections seeded by the
// three concurrent fetches dispatched on open. Errors are last-write
// because Task 7 is a skeleton; Task 8 may refine to per-tab error chips.
type detailModel struct {
	issue     *Issue
	loading   bool
	err       error
	activeTab detailTab
	scroll    int // body scroll offset in lines
	comments  []CommentEntry
	events    []EventLogEntry
	links     []LinkEntry
}

// newDetailModel returns a zeroed detailModel. The view is "loading…"
// until the issue field is populated (typically synchronously by the
// openDetailMsg handler at the top-level Model).
func newDetailModel() detailModel { return detailModel{} }

// Update routes detail-view messages: keys (j/k scroll, tab/shift-tab
// cycle, esc back) and the four fetch messages (issue + three tab
// projections). The keymap is passed in so Help stays in lockstep. The
// fetch-message branches are split into applyFetched so this dispatch
// stays under the project's cyclomatic ≤8 budget — the listModel.Update
// shape is mirrored for consistency.
func (dm detailModel) Update(msg tea.Msg, km keymap) (detailModel, tea.Cmd) {
	switch m := msg.(type) {
	case tea.KeyMsg:
		return dm.handleKey(m, km)
	case detailFetchedMsg, commentsFetchedMsg, eventsFetchedMsg, linksFetchedMsg:
		return dm.applyFetched(msg), nil
	}
	return dm, nil
}

// applyFetched seeds dm with the payload from one of the four fetched-
// messages. Errors are last-write-wins (most recent failure surfaces
// on dm.err); a successful fetch on one tab does not clear an earlier
// failure on another. mergeErr factors the err handling out so each
// case is a two-liner and applyFetched stays ≤5 cyclomatic. Task 8
// refines this to per-tab error chips and can drop mergeErr.
func (dm detailModel) applyFetched(msg tea.Msg) detailModel {
	// Last-write-wins for tab errors; Task 8 refines per-tab error chips.
	switch m := msg.(type) {
	case detailFetchedMsg:
		dm.loading = false
		// Defensive: refetch errors arrive with issue=nil. Don't clobber
		// the populated issue we already display.
		if m.issue != nil {
			dm.issue = m.issue
		}
		dm.err = mergeErr(dm.err, m.err)
	case commentsFetchedMsg:
		dm.comments = m.comments
		dm.err = mergeErr(dm.err, m.err)
	case eventsFetchedMsg:
		dm.events = m.events
		dm.err = mergeErr(dm.err, m.err)
	case linksFetchedMsg:
		dm.links = m.links
		dm.err = mergeErr(dm.err, m.err)
	}
	return dm
}

// mergeErr keeps the last non-nil error so a successful fetch on one
// tab does not clear an earlier failure on another. Task 8 swaps the
// single-err model for per-tab chips and mergeErr can go away.
func mergeErr(prev, next error) error {
	if next != nil {
		return next
	}
	return prev
}

// handleKey dispatches the four bindings the detail view consumes:
// j/k scroll the body, tab/shift-tab cycle the lower pane, esc returns
// to the list. Anything else is a quiet no-op so unrelated keystrokes
// do not interfere.
func (dm detailModel) handleKey(msg tea.KeyMsg, km keymap) (detailModel, tea.Cmd) {
	switch {
	case km.Up.matches(msg):
		if dm.scroll > 0 {
			dm.scroll--
		}
	case km.Down.matches(msg):
		dm.scroll++ // upper bound clamped in the render path
	case km.NextTab.matches(msg):
		dm.activeTab = (dm.activeTab + 1) % detailTabCount
	case km.PrevTab.matches(msg):
		dm.activeTab = (dm.activeTab + detailTabCount - 1) % detailTabCount
	case km.Back.matches(msg):
		return dm, popDetailCmd()
	}
	return dm, nil
}

// popDetailCmd returns a tea.Cmd that emits popDetailMsg. The top-level
// Model handles popDetailMsg by reverting m.view to viewList; listModel
// is held by value so its cursor and filter state survive the round
// trip untouched.
func popDetailCmd() tea.Cmd {
	return func() tea.Msg { return popDetailMsg{} }
}

// fetchComments wraps Client.ListComments for use as a tea.Cmd. The 5s
// ceiling matches fetchInitial so the detail view honors the same
// budget as the list-fetch path.
func fetchComments(api detailAPI, projectID, number int64) tea.Cmd {
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		comments, err := api.ListComments(ctx, projectID, number)
		return commentsFetchedMsg{comments: comments, err: err}
	}
}

// fetchEvents wraps Client.ListEvents for use as a tea.Cmd.
func fetchEvents(api detailAPI, projectID, number int64) tea.Cmd {
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		events, err := api.ListEvents(ctx, projectID, number)
		return eventsFetchedMsg{events: events, err: err}
	}
}

// fetchLinks wraps Client.ListLinks for use as a tea.Cmd.
func fetchLinks(api detailAPI, projectID, number int64) tea.Cmd {
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		links, err := api.ListLinks(ctx, projectID, number)
		return linksFetchedMsg{links: links, err: err}
	}
}

// detailProjectID picks the project_id to fetch under. In single-project
// scope the URL came from sc.projectID so we use that. In all-projects
// scope the issue carries its own project_id in the wire projection
// (db.Issue serializes project_id), so we use issue.ProjectID. A zero
// value falls back to sc.projectID — defensive for fixtures that omit
// the field but render anyway.
func detailProjectID(iss Issue, sc scope) int64 {
	if sc.allProjects && iss.ProjectID != 0 {
		return iss.ProjectID
	}
	return sc.projectID
}
