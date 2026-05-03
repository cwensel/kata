package tui

import (
	"strings"
	"testing"
	"time"
)

func TestDetailDocumentPage80x50LayoutSignals(t *testing.T) {
	defer snapshotInit(t)()
	dm := snapDetailHierarchyFixture()
	dm.issue.Owner = ptrString("alice")
	dm.issue.Labels = []string{"prio-1", "bug", "needs-design"}
	dm.issue.CreatedAt = time.Date(2026, 4, 30, 10, 0, 0, 0, time.UTC)
	dm.issue.UpdatedAt = snapshotFixedNow.Add(-3 * time.Hour)

	got := stripANSI(dm.View(80, 50, viewChrome{
		scope:   scope{projectID: 7, projectName: "kata"},
		version: "dev",
	}))

	assertLineCount(t, got, 50)
	assertLinesFitWidth(t, got, 80)
	for _, want := range []string{
		"issue #42",
		"fix login bug on Safari",
		"[open]",
		"authored by wesm",
		"created Apr 30 10:00",
		"updated 3h ago",
		"owner: alice",
		"labels: [bug] [needs-design] [prio-1]",
		"parent: #12 workspace polish parent",
		"children: 1 open / 2 total",
		"Body",
		"Children",
		"Activity",
		"[ Comments (2) ]",
	} {
		assertStringContains(t, got, want)
	}
	for _, deny := range []string{"Owner:", "Parent:"} {
		if strings.Contains(got, deny) {
			t.Fatalf("detail document should use lowercase metadata labels, found %q:\n%s", deny, got)
		}
	}
}

func TestDetailDocument_DoesNotPadBodyBeforeChildren(t *testing.T) {
	defer snapshotInit(t)()
	dm := snapDetailHierarchyFixture()

	got := stripANSI(dm.View(80, 50, viewChrome{}))
	lines := strings.Split(got, "\n")
	bodyEnd := indexOf(lines, "Click the login button twice.")
	children := indexOf(lines, "Children")
	if bodyEnd < 0 || children < 0 {
		t.Fatalf("missing body or children section:\n%s", got)
	}
	if gap := children - bodyEnd; gap > 4 {
		t.Fatalf("body section absorbed vertical slack; gap=%d:\n%s", gap, got)
	}
}

func TestDetailDocument_NarrowStacksMetadata(t *testing.T) {
	defer snapshotInit(t)()
	dm := snapDetailFixture()
	dm.issue.Owner = ptrString("alice")
	dm.issue.Labels = []string{"bug", "prio-1"}
	dm.parent = &IssueRef{Number: 12, Title: "workspace polish", Status: "open"}

	got := stripANSI(dm.View(72, 40, viewChrome{}))
	assertLinesFitWidth(t, got, 72)
	assertStringContains(t, got, "\nowner: alice\n")
	assertStringContains(t, got, "\nlabels: [bug] [prio-1]\n")
	assertStringContains(t, got, "\nparent: #12 workspace polish\n")
	assertStringContains(t, got, "\nchildren: none\n")
}

func TestDetailDocument_EmptyBodyAndActivityOmitted(t *testing.T) {
	defer snapshotInit(t)()
	iss := Issue{ProjectID: 7, Number: 99, Title: "empty issue", Status: "open"}
	dm := detailModel{issue: &iss}

	got := stripANSI(dm.View(80, 32, viewChrome{}))
	assertStringContains(t, got, "(no description)")
	if strings.Contains(got, "Activity") {
		t.Fatalf("detail document should omit all-empty activity section:\n%s", got)
	}
}

func TestDetailDocument_LongTitleKeepsStatusVisible(t *testing.T) {
	defer snapshotInit(t)()
	title := "this is a very long issue title that should truncate before it can collide with the status pill"
	iss := Issue{ProjectID: 7, Number: 77, Title: title, Status: "closed"}
	dm := detailModel{issue: &iss}

	got := stripANSI(dm.View(80, 32, viewChrome{}))
	assertLinesFitWidth(t, got, 80)
	assertStringContains(t, got, "[closed]")
	if !strings.Contains(got, "…") {
		t.Fatalf("expected long title truncation:\n%s", got)
	}
}

func TestDetailDocument_MarkdownRenderingDropsSourceFences(t *testing.T) {
	defer snapshotInit(t)()
	iss := Issue{
		ProjectID: 7,
		Number:    55,
		Title:     "markdown body",
		Status:    "open",
		Body: strings.Join([]string{
			"## Steps",
			"",
			"- Click `Login` twice.",
			"",
			"```go",
			`fmt.Println("ok")`,
			"```",
		}, "\n"),
	}
	dm := detailModel{issue: &iss}

	got := stripANSI(dm.View(80, 40, viewChrome{}))
	for _, want := range []string{"Steps", "`Login`", `fmt.Println("ok")`} {
		assertStringContains(t, got, want)
	}
	for _, deny := range []string{"## Steps", "```"} {
		if strings.Contains(got, deny) {
			t.Fatalf("markdown source marker %q leaked into detail render:\n%s", deny, got)
		}
	}
}

func TestDetailDocument_CommentAuthorsAlignTimestamps(t *testing.T) {
	defer snapshotInit(t)()
	when := time.Date(2026, 4, 30, 10, 0, 0, 0, time.UTC)
	comments := []CommentEntry{
		{Author: "alice", Body: "first", CreatedAt: when},
		{Author: "bob", Body: "second", CreatedAt: when.Add(time.Hour)},
	}

	got := stripANSI(renderCommentsTab(comments, 80, 10, 0, tabState{}))
	assertStringContains(t, got, "alice  Apr 30 10:00")
	assertStringContains(t, got, "bob    Apr 30 11:00")
}
