package tui

import (
	"errors"
	"strings"
	"testing"
)

// TestSuggestMenu_RendersEntries: a populated cache produces a menu
// with one row per suggestion (top + bottom border + N entry rows).
func TestSuggestMenu_RendersEntries(t *testing.T) {
	defer snapshotInit(t)()
	s := newPanelPrompt(inputLabelPrompt, formTarget{
		projectID: 7, issueNumber: 1,
	})
	suggestions := []LabelCount{
		{Label: "bug", Count: 5},
		{Label: "design", Count: 3},
	}
	got := renderSuggestMenu(s, suggestions, labelCacheEntry{})
	if !strings.Contains(got, "bug") {
		t.Fatalf("menu missing 'bug': %q", got)
	}
	if !strings.Contains(got, "design") {
		t.Fatalf("menu missing 'design': %q", got)
	}
}

// TestSuggestMenu_LoadingPlaceholder: a fetching=true entry with no
// labels renders the loading placeholder instead of an empty list.
func TestSuggestMenu_LoadingPlaceholder(t *testing.T) {
	defer snapshotInit(t)()
	s := newPanelPrompt(inputLabelPrompt, formTarget{
		projectID: 7, issueNumber: 1,
	})
	got := renderSuggestMenu(s, nil, labelCacheEntry{
		pid: 7, gen: 1, fetching: true,
	})
	if !strings.Contains(got, "loading") {
		t.Fatalf("menu missing 'loading' placeholder: %q", got)
	}
}

// TestSuggestMenu_ErrorPlaceholder: an entry with a non-nil err
// surfaces the error message in the menu body.
func TestSuggestMenu_ErrorPlaceholder(t *testing.T) {
	defer snapshotInit(t)()
	s := newPanelPrompt(inputLabelPrompt, formTarget{
		projectID: 7, issueNumber: 1,
	})
	got := renderSuggestMenu(s, nil, labelCacheEntry{
		pid: 7, gen: 1, err: errors.New("daemon 500"),
	})
	if !strings.Contains(got, "daemon 500") {
		t.Fatalf("menu missing error message: %q", got)
	}
	if !strings.Contains(got, "error") {
		t.Fatalf("menu missing error label: %q", got)
	}
}

// TestSuggestMenu_EmptyPlaceholder: a fetched entry with zero labels
// surfaces the "no labels" hint so the user knows the project has
// no labels yet (rather than a confusingly-empty menu).
func TestSuggestMenu_EmptyPlaceholder(t *testing.T) {
	defer snapshotInit(t)()
	s := newPanelPrompt(inputLabelPrompt, formTarget{
		projectID: 7, issueNumber: 1,
	})
	got := renderSuggestMenu(s, nil, labelCacheEntry{
		pid: 7, gen: 1, fetching: false,
	})
	if !strings.Contains(got, "no labels") {
		t.Fatalf("menu missing empty placeholder: %q", got)
	}
}

// TestSuggestMenu_Scrolls_HighlightStaysVisible: with N > maxRows
// suggestions and the highlight at the end, the visible window
// scrolls so the highlighted row is rendered (would be off-screen
// without the windowing).
func TestSuggestMenu_Scrolls_HighlightStaysVisible(t *testing.T) {
	defer snapshotInit(t)()
	s := newPanelPrompt(inputLabelPrompt, formTarget{
		projectID: 7, issueNumber: 1,
	})
	s.suggestHighlight = 9 // out past the menu's row budget
	suggestions := make([]LabelCount, 12)
	for i := range suggestions {
		suggestions[i] = LabelCount{
			Label: "lbl-" + ptrFormat(int64(i+1)),
			Count: int64(12 - i),
		}
	}
	got := renderSuggestMenu(s, suggestions, labelCacheEntry{})
	if !strings.Contains(got, "lbl-10") {
		t.Fatalf("scroll window did not include highlighted row "+
			"(lbl-10): %q", got)
	}
	// And the first entries (which would render without scroll)
	// should NOT be present once the window has scrolled past them.
	if strings.Contains(got, "lbl-1\n") || strings.Contains(got, "lbl-1 ") {
		t.Fatalf("scroll window did not scroll past lbl-1: %q", got)
	}
}

// TestFilterSuggestions_PrefixCaseInsensitive: prefix filter matches
// case-insensitively and ignores leading whitespace in the prefix.
func TestFilterSuggestions_PrefixCaseInsensitive(t *testing.T) {
	all := []LabelCount{
		{Label: "Bug", Count: 5},
		{Label: "design", Count: 3},
		{Label: "BugFix", Count: 2},
	}
	got := filterSuggestions(all, "BUG")
	if len(got) != 2 {
		t.Fatalf("want 2 matches for BUG, got %d: %+v", len(got), got)
	}
}

// TestFilterSuggestions_SortsByCountThenLabel: count desc primary,
// label asc secondary. Ties on count fall to alphabetical.
func TestFilterSuggestions_SortsByCountThenLabel(t *testing.T) {
	all := []LabelCount{
		{Label: "z", Count: 1},
		{Label: "a", Count: 5},
		{Label: "m", Count: 5},
		{Label: "b", Count: 1},
	}
	got := filterSuggestions(all, "")
	want := []string{"a", "m", "b", "z"}
	if len(got) != len(want) {
		t.Fatalf("len = %d, want %d", len(got), len(want))
	}
	for i, w := range want {
		if got[i].Label != w {
			t.Fatalf("got[%d].Label = %q, want %q (full: %+v)",
				i, got[i].Label, w, got)
		}
	}
}

// TestOverlayAtCorner_PlacesAtAnchor: a 1x1 panel placed at (row, col)
// shows up in the bg at the right cell.
func TestOverlayAtCorner_PlacesAtAnchor(t *testing.T) {
	bg := strings.Join([]string{
		"......",
		"......",
		"......",
	}, "\n")
	got := overlayAtCorner(bg, "X", 6, 3, 1, 2)
	want := strings.Join([]string{
		"......",
		"..X...",
		"......",
	}, "\n")
	if got != want {
		t.Fatalf("got:\n%s\nwant:\n%s", got, want)
	}
}

// TestSuggestMenuHeight_CountsBordersAndBody: the height includes
// the top/bottom borders + body rows (max of visible entries vs.
// placeholder rows).
func TestSuggestMenuHeight_CountsBordersAndBody(t *testing.T) {
	defer snapshotInit(t)()
	s := newPanelPrompt(inputLabelPrompt, formTarget{
		projectID: 7, issueNumber: 1,
	})
	// Empty cache: 1 placeholder row + 2 borders = 3.
	if got := suggestMenuHeight(s, nil, labelCacheEntry{}); got != 3 {
		t.Fatalf("empty-cache height = %d, want 3", got)
	}
	// 4 entries: 4 body rows + 2 borders = 6.
	suggestions := make([]LabelCount, 4)
	for i := range suggestions {
		suggestions[i] = LabelCount{Label: "x" + ptrFormat(int64(i)), Count: 1}
	}
	if got := suggestMenuHeight(s, suggestions, labelCacheEntry{}); got != 6 {
		t.Fatalf("4-entry height = %d, want 6", got)
	}
}
