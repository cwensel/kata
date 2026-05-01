package tui

import (
	tea "github.com/charmbracelet/bubbletea"
)

// listModel owns list-view state: the current rows, cursor, the filter
// in effect, an optional active search prompt, and any boot/refetch
// error to surface. filter and search.buffer are forward-declared for
// Task 6 — read sites land then. The keymap lives on the parent Model
// and is passed into Update; one instance keeps the help view in
// lockstep with what handlers actually do.
type listModel struct {
	issues  []Issue
	cursor  int
	filter  ListFilter //nolint:unused // Task 6
	search  searchState
	err     error
	loading bool
}

// searchState tracks the inline search prompt. inputting=true while the
// user is typing; the buffer is committed to filter.Search on Enter.
type searchState struct {
	inputting bool
	buffer    string //nolint:unused // Task 6 inline search
}

// newListModel returns a listModel waiting for its first fetch. loading=true
// keeps the spinner-equivalent on screen until initialFetchMsg lands.
func newListModel() listModel {
	return listModel{loading: true}
}

// Update handles list-view keys and fetch results. The top-level Model
// keeps responsibility for global keys (q, ?, R) and SSE messages.
func (lm listModel) Update(msg tea.Msg, km keymap) (listModel, tea.Cmd) {
	switch m := msg.(type) {
	case tea.KeyMsg:
		if lm.search.inputting {
			return lm.handleSearchKey(m)
		}
		lm = lm.applyNavKey(m, km)
	case initialFetchMsg, refetchedMsg:
		lm = lm.applyFetched(m)
	}
	return lm, nil
}

// applyNavKey handles cursor navigation. Filter bindings (s/o/l/c),
// inline search ('/'), and the inline create prompt ('n') land in
// Task 6. Mutation keys (x/r) land in Task 9.
func (lm listModel) applyNavKey(msg tea.KeyMsg, km keymap) listModel {
	switch {
	case km.Up.matches(msg):
		if lm.cursor > 0 {
			lm.cursor--
		}
	case km.Down.matches(msg):
		if lm.cursor < len(lm.issues)-1 {
			lm.cursor++
		}
	case km.Home.matches(msg):
		lm.cursor = 0
	case km.End.matches(msg):
		if n := len(lm.issues); n > 0 {
			lm.cursor = n - 1
		}
	}
	return lm
}

// applyFetched stores the latest issue list and clamps the cursor if
// it would otherwise point past the new list end.
func (lm listModel) applyFetched(msg tea.Msg) listModel {
	switch m := msg.(type) {
	case initialFetchMsg:
		lm.loading = false
		lm.err = m.err
		if m.err == nil {
			lm.issues = m.issues
		}
	case refetchedMsg:
		lm.err = m.err
		if m.err == nil {
			lm.issues = m.issues
		}
	}
	if lm.cursor >= len(lm.issues) {
		lm.cursor = max(0, len(lm.issues)-1)
	}
	return lm
}

// handleSearchKey is a stub; Task 6 implements the inline search prompt.
func (lm listModel) handleSearchKey(_ tea.KeyMsg) (listModel, tea.Cmd) {
	return lm, nil
}
