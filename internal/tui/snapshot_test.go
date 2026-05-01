package tui

import (
	"flag"
	"io"
	"os"
	"path/filepath"
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
	got := lm.View(120, 30)
	assertGolden(t, "list-default-mixed-status", got)
}

// TestSnapshot_List_NoColorMode is the same scenario rendered in the
// explicit no-color path. Because snapshotInit always pins
// KATA_COLOR_MODE=none, the rendered text is identical to the default
// snapshot here — but committing the golden separately documents the
// "no-color" surface and would catch a regression that re-introduces
// ANSI escapes only in this path.
func TestSnapshot_List_NoColorMode(t *testing.T) {
	defer snapshotInit(t)()
	t.Setenv("NO_COLOR", "1")
	applyDefaultColorMode(io.Discard)
	lm := newListModel()
	lm.loading = false
	lm.issues = snapListFixture()
	lm.cursor = 1
	got := lm.View(120, 30)
	assertGolden(t, "list-no-color-mode", got)
}

// TestSnapshot_List_EmptyAfterFilter exercises the "no rows visible"
// branch of renderBody: a search filter narrows everything out so the
// empty-state hint appears.
func TestSnapshot_List_EmptyAfterFilter(t *testing.T) {
	defer snapshotInit(t)()
	lm := newListModel()
	lm.loading = false
	lm.issues = snapListFixture()
	lm.filter = ListFilter{Search: "no-match-anywhere"}
	got := lm.View(120, 30)
	assertGolden(t, "list-empty-after-filter", got)
}

// TestSnapshot_List_WithFilterChips covers the chip strip render path
// with two active filters: status:open and owner:alice. The label chip
// is intentionally skipped (Issue projection lacks labels) so the
// scenario uses owner instead of label per Roborev-fix #2's guidance.
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
	got := lm.View(120, 30)
	assertGolden(t, "list-with-filter-chips", got)
}

// TestSnapshot_Detail_CommentsTab locks the comments tab render. Tab
// strip shows Comments highlighted; the entry list contains the two
// fixture comments with author, timestamp, and indented body lines.
func TestSnapshot_Detail_CommentsTab(t *testing.T) {
	defer snapshotInit(t)()
	dm := snapDetailFixture()
	dm.activeTab = tabComments
	got := dm.View(120, 30)
	assertGolden(t, "detail-comments-tab", got)
}

// TestSnapshot_Detail_EventsTab same fixture, events tab active.
func TestSnapshot_Detail_EventsTab(t *testing.T) {
	defer snapshotInit(t)()
	dm := snapDetailFixture()
	dm.activeTab = tabEvents
	got := dm.View(120, 30)
	assertGolden(t, "detail-events-tab", got)
}

// TestSnapshot_Detail_LinksTab same fixture, links tab active.
func TestSnapshot_Detail_LinksTab(t *testing.T) {
	defer snapshotInit(t)()
	dm := snapDetailFixture()
	dm.activeTab = tabLinks
	got := dm.View(120, 30)
	assertGolden(t, "detail-links-tab", got)
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
