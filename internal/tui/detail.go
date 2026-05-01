package tui

import (
	"context"

	tea "github.com/charmbracelet/bubbletea"
)

// detailTab names which sub-tab the detail view is rendering.
type detailTab int

const (
	tabComments detailTab = iota
	tabEvents
	tabLinks
)

// detailTabCount is the modulus for the tab cycle.
const detailTabCount = 3

// detailNavCap caps the nav stack at 1 prior entry — current + prior =
// 2 levels per the plan's "2-element stack." Jumping from level 2 is
// a no-op; Esc still pops back to level 1.
const detailNavCap = 1

// detailAPI is the subset of *Client the detail view needs. Mirrors
// listAPI so detail_test.go can drive Update with a fake.
type detailAPI interface {
	GetIssue(ctx context.Context, projectID, number int64) (*Issue, error)
	ListComments(ctx context.Context, projectID, number int64) ([]CommentEntry, error)
	ListEvents(ctx context.Context, projectID, number int64) ([]EventLogEntry, error)
	ListLinks(ctx context.Context, projectID, number int64) ([]LinkEntry, error)
}

// detailModel owns detail-view state. activeTab + tabCursor address
// the highlighted row; navStack holds the prior detailModel so Esc
// pops back to the issue the user jumped from. scopePID and scope-
// flags carry the project_id used for jump fetches.
type detailModel struct {
	issue       *Issue
	loading     bool
	err         error
	activeTab   detailTab
	scroll      int // body scroll offset in lines
	tabCursor   int // active-tab row cursor
	comments    []CommentEntry
	events      []EventLogEntry
	links       []LinkEntry
	navStack    []detailModel
	scopePID    int64
	allProjects bool
}

// newDetailModel returns a zeroed detailModel.
func newDetailModel() detailModel { return detailModel{} }

// Update routes detail-view messages: keys and the four fetch results.
// Splitting the key path into handleKey keeps this dispatch ≤5 cyc.
func (dm detailModel) Update(msg tea.Msg, km keymap, api detailAPI) (detailModel, tea.Cmd) {
	switch m := msg.(type) {
	case tea.KeyMsg:
		return dm.handleKey(m, km, api)
	case detailFetchedMsg, commentsFetchedMsg, eventsFetchedMsg, linksFetchedMsg:
		return dm.applyFetched(msg), nil
	}
	return dm, nil
}

// applyFetched seeds dm with the payload from one of the four fetched-
// messages. Errors are last-write-wins; mergeErr factors that out so
// each branch is a two-liner.
func (dm detailModel) applyFetched(msg tea.Msg) detailModel {
	switch m := msg.(type) {
	case detailFetchedMsg:
		dm.loading = false
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
// tab does not clear an earlier failure on another.
func mergeErr(prev, next error) error {
	if next != nil {
		return next
	}
	return prev
}

// handleKey dispatches detail bindings: tab/shift-tab cycle, j/k move
// the tab cursor (or scroll the body when the active tab is empty),
// enter jumps to a referenced issue, esc pops the nav stack one level
// before falling through to popDetailMsg.
func (dm detailModel) handleKey(
	msg tea.KeyMsg, km keymap, api detailAPI,
) (detailModel, tea.Cmd) {
	switch {
	case km.NextTab.matches(msg):
		dm.activeTab = (dm.activeTab + 1) % detailTabCount
		dm.tabCursor = 0
	case km.PrevTab.matches(msg):
		dm.activeTab = (dm.activeTab + detailTabCount - 1) % detailTabCount
		dm.tabCursor = 0
	case km.Up.matches(msg):
		return dm.handleUp(), nil
	case km.Down.matches(msg):
		return dm.handleDown(), nil
	case km.Open.matches(msg):
		return dm.handleEnter(api)
	case km.Back.matches(msg):
		return dm.handleBack()
	}
	return dm, nil
}

// handleUp moves the tab cursor up when the active tab has rows;
// otherwise scrolls the body. Both clamp at zero.
func (dm detailModel) handleUp() detailModel {
	if dm.activeRowCount() > 0 {
		if dm.tabCursor > 0 {
			dm.tabCursor--
		}
		return dm
	}
	if dm.scroll > 0 {
		dm.scroll--
	}
	return dm
}

// handleDown moves the tab cursor down (clamped to row-count - 1) or
// scrolls the body when the tab is empty. Body scroll's upper bound
// is clamped in the renderer.
func (dm detailModel) handleDown() detailModel {
	if n := dm.activeRowCount(); n > 0 {
		if dm.tabCursor < n-1 {
			dm.tabCursor++
		}
		return dm
	}
	dm.scroll++
	return dm
}

// handleBack pops one level off the nav stack if non-empty, otherwise
// returns popDetailCmd so the top-level Model reverts to viewList.
func (dm detailModel) handleBack() (detailModel, tea.Cmd) {
	if len(dm.navStack) == 0 {
		return dm, popDetailCmd()
	}
	prev := dm.navStack[len(dm.navStack)-1]
	prev.navStack = dm.navStack[:len(dm.navStack)-1]
	return prev, nil
}

// handleEnter dispatches the Enter-jump on events/links tabs. The
// comments tab does not navigate. No-op when the api is unwired, the
// stack is at cap, or there is no jump target under the cursor.
func (dm detailModel) handleEnter(api detailAPI) (detailModel, tea.Cmd) {
	if api == nil || len(dm.navStack) >= detailNavCap {
		return dm, nil
	}
	target, ok := dm.jumpTarget()
	if !ok {
		return dm, nil
	}
	return dm.jumpTo(api, target)
}

// jumpTarget returns the issue number to jump to from the active tab+
// cursor. Comments tab never jumps. Events tab reads payload.to_number
// or payload.issue_number; links tab reads the link's ToNumber.
func (dm detailModel) jumpTarget() (int64, bool) {
	switch dm.activeTab {
	case tabEvents:
		return eventJumpTarget(dm.events, dm.tabCursor)
	case tabLinks:
		return linkJumpTarget(dm.links, dm.tabCursor)
	}
	return 0, false
}

// jumpTo pushes the current dm onto its own nav stack and swaps in a
// fresh detail seeded with loading=true. The active tab is preserved
// so the user lands on the same tab. Fetches run in parallel via Batch.
func (dm detailModel) jumpTo(api detailAPI, number int64) (detailModel, tea.Cmd) {
	prior := dm
	prior.navStack = nil
	pid := dm.scopePID
	next := detailModel{
		loading:     true,
		activeTab:   dm.activeTab,
		navStack:    append(dm.navStack, prior),
		scopePID:    pid,
		allProjects: dm.allProjects,
	}
	cmds := []tea.Cmd{
		fetchIssue(api, pid, number),
		fetchComments(api, pid, number),
		fetchEvents(api, pid, number),
		fetchLinks(api, pid, number),
	}
	return next, tea.Batch(cmds...)
}

// activeRowCount is the row count for the active tab.
func (dm detailModel) activeRowCount() int {
	switch dm.activeTab {
	case tabComments:
		return len(dm.comments)
	case tabEvents:
		return len(dm.events)
	case tabLinks:
		return len(dm.links)
	}
	return 0
}

// eventJumpTarget reads the issue number that a jumpable event refers
// to. link.added/link.removed carry to_number; we also accept
// issue_number for forward-compat.
func eventJumpTarget(events []EventLogEntry, idx int) (int64, bool) {
	if idx < 0 || idx >= len(events) {
		return 0, false
	}
	return readEventTargetNumber(events[idx])
}

// readEventTargetNumber pulls an int64 issue number out of e.Payload.
// JSON decodes numbers as float64 by default; int64/int are accepted so
// hand-built test fixtures don't need to round-trip through json.
func readEventTargetNumber(e EventLogEntry) (int64, bool) {
	if e.Payload == nil {
		return 0, false
	}
	for _, k := range []string{"to_number", "issue_number"} {
		if v, ok := e.Payload[k]; ok {
			if n, ok := numberFromAny(v); ok {
				return n, true
			}
		}
	}
	return 0, false
}

// numberFromAny widens a JSON-decoded number to int64.
func numberFromAny(v any) (int64, bool) {
	switch n := v.(type) {
	case float64:
		return int64(n), true
	case int64:
		return n, true
	case int:
		return int64(n), true
	}
	return 0, false
}

// linkJumpTarget returns the link's ToNumber.
func linkJumpTarget(links []LinkEntry, idx int) (int64, bool) {
	if idx < 0 || idx >= len(links) {
		return 0, false
	}
	return links[idx].ToNumber, true
}

// popDetailCmd emits popDetailMsg so the top-level Model reverts to
// viewList. listModel is held by value so its cursor and filter state
// survive the round trip untouched.
func popDetailCmd() tea.Cmd {
	return func() tea.Msg { return popDetailMsg{} }
}

// detailProjectID picks the project_id to fetch under. In all-projects
// scope, we use issue.ProjectID (the issue carries it on the wire); in
// single-project scope we use sc.projectID. Zero issue.ProjectID falls
// back to sc so fixtures that omit ProjectID still work.
func detailProjectID(iss Issue, sc scope) int64 {
	if sc.allProjects && iss.ProjectID != 0 {
		return iss.ProjectID
	}
	return sc.projectID
}
