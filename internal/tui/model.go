package tui

import (
	"context"
	"os"
	"time"

	tea "github.com/charmbracelet/bubbletea"
)

type viewID int

const (
	viewList viewID = iota
	viewDetail
	viewHelp
	viewEmpty
)

// Model is the top-level Bubble Tea model. Sub-views are embedded by
// value so Update can mutate them in place without indirection. The
// detail sub-view is held by value (not pointer) so its scroll/tab
// state lives across opens of the same issue, and so popDetailMsg
// returns to a list whose cursor and filters are unchanged.
type Model struct {
	opts   Options
	api    *Client
	scope  scope
	view   viewID
	width  int
	height int
	keymap keymap
	list   listModel
	detail detailModel
}

// initialModel constructs the root Bubble Tea model. Style vars are
// populated against opts.Stdout (or os.Stdout when nil) so unit tests
// that bypass Run still see live styles. Run re-runs applyDefaultColorMode
// once it has the opts.Stdout to pin color detection to the real stream.
func initialModel(opts Options) Model {
	applyDefaultColorMode(opts.Stdout)
	lm := newListModel()
	lm.actor = resolveTUIActor()
	return Model{
		opts:   opts,
		view:   viewList,
		keymap: newKeymap(),
		list:   lm,
		detail: newDetailModel(),
	}
}

// resolveTUIActor mirrors cmd/kata's actor precedence (env → fallback)
// minus the --as flag and git fallback: the TUI has no flag plumbing
// here and we keep the dependency surface small. Tasks 9/10 re-evaluate
// once the broader mutation path lands and may add a git fallback.
func resolveTUIActor() string {
	if v := os.Getenv("KATA_AUTHOR"); v != "" {
		return v
	}
	return "anonymous"
}

// Init dispatches the initial fetch unless boot landed on the empty
// state or no client is wired (the latter happens in unit tests that
// drive the model directly via teatest.NewTestModel and feed
// initialFetchMsg by hand). The list view sets loading=true at
// construction so the spinner shows until initialFetchMsg arrives.
func (m Model) Init() tea.Cmd {
	if m.view == viewEmpty || m.api == nil {
		return nil
	}
	return m.fetchInitial()
}

// fetchInitial returns a command that issues the first list fetch. The
// scope drives whether this is single-project or cross-project. The
// 5s ceiling matches the daemon's typical p95 list latency.
func (m Model) fetchInitial() tea.Cmd {
	api, sc, filter := m.api, m.scope, initialFilter(m.opts)
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
		return initialFetchMsg{issues: issues, err: err}
	}
}

// initialFilter projects opts into the ListFilter the boot fetch uses.
// IncludeDeleted is threaded through so client-side filtering in later
// tasks can act on it; the wire request itself never sets
// include_deleted because api.ListIssuesRequest does not accept it.
func initialFilter(opts Options) ListFilter {
	return ListFilter{IncludeDeleted: opts.IncludeDeleted}
}

// Update routes messages to the active sub-view. Quit is handled at the
// top level so it works from every view, EXCEPT while a list-view inline
// prompt or a detail-view modal is active: typing 'q' into a prompt or
// modal must reach the buffer instead of quitting. The same gate applies
// to ?, R, and any future global key.
//
// openDetailMsg / popDetailMsg are intercepted before the per-view
// dispatch because the view switch lives at this level. The detail
// sub-model is reset on open so a new issue starts at scroll=0 with the
// comments tab — but the list sub-model is untouched on pop, preserving
// the user's cursor and filter state across the round trip.
func (m Model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.width, m.height = msg.Width, msg.Height
		return m, nil
	case tea.KeyMsg:
		if m.canQuit() && m.keymap.Quit.matches(msg) {
			return m, tea.Quit
		}
	case openDetailMsg:
		return m.handleOpenDetail(msg)
	case popDetailMsg:
		m.view = viewList
		return m, nil
	}
	return m.dispatchToView(msg)
}

// canQuit reports whether a 'q' keystroke should be honored as Quit.
// False while a list prompt is open (the buffer must absorb the rune)
// or while a detail modal is open (same reason — the user is typing a
// label/owner/link target, not asking to exit).
func (m Model) canQuit() bool {
	if m.list.search.inputting {
		return false
	}
	if m.view == viewDetail && m.detail.modal.active() {
		return false
	}
	return true
}

// handleOpenDetail seeds m.detail with the chosen issue and dispatches
// the three concurrent tab fetches via tea.Batch. The fetches run in
// parallel so the user sees data on whichever tab is active first. The
// detail model also remembers the project_id and all-projects flag so
// the Enter-jump path (Task 8) has them without re-resolving scope.
func (m Model) handleOpenDetail(msg openDetailMsg) (tea.Model, tea.Cmd) {
	iss := msg.issue
	pid := detailProjectID(iss, m.scope)
	// Reset on open is the spec — no per-issue scroll memory.
	m.detail = newDetailModel()
	m.detail.issue = &iss
	m.detail.scopePID = pid
	m.detail.allProjects = m.scope.allProjects
	m.view = viewDetail
	if m.api == nil {
		return m, nil
	}
	cmds := []tea.Cmd{
		fetchComments(m.api, pid, iss.Number),
		fetchEvents(m.api, pid, iss.Number),
		fetchLinks(m.api, pid, iss.Number),
	}
	return m, tea.Batch(cmds...)
}

// dispatchToView forwards msg to the active sub-view's Update.
func (m Model) dispatchToView(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch m.view {
	case viewList:
		var cmd tea.Cmd
		m.list, cmd = m.list.Update(msg, m.keymap, m.api, m.scope)
		return m, cmd
	case viewDetail:
		var cmd tea.Cmd
		m.detail, cmd = m.detail.Update(msg, m.keymap, m.api)
		return m, cmd
	}
	return m, nil
}

// View returns the rendered string for the active sub-view.
func (m Model) View() string {
	switch m.view {
	case viewList:
		return m.list.View(m.width, m.height)
	case viewDetail:
		return m.detail.View(m.width, m.height)
	}
	return ""
}
