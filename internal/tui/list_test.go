package tui

import (
	"strings"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/x/exp/teatest"
)

// twoIssueFixture is the on-screen seed for the list tests. Each test
// feeds it via initialFetchMsg so the rendering layer is exercised
// without booting a real daemon.
func twoIssueFixture() []Issue {
	open := "claude-4.7"
	closed := "wesm"
	return []Issue{
		{Number: 1, Title: "fix login bug on Safari", Status: "open", Owner: &open},
		{Number: 2, Title: "rebuild search index", Status: "closed", Owner: &closed},
	}
}

// TestList_Render_Fixture confirms the seed reaches the screen so the
// rendering layer can be reviewed independent of the network layer.
func TestList_Render_Fixture(t *testing.T) {
	m := initialModel(Options{})
	tm := teatest.NewTestModel(t, m, teatest.WithInitialTermSize(120, 30))
	tm.Send(tea.WindowSizeMsg{Width: 120, Height: 30})
	tm.Send(initialFetchMsg{issues: twoIssueFixture()})
	teatest.WaitFor(t, tm.Output(), func(b []byte) bool {
		return strings.Contains(string(b), "fix login bug on Safari")
	}, teatest.WithDuration(2*time.Second))
	tm.Send(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'q'}})
	tm.WaitFinished(t)
}

// TestList_Cursor_DownAndUp drives j/j/k against a three-row fixture and
// asserts the marker glyph lands on the row containing #2. The third row
// gives the down-clamp room to move; with two rows j/j/k would land on
// index 0 because cursor never reaches 2. lipgloss/table pads between
// columns, so we scan output line-by-line for one that contains both the
// marker and the row's issue number.
func TestList_Cursor_DownAndUp(t *testing.T) {
	m := initialModel(Options{})
	tm := teatest.NewTestModel(t, m, teatest.WithInitialTermSize(120, 30))
	tm.Send(tea.WindowSizeMsg{Width: 120, Height: 30})
	tm.Send(initialFetchMsg{issues: []Issue{
		{Number: 1, Title: "fix login bug on Safari", Status: "open"},
		{Number: 2, Title: "rebuild search index", Status: "closed"},
		{Number: 3, Title: "third row", Status: "open"},
	}})
	teatest.WaitFor(t, tm.Output(), func(b []byte) bool {
		return strings.Contains(string(b), "third row")
	}, teatest.WithDuration(2*time.Second))
	tm.Send(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'j'}})
	tm.Send(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'j'}})
	tm.Send(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'k'}})
	teatest.WaitFor(t, tm.Output(), func(b []byte) bool {
		for _, line := range strings.Split(string(b), "\n") {
			if strings.Contains(line, "›") && strings.Contains(line, "#2") {
				return true
			}
		}
		return false
	}, teatest.WithDuration(2*time.Second))
	tm.Send(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'q'}})
	tm.WaitFinished(t)
}
