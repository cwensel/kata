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
// value so Update can mutate them in place without indirection.
type Model struct {
	opts   Options
	api    *Client
	scope  scope
	view   viewID
	width  int
	height int
	keymap keymap
	list   listModel
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
// prompt is active: typing 'q' into the search/owner/title prompt must
// reach the buffer instead of quitting. The same gate applies to ?, R,
// and any future global key.
func (m Model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.width, m.height = msg.Width, msg.Height
		return m, nil
	case tea.KeyMsg:
		if !m.list.search.inputting && m.keymap.Quit.matches(msg) {
			return m, tea.Quit
		}
	}
	switch m.view {
	case viewList:
		var cmd tea.Cmd
		m.list, cmd = m.list.Update(msg, m.keymap, m.api, m.scope)
		return m, cmd
	}
	return m, nil
}

// View returns the rendered string for the active sub-view.
func (m Model) View() string {
	switch m.view {
	case viewList:
		return m.list.View(m.width, m.height)
	}
	return ""
}
