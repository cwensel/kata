package tui

import (
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// updateGoldens enables `go test ./internal/tui/ -update-goldens` so the
// snapshot tests rewrite their .txt fixtures rather than asserting. We
// use a custom name (not -update) so a future go test flag of that name
// would not collide with this one. The flag is package-level because
// every snapshot test reads it through assertGolden.
var updateGoldens = flag.Bool(
	"update-goldens", false, "update golden snapshot files",
)

// snapshotFixedNow is the wall-clock the snapshot tests freeze
// renderNow to so the "Nh ago" column stays deterministic across runs.
// Chosen to be deep in the future so any "X ago" deltas computed
// against fixture timestamps are stable. Tests that depend on the
// "ago" math compute fixture timestamps relative to this constant.
var snapshotFixedNow = time.Date(2026, 5, 1, 12, 0, 0, 0, time.UTC)

// snapshotInit prepares the package for a deterministic snapshot run:
//   - KATA_COLOR_MODE=none strips ANSI from rendered output, so the
//     golden files are plain UTF-8 text with no escape sequences.
//   - applyDefaultColorMode rebuilds the package-level styles against
//     io.Discard with the just-set env var so the new mode takes effect.
//   - renderNow is frozen at snapshotFixedNow; the caller restores the
//     prior renderNow when the test ends so other tests see time.Now.
//
// Returns a cleanup func; tests defer it.
func snapshotInit(t *testing.T) func() {
	t.Helper()
	t.Setenv("KATA_COLOR_MODE", "none")
	t.Setenv("NO_COLOR", "")
	applyDefaultColorMode(io.Discard)
	prior := renderNow
	renderNow = func() time.Time { return snapshotFixedNow }
	return func() {
		renderNow = prior
		applyDefaultColorMode(io.Discard)
	}
}

// assertGolden compares got against testdata/golden/<name>.txt. With
// -update-goldens, the file is (re)written with got. Without it, a
// missing file is a hard failure with a hint to regenerate; a mismatch
// shows both want and got verbatim so the diff is reviewable. The
// gosec hints (G301/G304/G306) are expected here: the test owns the
// path under testdata/golden/, the file is read-only fixture data, and
// 0o750/0o600 are sufficient for fixtures committed to the repo.
func assertGolden(t *testing.T, name, got string) {
	t.Helper()
	path := filepath.Join("testdata", "golden", name+".txt")
	if *updateGoldens {
		if err := os.MkdirAll(filepath.Dir(path), 0o750); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(got), 0o600); err != nil {
			t.Fatal(err)
		}
		return
	}
	want, err := os.ReadFile(path) //nolint:gosec // path is under testdata/golden/
	if err != nil {
		t.Fatalf(
			"missing golden %q: %v (run `go test ./internal/tui/ "+
				"-update-goldens` to create)", name, err,
		)
	}
	if got != string(want) {
		t.Errorf(
			"%s golden mismatch:\n--- want ---\n%s\n--- got ---\n%s",
			name, string(want), got,
		)
	}
}

// snapListFixture mirrors listFixture but pegs every UpdatedAt at a
// known offset from snapshotFixedNow so humanizeRelative renders
// deterministic deltas. The deleted row also has a fixed DeletedAt.
func snapListFixture() []Issue {
	return []Issue{
		{
			Number: 1, Title: "fix login bug on Safari",
			Status: "open", Owner: ptrString("claude-4.7"),
			UpdatedAt: snapshotFixedNow.Add(-3 * time.Hour),
		},
		{
			Number: 2, Title: "rebuild search index",
			Status: "closed", Owner: ptrString("wesm"),
			UpdatedAt: snapshotFixedNow.Add(-1 * time.Hour),
		},
		{
			Number: 3, Title: "purge stale tokens",
			Status:    "open",
			DeletedAt: ptrTime(snapshotFixedNow.Add(-2 * time.Hour)),
			UpdatedAt: snapshotFixedNow.Add(-2 * time.Hour),
		},
	}
}

// snapDetailFixture builds a detailModel with two comments, two events,
// and one link so each tab snapshot has data to render. All timestamps
// are absolute so fmtTime produces the same output every run.
func snapDetailFixture() detailModel {
	when := time.Date(2026, 4, 30, 10, 0, 0, 0, time.UTC)
	iss := Issue{
		ProjectID: 7, Number: 42, Title: "fix login bug on Safari",
		Status: "open", Author: "wesm",
		Body: "Reproduces in Safari 17 only.\nClick the login button twice.",
	}
	return detailModel{
		issue:    &iss,
		scopePID: 7,
		comments: []CommentEntry{
			{ID: 1, Author: "alice", Body: "I can repro on macOS.", CreatedAt: when},
			{ID: 2, Author: "bob", Body: "Looks like a race in oauth.", CreatedAt: when.Add(time.Hour)},
		},
		events: []EventLogEntry{
			{ID: 9, Type: "issue.created", Actor: "alice", CreatedAt: when},
			{ID: 10, Type: "issue.commented", Actor: "bob", CreatedAt: when.Add(time.Hour)},
		},
		links: []LinkEntry{
			{
				ID: 100, Type: "blocks", FromNumber: 42, ToNumber: 7,
				Author: "wesm", CreatedAt: when,
			},
		},
	}
}

// TestSnapshot_List_DefaultMixedStatus locks down the steady-state list
// view at width 120 with three rows and the cursor on row 2. The fixture
// covers open, closed, and soft-deleted statusChip branches.
func TestSnapshot_List_DefaultMixedStatus(t *testing.T) {
	defer snapshotInit(t)()
	lm := newListModel()
	lm.loading = false
	lm.issues = snapListFixture()
	lm.cursor = 1
	chrome := viewChrome{
		scope:     scope{projectID: 7, projectName: "kata"},
		sseStatus: sseConnected,
		version:   "v0.1.0",
	}
	got := lm.View(120, 30, chrome)
	assertGolden(t, "list-default-mixed-status", got)
}

// All snapshot tests run under KATA_COLOR_MODE=none for deterministic
// goldens (the renderer writes through io.Discard, which is not a TTY,
// so lipgloss strips ANSI even in colored modes). The plan called for a
// separate list-no-color-mode snapshot, but it would be byte-identical
// to list-default-mixed-status under the harness above and add no
// coverage. ANSI suppression is verified directly by theme_test.go's
// TestApplyColorMode_NoneStripsForeground; the snapshot suite locks
// down rendering shape, not color escape semantics.

// TestSnapshot_List_EmptyAfterFilter exercises the "no rows visible"
// branch of renderBody: a search filter narrows everything out so the
// empty-state hint appears.
func TestSnapshot_List_EmptyAfterFilter(t *testing.T) {
	defer snapshotInit(t)()
	lm := newListModel()
	lm.loading = false
	lm.issues = snapListFixture()
	lm.filter = ListFilter{Search: "no-match-anywhere"}
	chrome := viewChrome{
		scope:     scope{projectID: 7, projectName: "kata"},
		sseStatus: sseConnected,
		version:   "v0.1.0",
	}
	got := lm.View(120, 30, chrome)
	assertGolden(t, "list-empty-after-filter", got)
}

// TestSnapshot_QuitConfirmModal covers the M3.5b quit-confirm
// modal overlaid on a list view. The modal sits centered over the
// rendered background; underlying content stays painted around it.
func TestSnapshot_QuitConfirmModal(t *testing.T) {
	defer snapshotInit(t)()
	lm := newListModel()
	lm.loading = false
	lm.issues = snapListFixture()
	lm.cursor = 1
	chrome := viewChrome{
		scope:     scope{projectID: 7, projectName: "kata"},
		sseStatus: sseConnected,
		version:   "v0.1.0",
	}
	bg := lm.View(120, 30, chrome)
	got := overlayModal(bg, renderQuitConfirmModal(), 120, 30)
	assertGolden(t, "quit-confirm-modal", got)
}

// TestSnapshot_List_SearchBarActive covers the inline command bar
// in place of the chip strip when chrome.input.kind == inputSearchBar.
// The footer help row swaps to the bar's enter/esc/ctrl+u keys.
func TestSnapshot_List_SearchBarActive(t *testing.T) {
	defer snapshotInit(t)()
	lm := newListModel()
	lm.loading = false
	lm.issues = snapListFixture()
	chrome := viewChrome{
		scope:     scope{projectID: 7, projectName: "kata"},
		sseStatus: sseConnected,
		version:   "v0.1.0",
		input:     newSearchBar(ListFilter{Search: "login"}),
	}
	got := lm.View(120, 30, chrome)
	assertGolden(t, "list-search-bar-active", got)
}

// TestSnapshot_List_ScrollIndicator covers the scroll-indicator slot
// in the footer status line. With 50 issues and a 30-row terminal, the
// chrome reserves enough rows that not every issue fits — the
// indicator surfaces as `[start-end of N issues]` aligned right.
func TestSnapshot_List_ScrollIndicator(t *testing.T) {
	defer snapshotInit(t)()
	lm := newListModel()
	lm.loading = false
	issues := make([]Issue, 50)
	for i := range issues {
		issues[i] = Issue{
			Number: int64(i + 1),
			Title:  "issue " + ptrFormat(int64(i+1)),
			Status: "open",
			UpdatedAt: snapshotFixedNow.Add(
				-time.Duration(i+1) * time.Hour,
			),
		}
	}
	lm.issues = issues
	lm.cursor = 25 // mid-list so the scroll window has both start and end visible
	chrome := viewChrome{
		scope:     scope{projectID: 7, projectName: "kata"},
		sseStatus: sseConnected,
		version:   "v0.1.0",
	}
	got := lm.View(120, 30, chrome)
	assertGolden(t, "list-scroll-indicator", got)
}

// ptrFormat is a tiny helper for fixture row titles that need the
// issue number embedded — keeps the fixture builder readable.
func ptrFormat(n int64) string {
	return fmt.Sprintf("%d", n)
}

func TestSnapshot_List_WithFilterChips(t *testing.T) {
	defer snapshotInit(t)()
	lm := newListModel()
	lm.loading = false
	lm.issues = []Issue{{
		Number: 1, Title: "narrowed by chips", Status: "open",
		Owner:     ptrString("alice"),
		UpdatedAt: snapshotFixedNow.Add(-30 * time.Minute),
	}}
	lm.filter = ListFilter{Status: "open", Owner: "alice"}
	chrome := viewChrome{
		scope:     scope{projectID: 7, projectName: "kata"},
		sseStatus: sseConnected,
		version:   "v0.1.0",
	}
	got := lm.View(120, 30, chrome)
	assertGolden(t, "list-with-filter-chips", got)
}

// TestSnapshot_Detail_WithLabelPrompt covers the M3b panel-local
// prompt rendered at the bottom of the detail pane.
func TestSnapshot_Detail_WithLabelPrompt(t *testing.T) {
	defer snapshotInit(t)()
	dm := snapDetailFixture()
	chrome := viewChrome{
		input: newPanelPrompt(inputLabelPrompt, formTarget{
			projectID: dm.scopePID, issueNumber: dm.issue.Number,
		}),
	}
	got := dm.View(120, 30, chrome)
	assertGolden(t, "detail-with-label-prompt", got)
}

// TestSnapshot_Detail_LongCommentsList locks the per-tab scroll
// indicator: 30 comments on a 30-row terminal forces the visible
// window to slice into the entries, and the footer shows
// `[start-end of N comments]`.
func TestSnapshot_Detail_LongCommentsList(t *testing.T) {
	defer snapshotInit(t)()
	dm := snapDetailFixture()
	when := time.Date(2026, 4, 30, 10, 0, 0, 0, time.UTC)
	cs := make([]CommentEntry, 30)
	for i := range cs {
		cs[i] = CommentEntry{
			ID:        int64(i + 1),
			Author:    "actor-" + ptrFormat(int64(i+1)),
			Body:      "comment body " + ptrFormat(int64(i+1)),
			CreatedAt: when.Add(time.Duration(i) * time.Minute),
		}
	}
	dm.comments = cs
	dm.activeTab = tabComments
	dm.tabCursor = 14 // mid-list
	got := dm.View(120, 30, viewChrome{})
	assertGolden(t, "detail-long-comments-list", got)
}

// TestSnapshot_Detail_CommentsTab locks the comments tab render. Tab
// strip shows Comments highlighted; the entry list contains the two
// fixture comments with author, timestamp, and indented body lines.
func TestSnapshot_Detail_CommentsTab(t *testing.T) {
	defer snapshotInit(t)()
	dm := snapDetailFixture()
	dm.activeTab = tabComments
	got := dm.View(120, 30, viewChrome{})
	assertGolden(t, "detail-comments-tab", got)
}

// TestSnapshot_Detail_EventsTab same fixture, events tab active.
func TestSnapshot_Detail_EventsTab(t *testing.T) {
	defer snapshotInit(t)()
	dm := snapDetailFixture()
	dm.activeTab = tabEvents
	got := dm.View(120, 30, viewChrome{})
	assertGolden(t, "detail-events-tab", got)
}

// TestSnapshot_Detail_LinksTab same fixture, links tab active.
func TestSnapshot_Detail_LinksTab(t *testing.T) {
	defer snapshotInit(t)()
	dm := snapDetailFixture()
	dm.activeTab = tabLinks
	got := dm.View(120, 30, viewChrome{})
	assertGolden(t, "detail-links-tab", got)
}

// TestSnapshot_Detail_WithLabels exercises the assignment row's chip
// strip on a wide terminal: owner left, three sorted chips right.
func TestSnapshot_Detail_WithLabels(t *testing.T) {
	defer snapshotInit(t)()
	dm := snapDetailFixture()
	dm.issue.Owner = ptrString("alice")
	dm.issue.Labels = []string{"prio-1", "bug", "needs-design"}
	got := dm.View(120, 30, viewChrome{})
	assertGolden(t, "detail-with-labels", got)
}

// TestSnapshot_Detail_LabelsNarrow_OverflowAndDegrade verifies the
// chip strip degrades gracefully on narrow terminals: at 60 cells
// the +N overflow appears; at 30 cells the strip collapses to the
// `[N labels]` ultra-narrow fallback.
func TestSnapshot_Detail_LabelsNarrow_OverflowAndDegrade(t *testing.T) {
	defer snapshotInit(t)()
	dm := snapDetailFixture()
	dm.issue.Owner = ptrString("alice")
	dm.issue.Labels = []string{
		"alpha-pretty-long", "beta-pretty-long", "gamma-pretty-long",
		"delta-pretty-long", "epsilon-pretty-long",
	}
	overflow := dm.View(60, 24, viewChrome{})
	if !strings.Contains(overflow, "+") {
		t.Fatalf("expected +N overflow at width 60, got:\n%s", overflow)
	}
	degrade := dm.View(30, 24, viewChrome{})
	if !strings.Contains(degrade, "labels]") {
		t.Fatalf("expected [N labels] degrade at width 30, got:\n%s", degrade)
	}
}

// TestSnapshot_Help_Narrow renders help at width 60 (helpColumnCount=1).
// Sections stack vertically.
func TestSnapshot_Help_Narrow(t *testing.T) {
	defer snapshotInit(t)()
	got := renderHelp(newKeymap(), 60, ListFilter{})
	assertGolden(t, "help-narrow", got)
}

// TestSnapshot_Help_Wide renders help at width 120 (helpColumnCount=3).
// Sections lay out side-by-side.
func TestSnapshot_Help_Wide(t *testing.T) {
	defer snapshotInit(t)()
	got := renderHelp(newKeymap(), 120, ListFilter{})
	assertGolden(t, "help-wide", got)
}

// TestSnapshot_Empty renders the onboarding empty state at 80x24.
func TestSnapshot_Empty(t *testing.T) {
	defer snapshotInit(t)()
	got := renderEmpty(80, 24)
	assertGolden(t, "empty-state", got)
}

// TestSnapshot_NarrowTerminalHint locks the M5 degraded hint at the
// canonical narrow fixture (60x24 — below the 80-cell width
// threshold). The bordered panel sits centered; q/ctrl+c routing is
// unaffected (covered by narrow_terminal_test.go).
func TestSnapshot_NarrowTerminalHint(t *testing.T) {
	defer snapshotInit(t)()
	got := renderTooNarrow(60, 24)
	assertGolden(t, "narrow-terminal-hint", got)
}

// TestSnapshot_LabelPrompt_MenuOpen renders the autocomplete menu
// for a `+` label prompt with 5 suggestions and the highlight on
// the first row. Pinned to 120x30 like the other detail snapshots.
func TestSnapshot_LabelPrompt_MenuOpen(t *testing.T) {
	defer snapshotInit(t)()
	m := snapLabelPromptModel()
	m.projectLabels.byProject[7] = labelCacheEntry{
		pid: 7, gen: 1,
		labels: []LabelCount{
			{Label: "alpha", Count: 5},
			{Label: "beta", Count: 4},
			{Label: "gamma", Count: 3},
			{Label: "delta", Count: 2},
			{Label: "epsilon", Count: 1},
		},
	}
	got := m.View()
	assertGolden(t, "label-prompt-menu-open", got)
}

// TestSnapshot_LabelPrompt_Loading renders the loading-placeholder
// menu state — the cache is fetching but has no entries yet.
func TestSnapshot_LabelPrompt_Loading(t *testing.T) {
	defer snapshotInit(t)()
	m := snapLabelPromptModel()
	m.projectLabels.byProject[7] = labelCacheEntry{
		pid: 7, gen: 1, fetching: true,
	}
	got := m.View()
	assertGolden(t, "label-prompt-loading", got)
}

// TestSnapshot_LabelPrompt_Error renders the error-placeholder menu
// state — the cache has an err and no labels.
func TestSnapshot_LabelPrompt_Error(t *testing.T) {
	defer snapshotInit(t)()
	m := snapLabelPromptModel()
	m.projectLabels.byProject[7] = labelCacheEntry{
		pid: 7, gen: 1, err: errStub("daemon 500"),
	}
	got := m.View()
	assertGolden(t, "label-prompt-error", got)
}

// TestSnapshot_LabelPrompt_Empty renders the empty-placeholder menu
// state — the cache fetched, has no entries, no error.
func TestSnapshot_LabelPrompt_Empty(t *testing.T) {
	defer snapshotInit(t)()
	m := snapLabelPromptModel()
	m.projectLabels.byProject[7] = labelCacheEntry{
		pid: 7, gen: 1, fetching: false,
	}
	got := m.View()
	assertGolden(t, "label-prompt-empty", got)
}

// TestSnapshot_LabelPrompt_Scroll renders the menu with 12
// suggestions and the highlight at index 9 — the visible window
// scrolls past the first entries.
func TestSnapshot_LabelPrompt_Scroll(t *testing.T) {
	defer snapshotInit(t)()
	m := snapLabelPromptModel()
	suggestions := make([]LabelCount, 12)
	for i := range suggestions {
		suggestions[i] = LabelCount{
			Label: "lbl-" + ptrFormat(int64(i+1)),
			Count: int64(20 - i),
		}
	}
	m.projectLabels.byProject[7] = labelCacheEntry{
		pid: 7, gen: 1, labels: suggestions,
	}
	m.input.suggestHighlight = 9
	got := m.View()
	assertGolden(t, "label-prompt-scroll", got)
}

// snapLabelPromptModel builds the Model used by the label-prompt
// snapshot tests: detail view with the snap fixture issue + a `+`
// prompt open against project 7 / issue 42. width/height are pinned
// at 120x30.
func snapLabelPromptModel() Model {
	dm := snapDetailFixture()
	m := Model{
		view:          viewDetail,
		scope:         scope{projectID: 7, projectName: "kata"},
		width:         120,
		height:        30,
		sseStatus:     sseConnected,
		projectLabels: newLabelCache(),
		detail:        dm,
	}
	m.input = newPanelPrompt(inputLabelPrompt, formTarget{
		projectID: 7, issueNumber: dm.issue.Number, detailGen: dm.gen,
	})
	return m
}
