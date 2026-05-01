package tui

import (
	"context"
	"fmt"

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
	Close(ctx context.Context, projectID, number int64, actor string) (*MutationResp, error)
	Reopen(ctx context.Context, projectID, number int64, actor string) (*MutationResp, error)
	AddLabel(
		ctx context.Context, projectID, number int64, label, actor string,
	) (*MutationResp, error)
	RemoveLabel(
		ctx context.Context, projectID, number int64, label, actor string,
	) (*MutationResp, error)
	Assign(
		ctx context.Context, projectID, number int64, owner, actor string,
	) (*MutationResp, error)
	AddLink(
		ctx context.Context, projectID, number int64, body LinkBody, actor string,
	) (*MutationResp, error)
	EditBody(
		ctx context.Context, projectID, number int64, body, actor string,
	) (*MutationResp, error)
	AddComment(
		ctx context.Context, projectID, number int64, body, actor string,
	) (*MutationResp, error)
}

// detailModel owns detail-view state. activeTab + tabCursor address
// the highlighted row; navStack holds the prior detailModel so Esc
// pops back to the issue the user jumped from. scopePID and scope-
// flags carry the project_id used for jump fetches. modal/status/actor
// support the Task 9 mutation path: modal is the inline label/owner/
// link prompt, status is the one-shot toast text (Task 12 will swap to
// timed expiry), and actor is the user identity threaded into mutations.
//
// gen is the detail-open generation: it increments every time the user
// opens or jumps to a different issue. Every async fetch and detail-
// originated mutation captures the current gen at dispatch time, and
// applyFetched/applyMutation discard messages whose gen no longer
// matches so an in-flight response cannot pollute a different issue.
//
// commentsLoading / eventsLoading / linksLoading and their per-tab
// error siblings drive the per-tab placeholders ("(loading...)" /
// "comments: <err>") so the user can tell whether an empty tab is the
// daemon still working or a real failure they should react to.
type detailModel struct {
	issue           *Issue
	loading         bool
	err             error
	gen             int64
	activeTab       detailTab
	scroll          int // body scroll offset in lines
	tabCursor       int // active-tab row cursor
	comments        []CommentEntry
	events          []EventLogEntry
	links           []LinkEntry
	commentsLoading bool
	eventsLoading   bool
	linksLoading    bool
	commentsErr     error
	eventsErr       error
	linksErr        error
	navStack        []detailModel
	scopePID        int64
	allProjects     bool
	actor           string
	status          string
	// dm.modal was removed in M3b — the M3a/b input infrastructure on
	// Model.input owns inline label/owner/link prompts now.
}

// newDetailModel returns a zeroed detailModel.
func newDetailModel() detailModel { return detailModel{} }

// Update routes detail-view messages: keys and the four fetch results.
// After M3b the dm.modal in-place input was retired; the panel-local
// prompt lives on Model.input and is dispatched at the Model level,
// so dm.Update no longer has a "modal active" branch.
func (dm detailModel) Update(msg tea.Msg, km keymap, api detailAPI) (detailModel, tea.Cmd) {
	switch m := msg.(type) {
	case tea.KeyMsg:
		return dm.handleKey(m, km, api)
	case mutationDoneMsg:
		return dm.applyMutation(m, api)
	case editorReturnedMsg:
		return dm.applyEditorReturned(m, api)
	case detailFetchedMsg, commentsFetchedMsg, eventsFetchedMsg, linksFetchedMsg:
		return dm.applyFetched(msg), nil
	}
	return dm, nil
}

// applyFetched seeds dm with the payload from one of the four fetched-
// messages. Per-tab errors land on dm.commentsErr/eventsErr/linksErr
// so the user can tell which tab failed; the detail-issue error still
// rides dm.err because it gates the entire view. Messages whose gen
// does not match dm.gen are dropped so an in-flight fetch cannot
// pollute a different issue after Esc + reopen or an Enter-jump to a
// referenced issue.
func (dm detailModel) applyFetched(msg tea.Msg) detailModel {
	switch m := msg.(type) {
	case detailFetchedMsg:
		if m.gen != dm.gen {
			return dm
		}
		dm.loading = false
		if m.issue != nil {
			dm.issue = m.issue
		}
		dm.err = mergeErr(dm.err, m.err)
	case commentsFetchedMsg:
		if m.gen != dm.gen {
			return dm
		}
		dm.commentsLoading = false
		dm.comments = m.comments
		dm.commentsErr = m.err
	case eventsFetchedMsg:
		if m.gen != dm.gen {
			return dm
		}
		dm.eventsLoading = false
		dm.events = m.events
		dm.eventsErr = m.err
	case linksFetchedMsg:
		if m.gen != dm.gen {
			return dm
		}
		dm.linksLoading = false
		dm.links = m.links
		dm.linksErr = m.err
	}
	return dm
}

// mergeErr keeps the last non-nil error so a successful fetch on one
// tab does not clear an earlier failure on another. Today only the
// detail-issue fetch path uses this; the per-tab errors live on their
// own dm fields.
func mergeErr(prev, next error) error {
	if next != nil {
		return next
	}
	return prev
}

// handleKey dispatches detail bindings. The function is intentionally a
// thin router: navigation keys live here directly, mutation keys defer
// to handleMutationKey so the cyclomatic budget (≤8) holds.
func (dm detailModel) handleKey(
	msg tea.KeyMsg, km keymap, api detailAPI,
) (detailModel, tea.Cmd) {
	if next, cmd, ok := dm.handleNavKey(msg, km, api); ok {
		return next, cmd
	}
	if next, cmd, ok := dm.handleEditorKey(msg, km); ok {
		return next, cmd
	}
	if next, cmd, ok := dm.handleMutationKey(msg, km, api); ok {
		return next, cmd
	}
	return dm, nil
}

// handleNavKey processes the navigation/cursor/scroll/tab bindings.
// Returns ok=true when the key was consumed; handleKey forwards to
// handleMutationKey otherwise.
func (dm detailModel) handleNavKey(
	msg tea.KeyMsg, km keymap, api detailAPI,
) (detailModel, tea.Cmd, bool) {
	switch {
	case km.NextTab.matches(msg):
		dm.activeTab = (dm.activeTab + 1) % detailTabCount
		dm.tabCursor = 0
	case km.PrevTab.matches(msg):
		dm.activeTab = (dm.activeTab + detailTabCount - 1) % detailTabCount
		dm.tabCursor = 0
	case km.Up.matches(msg):
		return dm.handleUp(), nil, true
	case km.Down.matches(msg):
		return dm.handleDown(), nil, true
	case km.Open.matches(msg):
		next, cmd := dm.handleEnter(api)
		return next, cmd, true
	case km.Back.matches(msg):
		next, cmd := dm.handleBack()
		return next, cmd, true
	default:
		return dm, nil, false
	}
	return dm, nil, true
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
//
// Rather than building the new detailModel here, handleEnter emits
// jumpDetailMsg so the top-level Model can allocate the new gen from
// its monotonic m.nextGen counter — see Model.handleJumpDetail. dm is
// returned unchanged because the navStack push and gen advance happen
// at the Model level when it consumes jumpDetailMsg.
func (dm detailModel) handleEnter(api detailAPI) (detailModel, tea.Cmd) {
	if api == nil || len(dm.navStack) >= detailNavCap {
		return dm, nil
	}
	target, ok := dm.jumpTarget()
	if !ok {
		return dm, nil
	}
	return dm, jumpDetailCmd(target)
}

// jumpDetailCmd emits a jumpDetailMsg so Model.handleJumpDetail can
// perform the actual jump (with a fresh monotonic gen). Splitting the
// emit off handleEnter keeps the cmd shape obvious in tests.
func jumpDetailCmd(number int64) tea.Cmd {
	return func() tea.Msg { return jumpDetailMsg{number: number} }
}

// jumpTarget returns the issue number to jump to from the active tab+
// cursor. Comments tab never jumps. Events tab reads payload.to_number
// or payload.issue_number; links tab reads the link's ToNumber, or
// FromNumber when the link is incoming (ToNumber matches the current
// issue) so Enter takes the user to the other end of the relation.
func (dm detailModel) jumpTarget() (int64, bool) {
	switch dm.activeTab {
	case tabEvents:
		return eventJumpTarget(dm.events, dm.tabCursor)
	case tabLinks:
		current := int64(0)
		if dm.issue != nil {
			current = dm.issue.Number
		}
		return linkJumpTarget(dm.links, dm.tabCursor, current)
	}
	return 0, false
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

// activeChunks builds the entryChunk slice for the active tab using
// the same per-entry layout the per-tab renderer produces (header +
// wrapped body + separator for comments; one line for events / links).
// Used by the detail scroll indicator so the visible-entry window is
// computed against the renderer's actual chunk shape rather than
// comparing entry count to a line budget — fixes the multi-line
// comment indicator gap from roborev #119 finding 2.
//
// width is the rendered width of the tab pane (the same width passed
// to the tab renderers from renderActiveTab).
func (dm detailModel) activeChunks(width int) []entryChunk {
	switch dm.activeTab {
	case tabComments:
		out := make([]entryChunk, 0, len(dm.comments))
		for _, c := range dm.comments {
			lines := []string{
				fmt.Sprintf("[%s] %s",
					sanitizeForDisplay(c.Author), fmtTime(c.CreatedAt)),
			}
			for _, ln := range wrapBody(sanitizeForDisplay(c.Body), max(1, width-2)) {
				lines = append(lines, "  "+ln)
			}
			lines = append(lines, "")
			out = append(out, entryChunk{lines: lines})
		}
		return out
	case tabEvents:
		out := make([]entryChunk, 0, len(dm.events))
		for range dm.events {
			out = append(out, entryChunk{lines: []string{""}})
		}
		return out
	case tabLinks:
		out := make([]entryChunk, 0, len(dm.links))
		for range dm.links {
			out = append(out, entryChunk{lines: []string{""}})
		}
		return out
	}
	return nil
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
