package tui

import (
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"
)

// labelPromptFixture builds a Model with an open label prompt and a
// pre-populated suggestion cache. Used by the interaction tests so
// they exercise key dispatch against a populated menu.
func labelPromptFixture() Model {
	pid := int64(7)
	m := Model{
		view:          viewDetail,
		scope:         scope{projectID: pid},
		projectLabels: newLabelCache(),
		toastNow:      func() time.Time { return time.Time{} },
	}
	m.detail.scopePID = pid
	m.detail.gen = 1
	m.detail.issue = &Issue{ProjectID: pid, Number: 42, Status: "open"}
	m.projectLabels.byProject[pid] = labelCacheEntry{
		pid: pid, gen: 1,
		labels: []LabelCount{
			{Label: "alpha", Count: 5},
			{Label: "beta", Count: 3},
			{Label: "gamma", Count: 1},
		},
	}
	m.input = newPanelPrompt(inputLabelPrompt, formTarget{
		projectID: pid, issueNumber: 42, detailGen: 1,
	})
	return m
}

// TestLabelPrompt_ArrowKeys_MoveHighlight_WithWrap: pressing ↓ four
// times wraps from index 0 → 1 → 2 → 0 (3 entries). Then ↑ wraps
// 0 → 2.
func TestLabelPrompt_ArrowKeys_MoveHighlight_WithWrap(t *testing.T) {
	m := labelPromptFixture()
	// Down to 1.
	out, _ := m.Update(tea.KeyMsg{Type: tea.KeyDown})
	m = out.(Model)
	if got := m.input.suggestHighlight; got != 1 {
		t.Fatalf("highlight = %d after 1 down, want 1", got)
	}
	// Down to 2.
	out, _ = m.Update(tea.KeyMsg{Type: tea.KeyDown})
	m = out.(Model)
	if got := m.input.suggestHighlight; got != 2 {
		t.Fatalf("highlight = %d after 2 down, want 2", got)
	}
	// Down wraps to 0.
	out, _ = m.Update(tea.KeyMsg{Type: tea.KeyDown})
	m = out.(Model)
	if got := m.input.suggestHighlight; got != 0 {
		t.Fatalf("highlight = %d after wrap-down, want 0", got)
	}
	// Up wraps from 0 → 2.
	out, _ = m.Update(tea.KeyMsg{Type: tea.KeyUp})
	m = out.(Model)
	if got := m.input.suggestHighlight; got != 2 {
		t.Fatalf("highlight = %d after wrap-up, want 2", got)
	}
}

// TestLabelPrompt_TabCompletesHighlightedSuggestion: with the
// highlight on entry index 1 (beta — second after sort), pressing
// Tab fills the buffer with "beta".
func TestLabelPrompt_TabCompletesHighlightedSuggestion(t *testing.T) {
	m := labelPromptFixture()
	// Move highlight to beta (sorted by count desc: alpha=5, beta=3,
	// gamma=1, so beta is index 1).
	out, _ := m.Update(tea.KeyMsg{Type: tea.KeyDown})
	m = out.(Model)
	out, _ = m.Update(tea.KeyMsg{Type: tea.KeyTab})
	m = out.(Model)
	if got := m.input.activeField().value(); got != "beta" {
		t.Fatalf("buffer = %q after tab on beta, want %q", got, "beta")
	}
}

// TestLabelPrompt_EmptyBuffer_ShowsTopProjectLabels: with an empty
// buffer, the suggestion source is unfiltered and the count-desc
// sort applies (alpha=5 first).
func TestLabelPrompt_EmptyBuffer_ShowsTopProjectLabels(t *testing.T) {
	m := labelPromptFixture()
	got := filterSuggestions(m.suggestionsForPrompt(m.input), "")
	if len(got) != 3 {
		t.Fatalf("len = %d, want 3", len(got))
	}
	if got[0].Label != "alpha" {
		t.Fatalf("first label = %q, want %q (count-desc sort)",
			got[0].Label, "alpha")
	}
}

// TestLabelPrompt_PrefixFilterCaseInsensitive: an "AL" prefix
// matches "alpha" but not "beta" (case-insensitive).
func TestLabelPrompt_PrefixFilterCaseInsensitive(t *testing.T) {
	m := labelPromptFixture()
	got := filterSuggestions(m.suggestionsForPrompt(m.input), "AL")
	if len(got) != 1 {
		t.Fatalf("len = %d, want 1 for prefix AL", len(got))
	}
	if got[0].Label != "alpha" {
		t.Fatalf("got %q, want %q", got[0].Label, "alpha")
	}
}

// TestRemoveLabelPrompt_SourceIsAttachedLabelsNotProjectCache: the
// `-` prompt sources from dm.issue.Labels, NOT from the project
// cache. Even with a populated cache, the suggestion list reflects
// only what's currently attached.
func TestRemoveLabelPrompt_SourceIsAttachedLabelsNotProjectCache(t *testing.T) {
	m := labelPromptFixture()
	m.detail.issue.Labels = []string{"attached1", "attached2"}
	m.input = newPanelPrompt(inputRemoveLabelPrompt, formTarget{
		projectID: 7, issueNumber: 42,
	})
	got := m.suggestionsForPrompt(m.input)
	if len(got) != 2 {
		t.Fatalf("len = %d, want 2", len(got))
	}
	if got[0].Label != "attached1" || got[1].Label != "attached2" {
		t.Fatalf("got %+v, want attached1 + attached2", got)
	}
	// And the cache labels (alpha/beta/gamma) must not bleed in.
	for _, lc := range got {
		if lc.Label == "alpha" || lc.Label == "beta" {
			t.Fatalf("removeLabelPrompt source leaked project cache: %+v", got)
		}
	}
}

// TestLabelPrompt_EnterCommitsCurrentBuffer: pressing Enter with a
// free-typed buffer dispatches the label-add mutation (commit
// closes the input and routes through commitInput → dispatchLabel).
func TestLabelPrompt_EnterCommitsCurrentBuffer(t *testing.T) {
	m := labelPromptFixture()
	m.input.activeField().input.SetValue("freshlabel")
	m.input.fields[0] = *m.input.activeField()
	out, _ := m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	nm := out.(Model)
	if nm.input.kind != inputNone {
		t.Fatalf("input kind after enter = %v, want inputNone", nm.input.kind)
	}
}

// TestLabelPrompt_EscClosesPromptAndMenu: esc cancels the input,
// closing both the prompt and the menu.
func TestLabelPrompt_EscClosesPromptAndMenu(t *testing.T) {
	m := labelPromptFixture()
	out, _ := m.Update(tea.KeyMsg{Type: tea.KeyEsc})
	nm := out.(Model)
	if nm.input.kind != inputNone {
		t.Fatalf("input kind after esc = %v, want inputNone", nm.input.kind)
	}
}

// TestSuggestMenu_HeightSubtractedFromTabBudget: when the menu is
// open, the detail-view layout reduces tabA by the menu's rendered
// height. Test against renderInfoLine's indicator — with the menu
// occupying 5 rows, the tab budget shrinks and the indicator math
// reflects the smaller window.
func TestSuggestMenu_HeightSubtractedFromTabBudget(t *testing.T) {
	defer snapshotInit(t)()
	m := labelPromptFixture()
	dm := m.detail
	// Add comments so the tab is non-empty (otherwise no indicator).
	dm.comments = []CommentEntry{
		{ID: 1, Author: "a", Body: "x"},
		{ID: 2, Author: "b", Body: "y"},
		{ID: 3, Author: "c", Body: "z"},
	}
	dm.activeTab = tabComments
	chrome := m.chrome()
	// Without the menu: tabA at height 30 → 9 reserved → ~21 slack →
	// 14 body + 7 tab. With menu: subtract menuH from slack.
	withMenu := dm.View(120, 30, chrome)
	noMenu := dm.View(120, 30, viewChrome{})
	// The menu present means fewer tab rows; verify the rendered
	// view differs (the menu overlay isn't in this string but the
	// reservation reduces tabA, so renderInfoLine's chunk window
	// changes).
	if withMenu == noMenu {
		t.Fatalf("layout did not differ with vs. without label prompt menu")
	}
}
