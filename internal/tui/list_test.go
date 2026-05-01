package tui

import (
	"strings"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/x/exp/teatest"
)

// TestList_Render_Fixture confirms the fixture rows reach the screen so
// the rendering layer can be reviewed independent of the network layer.
func TestList_Render_Fixture(t *testing.T) {
	m := initialModel(Options{})
	tm := teatest.NewTestModel(t, m, teatest.WithInitialTermSize(120, 30))
	tm.Send(tea.WindowSizeMsg{Width: 120, Height: 30})
	teatest.WaitFor(t, tm.Output(), func(b []byte) bool {
		return strings.Contains(string(b), "fix login bug on Safari")
	}, teatest.WithDuration(2*time.Second))
	tm.Send(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'q'}})
	tm.WaitFinished(t)
}

// TestList_Cursor_DownAndUp drives j/j/k against the same fixture and
// asserts the marker glyph lands on the row containing #2. lipgloss/table
// pads between columns, so we scan output line-by-line for one that
// contains both the marker and the row's issue number.
func TestList_Cursor_DownAndUp(t *testing.T) {
	m := initialModel(Options{})
	m.list.cursor = 0
	tm := teatest.NewTestModel(t, m, teatest.WithInitialTermSize(120, 30))
	tm.Send(tea.WindowSizeMsg{Width: 120, Height: 30})
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
