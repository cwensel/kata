package tui

import (
	"io"
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
)

// splitTestSetup boots a Model into split layout (160x40) with the
// listFixture seeded so the split tests have something to render and
// drive cursor moves against. Returns the model and a cleanup that
// reverts the rebuilt color mode.
func splitTestSetup(t *testing.T) (Model, func()) {
	t.Helper()
	t.Setenv("KATA_COLOR_MODE", "none")
	t.Setenv("NO_COLOR", "")
	applyDefaultColorMode(io.Discard)
	m := initialModel(Options{})
	m.scope = scope{projectID: 7, projectName: "kata"}
	m.list.loading = false
	m.list.issues = snapListFixture()
	out, _ := m.Update(tea.WindowSizeMsg{Width: 160, Height: 40})
	m = out.(Model)
	cleanup := func() { applyDefaultColorMode(io.Discard) }
	if m.layout != layoutSplit {
		t.Fatalf("split setup failed: layout=%v want layoutSplit", m.layout)
	}
	return m, cleanup
}

// TestSplit_CursorMoveRetargetsDetail covers the synchronous detail-
// follows-cursor behavior: pressing j three times in the list pane
// must land m.detail.issue on the third row's issue without waiting
// for the debounce tick (the fetch is debounced; the dm.issue
// retarget is immediate).
func TestSplit_CursorMoveRetargetsDetail(t *testing.T) {
	m, cleanup := splitTestSetup(t)
	defer cleanup()
	// Press j twice — the fixture has 3 rows so cursor lands on row 2.
	for i := 0; i < 2; i++ {
		out, _ := m.Update(runeKey('j'))
		m = out.(Model)
	}
	if m.detail.issue == nil {
		t.Fatal("dm.issue stayed nil after cursor moves")
	}
	want := m.list.issues[2].Number
	if m.detail.issue.Number != want {
		t.Errorf("dm.issue.Number = %d, want %d", m.detail.issue.Number, want)
	}
	if m.list.cursor != 2 {
		t.Errorf("list.cursor = %d, want 2", m.list.cursor)
	}
}

// TestSplit_DebounceCoalescesBursts: rapid j keys must bump the
// debounce gen each time so older pending ticks drop. We can't
// directly observe tea.Tick scheduling from here, but the gen
// counter is the load-bearing identifier — verify it advances by N
// for N keystrokes (or fewer if some keystrokes don't move the
// cursor because we hit the end).
func TestSplit_DebounceCoalescesBursts(t *testing.T) {
	m, cleanup := splitTestSetup(t)
	defer cleanup()
	startGen := m.nextDetailFollowGen
	for i := 0; i < 5; i++ {
		out, _ := m.Update(runeKey('j'))
		m = out.(Model)
	}
	// Cursor caps at len-1 = 2 (3 rows), so two of the five j keys
	// move and three are no-ops. The gen advances only on actual
	// cursor moves, so the counter goes up by 2.
	if m.nextDetailFollowGen-startGen != 2 {
		t.Errorf("nextDetailFollowGen advanced by %d, want 2",
			m.nextDetailFollowGen-startGen)
	}
}

// TestSplit_TabMovesFocusToDetail: tab in split mode while focusList
// flips focus to focusDetail (and the list pane border switches to
// the inactive style on render).
func TestSplit_TabMovesFocusToDetail(t *testing.T) {
	m, cleanup := splitTestSetup(t)
	defer cleanup()
	// Seed a detail issue so the tab move actually flips focus (no
	// detail open => tab is a no-op per the routeLayoutFocusKey
	// guard).
	iss := m.list.issues[0]
	m.detail.issue = &iss
	if m.focus != focusList {
		t.Fatalf("setup focus=%v want focusList", m.focus)
	}
	out, _ := m.Update(tea.KeyMsg{Type: tea.KeyTab})
	m = out.(Model)
	if m.focus != focusDetail {
		t.Errorf("focus=%v after tab, want focusDetail", m.focus)
	}
}

// TestSplit_EnterMovesFocusToDetail: enter on focusList dispatches
// openDetailMsg through the list pane handler; routing the resulting
// message moves focus to focusDetail (per handleOpenDetail's split-
// mode branch).
func TestSplit_EnterMovesFocusToDetail(t *testing.T) {
	m, cleanup := splitTestSetup(t)
	defer cleanup()
	out, cmd := m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	m = out.(Model)
	if cmd == nil {
		t.Fatal("Enter on list pane produced no cmd; expected openDetailMsg dispatch")
	}
	msg := cmd()
	if _, ok := msg.(openDetailMsg); !ok {
		t.Fatalf("expected openDetailMsg from Enter, got %T", msg)
	}
	out, _ = m.Update(msg)
	m = out.(Model)
	if m.focus != focusDetail {
		t.Errorf("focus=%v after enter+route, want focusDetail", m.focus)
	}
}

// TestSplit_EscReturnsFocusToList: esc on focusDetail flips focus
// back to focusList without consuming the esc on the detail pane
// (the per-pane back-handler is reserved for the no-input case).
func TestSplit_EscReturnsFocusToList(t *testing.T) {
	m, cleanup := splitTestSetup(t)
	defer cleanup()
	iss := m.list.issues[0]
	m.detail.issue = &iss
	m.focus = focusDetail
	out, _ := m.Update(tea.KeyMsg{Type: tea.KeyEsc})
	m = out.(Model)
	if m.focus != focusList {
		t.Errorf("focus=%v after esc, want focusList", m.focus)
	}
}

// TestSplit_EscDoesNotEscapeWhilePromptActive: with a panel-local
// prompt open on the detail pane, esc closes the prompt but leaves
// focus on the detail pane (the routeInputKey path absorbs esc
// before routeLayoutFocusKey runs). A second esc then moves focus.
func TestSplit_EscDoesNotEscapeWhilePromptActive(t *testing.T) {
	m, cleanup := splitTestSetup(t)
	defer cleanup()
	iss := m.list.issues[0]
	m.detail.issue = &iss
	m.detail.scopePID = 7
	m.focus = focusDetail
	// Open a label prompt.
	m, _ = m.openInput(inputLabelPrompt)
	if m.input.kind != inputLabelPrompt {
		t.Fatalf("setup failed: input.kind=%v want inputLabelPrompt", m.input.kind)
	}
	out, _ := m.Update(tea.KeyMsg{Type: tea.KeyEsc})
	m = out.(Model)
	if m.input.kind != inputNone {
		t.Errorf("input.kind=%v after first esc, want inputNone (prompt closed)", m.input.kind)
	}
	if m.focus != focusDetail {
		t.Errorf("focus=%v after first esc, want focusDetail (focus stays)", m.focus)
	}
	// Second esc moves focus.
	out, _ = m.Update(tea.KeyMsg{Type: tea.KeyEsc})
	m = out.(Model)
	if m.focus != focusList {
		t.Errorf("focus=%v after second esc, want focusList", m.focus)
	}
}

// TestSplit_FilterModalOverlaysWholeTerminal: opening the filter
// modal in split mode renders the centered overlay over the whole
// terminal (not anchored to a single pane). We verify by counting
// the ╭ corners — the modal box has exactly one top-left corner; if
// the modal accidentally rendered inside a pane the surrounding
// pane border would inject extras.
func TestSplit_FilterModalOverlaysWholeTerminal(t *testing.T) {
	m, cleanup := splitTestSetup(t)
	defer cleanup()
	m, _ = m.openInput(inputFilterForm)
	got := m.View()
	if !strings.Contains(got, "filter") {
		t.Fatalf("filter modal did not render; output:\n%s", got)
	}
	// Modal box uses rounded border ╭ ╮ ╰ ╯; pane borders use the
	// normal border (┌ ┐ └ ┘). One ╭ means the modal box is the
	// only rounded panel on screen.
	if c := strings.Count(got, "╭"); c != 1 {
		t.Errorf("expected exactly 1 modal top-left ╭ corner, got %d", c)
	}
}

// TestSplit_NewIssueFormOverlaysWholeTerminal: same property for the
// new-issue centered form.
func TestSplit_NewIssueFormOverlaysWholeTerminal(t *testing.T) {
	m, cleanup := splitTestSetup(t)
	defer cleanup()
	m, _ = m.openInput(inputNewIssueForm)
	got := m.View()
	if !strings.Contains(got, "new issue") {
		t.Fatalf("new-issue form did not render; output:\n%s", got)
	}
	if c := strings.Count(got, "╭"); c != 1 {
		t.Errorf("expected exactly 1 modal top-left ╭ corner, got %d", c)
	}
}

// TestSplit_HelpRowSwapsWithFocus: focus=list shows list footer
// bindings (e.g. "search"); switching to focus=detail shows detail
// bindings (e.g. "tab", "edit").
func TestSplit_HelpRowSwapsWithFocus(t *testing.T) {
	m, cleanup := splitTestSetup(t)
	defer cleanup()
	listView := m.View()
	if !strings.Contains(listView, "search") {
		t.Errorf("list-focus footer missing 'search' hint:\n%s", listView)
	}
	iss := m.list.issues[0]
	m.detail.issue = &iss
	m.focus = focusDetail
	detailView := m.View()
	if !strings.Contains(detailView, "edit") {
		t.Errorf("detail-focus footer missing 'edit' hint:\n%s", detailView)
	}
}

// TestSplit_SuggestionMenuClampedToDetailPane: opening `+` on the
// detail pane in split mode anchors the menu inside the detail-pane
// column range. The menu sits to the right of the list pane; we
// search for the menu content row ("alpha (1)") and verify it
// starts at a column >= splitListPaneWidth.
func TestSplit_SuggestionMenuClampedToDetailPane(t *testing.T) {
	m, cleanup := splitTestSetup(t)
	defer cleanup()
	iss := m.list.issues[0]
	m.detail.issue = &iss
	m.detail.scopePID = 7
	m.focus = focusDetail
	// Seed the label cache so the menu has a known row to find.
	m.projectLabels.byProject[7] = labelCacheEntry{
		pid: 7, gen: 1,
		labels: []LabelCount{{Label: "alpha", Count: 1}},
	}
	m, _ = m.openInput(inputLabelPrompt)
	got := m.View()
	// Look for the unique menu content "alpha (1)" — it shouldn't
	// appear on the left side of the screen (which is the list
	// pane), only inside the detail pane (column >= splitListPaneWidth).
	idx := strings.Index(got, "alpha (1)")
	if idx < 0 {
		t.Fatalf("menu content not found in output:\n%s", got)
	}
	// Find the column of "alpha (1)" within its line.
	lineStart := strings.LastIndex(got[:idx], "\n") + 1
	col := idx - lineStart
	if col < splitListPaneWidth {
		t.Errorf("suggest menu content at column %d, want >= %d (list pane width)",
			col, splitListPaneWidth)
	}
}

// TestSplit_LayoutFlip_FromStackedToSplitFromList: stacked viewList
// resized up to split → focus goes to focusList, view stays viewList,
// selection survives.
func TestSplit_LayoutFlip_FromStackedToSplitFromList(t *testing.T) {
	m, cleanup := splitTestSetup(t)
	defer cleanup()
	// Already in split mode from setup — flip back to stacked first.
	out, _ := m.Update(tea.WindowSizeMsg{Width: 100, Height: 40})
	m = out.(Model)
	if m.layout != layoutStacked {
		t.Fatalf("setup failed: layout=%v want layoutStacked", m.layout)
	}
	m.list.selectedNumber = 7
	out, _ = m.Update(tea.WindowSizeMsg{Width: 160, Height: 40})
	m = out.(Model)
	if m.layout != layoutSplit {
		t.Errorf("layout=%v after resize up, want layoutSplit", m.layout)
	}
	if m.focus != focusList {
		t.Errorf("focus=%v want focusList", m.focus)
	}
	if m.list.selectedNumber != 7 {
		t.Errorf("selectedNumber=%d want 7", m.list.selectedNumber)
	}
}
