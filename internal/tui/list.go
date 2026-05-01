package tui

import (
	tea "github.com/charmbracelet/bubbletea"
)

// listModel owns list-view state: the current rows, cursor, the filter
// in effect, an optional active search prompt, and any boot/refetch
// error to surface. filter and search.buffer are forward-declared for
// Task 6 — read sites land then.
type listModel struct {
	keymap  keymap
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

// newListModel returns a zero-valued listModel with the canonical keymap.
func newListModel() listModel {
	return listModel{keymap: newKeymap()}
}

// Update handles list-view keys and fetch results. The top-level Model
// keeps responsibility for global keys (q, ?, R) and SSE messages.
func (lm listModel) Update(msg tea.Msg) (listModel, tea.Cmd) {
	switch m := msg.(type) {
	case tea.KeyMsg:
		if lm.search.inputting {
			return lm.handleSearchKey(m)
		}
		lm = lm.applyNavKey(m)
	case initialFetchMsg, refetchedMsg:
		lm = lm.applyFetched(m)
	}
	return lm, nil
}

// applyNavKey handles cursor navigation. Filter bindings (s/o/l/c),
// inline search ('/'), and the inline create prompt ('n') land in
// Task 6. Mutation keys (x/r) land in Task 9.
func (lm listModel) applyNavKey(msg tea.KeyMsg) listModel {
	switch {
	case lm.keymap.Up.matches(msg):
		if lm.cursor > 0 {
			lm.cursor--
		}
	case lm.keymap.Down.matches(msg):
		if lm.cursor < len(lm.issues)-1 {
			lm.cursor++
		}
	case lm.keymap.Home.matches(msg):
		lm.cursor = 0
	case lm.keymap.End.matches(msg):
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
