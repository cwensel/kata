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
	keymap keymap
	list   listModel
}

func initialModel(opts Options) Model {
	applyDefaultColorMode()
	m := Model{
		opts:   opts,
		view:   viewList,
		keymap: newKeymap(),
		list:   newListModel(),
	}
	// Seed a fixture so Task 4's binary renders without a network. Task 5
	// replaces this with a real fetch driven by Init().
	m.list.issues = fixtureIssues()
	return m
}

// Init returns startup commands for the TEA loop. Task 5 wires the
// initial daemon fetch here.
func (m Model) Init() tea.Cmd { return nil }

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
		m.list, cmd = m.list.Update(msg)
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

// fixtureIssues seeds the list view for Task 4's smoke test. Task 5
// replaces this with a real ListIssues call.
func fixtureIssues() []Issue {
	return []Issue{
		{
			Number: 1, Title: "fix login bug on Safari",
			Status: "open", Owner: ptrString("claude-4.7"),
			UpdatedAt: "2026-04-30T10:00:00Z",
		},
		{
			Number: 2, Title: "rebuild search index",
			Status: "closed", Owner: ptrString("wesm"),
			UpdatedAt: "2026-04-29T10:00:00Z",
		},
		{
			Number: 3, Title: "deleted example",
			Status:    "open",
			DeletedAt: ptrString("2026-04-28T10:00:00Z"),
			UpdatedAt: "2026-04-28T10:00:00Z",
		},
	}
}

func ptrString(s string) *string { return &s }
