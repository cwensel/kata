package tui

import (
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
	view   viewID
	width  int
	height int
}

func initialModel(opts Options) Model {
	return Model{opts: opts, view: viewList}
}

// Init satisfies tea.Model. The scaffold has no startup commands.
func (m Model) Init() tea.Cmd {
	return nil
}

// Update routes Bubble Tea messages. The scaffold only handles window
// resizes and the q/ctrl+c quit keys; subsequent tasks dispatch to
// sub-views.
func (m Model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.width, m.height = msg.Width, msg.Height
		return m, nil
	case tea.KeyMsg:
		if msg.String() == "q" || msg.String() == "ctrl+c" {
			return m, tea.Quit
		}
	}
	return m, nil
}

// View renders the placeholder string until later tasks supply real
// list/detail/help renderers.
func (m Model) View() string {
	return "kata tui — Plan 6 scaffolding (press q to quit)"
}
