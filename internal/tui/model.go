package tui

import (
	"context"
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

func initialModel(opts Options) Model {
	applyDefaultColorMode()
	return Model{
		opts:   opts,
		view:   viewList,
		keymap: newKeymap(),
		list:   newListModel(),
	}
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
	api, sc := m.api, m.scope
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		var (
			issues []Issue
			err    error
		)
		if sc.allProjects {
			issues, err = api.ListAllIssues(ctx, ListFilter{})
		} else {
			issues, err = api.ListIssues(ctx, sc.projectID, ListFilter{})
		}
		return initialFetchMsg{issues: issues, err: err}
	}
}

// Update routes messages to the active sub-view, with quit handled at
// the top level so it works from every view.
func (m Model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.width, m.height = msg.Width, msg.Height
		return m, nil
	case tea.KeyMsg:
		if m.keymap.Quit.matches(msg) {
			return m, tea.Quit
		}
	}
	switch m.view {
	case viewList:
		var cmd tea.Cmd
		m.list, cmd = m.list.Update(msg, m.keymap)
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
