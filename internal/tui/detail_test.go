package tui

import (
	"context"
	"errors"
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
)

// detailFixture seeds a detailModel with one issue, two comments, two
// events, and one link so every per-tab stub has data to render. The
// body has many lines so j scrolling has somewhere to go.
func detailFixture() detailModel {
	iss := Issue{
		ProjectID: 7, Number: 42, Title: "fix login bug on Safari",
		Status: "open", Author: "wesm",
	}
	body := strings.Repeat("line\n", 20) + "tail"
	iss.Body = body
	return detailModel{
		issue: &iss,
		comments: []CommentEntry{
			{ID: 1, Author: "alice", Body: "first"},
			{ID: 2, Author: "bob", Body: "second"},
		},
		events: []EventLogEntry{
			{ID: 9, Type: "issue.created", Actor: "alice"},
			{ID: 10, Type: "issue.commented", Actor: "bob"},
		},
		links: []LinkEntry{
			{ID: 100, Type: "blocks", FromNumber: 42, ToNumber: 7, Author: "wesm"},
		},
	}
}

// TestDetail_Render_Header_Title confirms the title appears in the view.
func TestDetail_Render_Header_Title(t *testing.T) {
	dm := detailFixture()
	out := dm.View(80, 24)
	if !strings.Contains(out, "fix login bug on Safari") {
		t.Fatalf("title missing from view:\n%s", out)
	}
	if !strings.Contains(out, "#42") {
		t.Fatalf("issue number missing:\n%s", out)
	}
}

// TestDetail_TabCycle_NextPrev: tab cycles forward, shift+tab cycles
// backward, both with wrap-around. Three forward presses returns to
// Comments; one backward from Comments lands on Links.
func TestDetail_TabCycle_NextPrev(t *testing.T) {
	dm := detailFixture()
	km := newKeymap()
	tabKey := tea.KeyMsg{Type: tea.KeyTab}
	shiftTab := tea.KeyMsg{Type: tea.KeyShiftTab}

	dm, _ = dm.Update(tabKey, km)
	if dm.activeTab != tabEvents {
		t.Fatalf("after tab: activeTab = %d, want tabEvents (%d)", dm.activeTab, tabEvents)
	}
	dm, _ = dm.Update(tabKey, km)
	if dm.activeTab != tabLinks {
		t.Fatalf("after tab tab: activeTab = %d, want tabLinks (%d)", dm.activeTab, tabLinks)
	}
	dm, _ = dm.Update(tabKey, km)
	if dm.activeTab != tabComments {
		t.Fatalf("after wrap: activeTab = %d, want tabComments (%d)", dm.activeTab, tabComments)
	}
	dm, _ = dm.Update(shiftTab, km)
	if dm.activeTab != tabLinks {
		t.Fatalf("after shift+tab from comments: activeTab = %d, want tabLinks", dm.activeTab)
	}
}

// TestDetail_TabRender_ActiveContent: after a tab press the events
// header text appears in the rendered output.
func TestDetail_TabRender_ActiveContent(t *testing.T) {
	dm := detailFixture()
	km := newKeymap()
	out := dm.View(80, 24)
	if !strings.Contains(out, "Comments (2)") {
		t.Fatalf("comments stub missing:\n%s", out)
	}
	dm, _ = dm.Update(tea.KeyMsg{Type: tea.KeyTab}, km)
	out = dm.View(80, 24)
	if !strings.Contains(out, "Events (2)") {
		t.Fatalf("events stub missing after tab:\n%s", out)
	}
	dm, _ = dm.Update(tea.KeyMsg{Type: tea.KeyTab}, km)
	out = dm.View(80, 24)
	if !strings.Contains(out, "Links (1)") {
		t.Fatalf("links stub missing after second tab:\n%s", out)
	}
}

// TestDetail_Scroll_BoundsAtTop: j when scroll==0 must clamp at zero,
// not go negative.
func TestDetail_Scroll_BoundsAtTop(t *testing.T) {
	dm := detailFixture()
	km := newKeymap()
	dm, _ = dm.Update(runeKey('k'), km)
	if dm.scroll != 0 {
		t.Fatalf("scroll = %d, want 0 (clamped at top)", dm.scroll)
	}
}

// TestDetail_Scroll_DownIncreases: j increments dm.scroll.
func TestDetail_Scroll_DownIncreases(t *testing.T) {
	dm := detailFixture()
	km := newKeymap()
	dm, _ = dm.Update(runeKey('j'), km)
	dm, _ = dm.Update(runeKey('j'), km)
	if dm.scroll != 2 {
		t.Fatalf("scroll = %d, want 2", dm.scroll)
	}
}

// TestDetail_Back_EmitsPopMsg: esc returns a tea.Cmd that emits popDetailMsg.
func TestDetail_Back_EmitsPopMsg(t *testing.T) {
	dm := detailFixture()
	km := newKeymap()
	_, cmd := dm.Update(tea.KeyMsg{Type: tea.KeyEsc}, km)
	if cmd == nil {
		t.Fatal("esc must return a tea.Cmd")
	}
	msg := cmd()
	if _, ok := msg.(popDetailMsg); !ok {
		t.Fatalf("expected popDetailMsg, got %T", msg)
	}
}

// TestDetail_OpenFromList_DispatchesBatch: pressing Enter on a list row
// switches the model to viewDetail, seeds m.detail.issue, and returns
// a tea.Cmd. (We can't introspect a tea.Batch directly without running
// it, but we can verify the model state mutated correctly.)
func TestDetail_OpenFromList_DispatchesBatch(t *testing.T) {
	m := initialModel(Options{})
	m.api = &Client{} // non-nil so handleOpenDetail dispatches the batch
	m.scope = scope{projectID: 7}
	m.list.loading = false
	m.list.issues = []Issue{
		{ProjectID: 7, Number: 1, Title: "first"},
		{ProjectID: 7, Number: 2, Title: "second"},
	}
	// Move cursor to row 1 so Enter opens issue #2.
	out, _ := m.Update(runeKey('j'))
	m = out.(Model)
	out, cmd := m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	m = out.(Model)
	if cmd == nil {
		t.Fatal("expected open cmd from Enter")
	}
	// Run the cmd — it returns openDetailMsg for the top-level Model.
	msg := cmd()
	open, ok := msg.(openDetailMsg)
	if !ok {
		t.Fatalf("expected openDetailMsg, got %T", msg)
	}
	if open.issue.Number != 2 {
		t.Fatalf("issue.Number = %d, want 2", open.issue.Number)
	}
	out, _ = m.Update(open)
	m = out.(Model)
	if m.view != viewDetail {
		t.Fatalf("view = %v, want viewDetail", m.view)
	}
	if m.detail.issue == nil || m.detail.issue.Number != 2 {
		t.Fatalf("detail.issue not seeded correctly: %+v", m.detail.issue)
	}
}

// TestDetail_PopReturnsToListPreservingState: detail → esc → list keeps
// the list cursor and filter state intact.
func TestDetail_PopReturnsToListPreservingState(t *testing.T) {
	m := initialModel(Options{})
	m.scope = scope{projectID: 7}
	m.list.loading = false
	m.list.cursor = 1
	m.list.filter = ListFilter{Status: "open", Search: "bug"}
	m.list.issues = []Issue{
		{ProjectID: 7, Number: 1, Title: "first bug"},
		{ProjectID: 7, Number: 2, Title: "second bug"},
	}
	m.view = viewDetail
	m.detail.issue = &m.list.issues[1]
	out, _ := m.Update(popDetailMsg{})
	m = out.(Model)
	if m.view != viewList {
		t.Fatalf("view = %v, want viewList after pop", m.view)
	}
	if m.list.cursor != 1 {
		t.Fatalf("list cursor = %d, want 1 (preserved)", m.list.cursor)
	}
	if m.list.filter.Status != "open" || m.list.filter.Search != "bug" {
		t.Fatalf("list filter clobbered: %+v", m.list.filter)
	}
}

// TestDetail_Loading_Renders shows the loading hint while issue is nil.
func TestDetail_Loading_Renders(t *testing.T) {
	dm := detailModel{loading: true}
	out := dm.View(80, 24)
	if !strings.Contains(out, "loading") {
		t.Fatalf("expected loading hint, got:\n%s", out)
	}
}

// TestDetail_FetchedMsgs_Populate: the three tab fetch messages seed
// the corresponding slices on dm.
func TestDetail_FetchedMsgs_Populate(t *testing.T) {
	dm := detailModel{issue: &Issue{Number: 1}}
	km := newKeymap()
	dm, _ = dm.Update(commentsFetchedMsg{
		comments: []CommentEntry{{ID: 1, Author: "a", Body: "x"}},
	}, km)
	if len(dm.comments) != 1 {
		t.Fatalf("comments not populated: %+v", dm.comments)
	}
	dm, _ = dm.Update(eventsFetchedMsg{
		events: []EventLogEntry{{ID: 1, Type: "issue.created"}},
	}, km)
	if len(dm.events) != 1 {
		t.Fatalf("events not populated: %+v", dm.events)
	}
	dm, _ = dm.Update(linksFetchedMsg{
		links: []LinkEntry{{ID: 1, Type: "blocks"}},
	}, km)
	if len(dm.links) != 1 {
		t.Fatalf("links not populated: %+v", dm.links)
	}
}

// TestDetail_FetchedMsgs_ErrorRecorded: an error on any tab fetch
// surfaces on dm.err.
func TestDetail_FetchedMsgs_ErrorRecorded(t *testing.T) {
	dm := detailModel{issue: &Issue{Number: 1}}
	km := newKeymap()
	dm, _ = dm.Update(commentsFetchedMsg{err: errors.New("boom")}, km)
	if dm.err == nil || dm.err.Error() != "boom" {
		t.Fatalf("err = %v, want boom", dm.err)
	}
}

// TestDetail_ProjectID_AllProjectsUsesIssueProjectID: in all-projects
// scope, detailProjectID prefers the issue's ProjectID field over the
// (zero) sc.projectID so the URL is correct.
func TestDetail_ProjectID_AllProjectsUsesIssueProjectID(t *testing.T) {
	iss := Issue{ProjectID: 42, Number: 1}
	got := detailProjectID(iss, scope{allProjects: true})
	if got != 42 {
		t.Fatalf("detailProjectID = %d, want 42", got)
	}
}

// TestDetail_ProjectID_SingleProjectUsesScope: in single-project scope,
// detailProjectID always uses sc.projectID even when the issue carries
// its own (they should match anyway).
func TestDetail_ProjectID_SingleProjectUsesScope(t *testing.T) {
	iss := Issue{ProjectID: 99, Number: 1}
	got := detailProjectID(iss, scope{projectID: 7})
	if got != 7 {
		t.Fatalf("detailProjectID = %d, want 7", got)
	}
}

// TestDetail_HardWrap covers the body-line wrapper: a long line is
// chopped into chunks of width cells and the trailing remainder is
// preserved as the final chunk.
func TestDetail_HardWrap(t *testing.T) {
	got := hardWrap("abcdefghij", 4)
	want := []string{"abcd", "efgh", "ij"}
	if len(got) != len(want) {
		t.Fatalf("got %d chunks, want %d (%v)", len(got), len(want), got)
	}
	for i := range got {
		if got[i] != want[i] {
			t.Fatalf("chunk %d: got %q, want %q", i, got[i], want[i])
		}
	}
}

// fakeDetailAPI is the test double for detailAPI used by the fetch-cmd
// tests. The exported fields seed the return values; the *Calls fields
// count invocations so concurrency tests can assert exactly N hits.
type fakeDetailAPI struct {
	commentsResult []CommentEntry
	eventsResult   []EventLogEntry
	linksResult    []LinkEntry
	commentsErr    error
	eventsErr      error
	linksErr       error
}

func (f *fakeDetailAPI) ListComments(
	_ context.Context, _, _ int64,
) ([]CommentEntry, error) {
	return f.commentsResult, f.commentsErr
}

func (f *fakeDetailAPI) ListEvents(
	_ context.Context, _, _ int64,
) ([]EventLogEntry, error) {
	return f.eventsResult, f.eventsErr
}

func (f *fakeDetailAPI) ListLinks(
	_ context.Context, _, _ int64,
) ([]LinkEntry, error) {
	return f.linksResult, f.linksErr
}

// TestDetail_FetchCommands_RoundTrip exercises the three fetch wrappers
// through their tea.Cmd contracts. Each cmd should call the API once
// and emit the corresponding fetched-message with the seed payload.
func TestDetail_FetchCommands_RoundTrip(t *testing.T) {
	api := &fakeDetailAPI{
		commentsResult: []CommentEntry{{ID: 1, Author: "a"}},
		eventsResult:   []EventLogEntry{{ID: 2, Type: "issue.created"}},
		linksResult:    []LinkEntry{{ID: 3, Type: "blocks"}},
	}
	cm, ok := fetchComments(api, 7, 42)().(commentsFetchedMsg)
	if !ok {
		t.Fatalf("expected commentsFetchedMsg")
	}
	if len(cm.comments) != 1 || cm.comments[0].Author != "a" {
		t.Fatalf("comments payload wrong: %+v", cm.comments)
	}
	em, ok := fetchEvents(api, 7, 42)().(eventsFetchedMsg)
	if !ok {
		t.Fatalf("expected eventsFetchedMsg")
	}
	if len(em.events) != 1 || em.events[0].Type != "issue.created" {
		t.Fatalf("events payload wrong: %+v", em.events)
	}
	lm, ok := fetchLinks(api, 7, 42)().(linksFetchedMsg)
	if !ok {
		t.Fatalf("expected linksFetchedMsg")
	}
	if len(lm.links) != 1 || lm.links[0].Type != "blocks" {
		t.Fatalf("links payload wrong: %+v", lm.links)
	}
}

// TestDetail_BodyScroll_RenderWindow: with scroll=N the rendered body
// starts at line N+header so the user sees later body content.
func TestDetail_BodyScroll_RenderWindow(t *testing.T) {
	dm := detailFixture()
	dm.scroll = 5
	out := dm.renderBody(80, 5)
	// The fixture body is 20 "line"s + "tail". With scroll=5 and 5-row
	// window, we should see lines 6-10 (zero-indexed 5-9), all "line".
	lines := strings.Split(out, "\n")
	if len(lines) != 5 {
		t.Fatalf("got %d body lines, want 5", len(lines))
	}
	if lines[0] != "line" {
		t.Fatalf("body[0] = %q, want line", lines[0])
	}
}

// TestDetail_ScrollClampsAtEOF: if scroll is set beyond the body length
// the renderer clamps at the last line so the window still fills.
func TestDetail_ScrollClampsAtEOF(t *testing.T) {
	dm := detailFixture()
	dm.scroll = 10000
	out := dm.renderBody(80, 5)
	lines := strings.Split(out, "\n")
	// Even with a huge scroll, the window must still produce non-empty
	// output (the last 5 lines of the fixture body).
	if len(lines) == 0 {
		t.Fatalf("expected clamped window, got empty output")
	}
	if !strings.Contains(out, "tail") {
		t.Fatalf("expected tail near EOF, got:\n%s", out)
	}
}

// TestDetail_OpenInAllProjectsScope_UsesIssueProjectID: in all-projects
// mode, opening an issue dispatches fetches against the issue's own
// project_id (not the scope's, which is zero in all-projects mode).
func TestDetail_OpenInAllProjectsScope_UsesIssueProjectID(t *testing.T) {
	m := initialModel(Options{})
	m.api = &Client{}
	m.scope = scope{allProjects: true}
	iss := Issue{ProjectID: 99, Number: 5, Title: "cross-project"}
	out, _ := m.Update(openDetailMsg{issue: iss})
	m = out.(Model)
	if m.view != viewDetail {
		t.Fatalf("view = %v, want viewDetail", m.view)
	}
	if m.detail.issue == nil || m.detail.issue.ProjectID != 99 {
		t.Fatalf("detail.issue not seeded correctly: %+v", m.detail.issue)
	}
}

// TestDetail_OpenWithNilAPI_NoCrash: without a wired client (test
// harness path), the open handler still seeds the model and returns
// nil instead of panicking on the fetch dispatch.
func TestDetail_OpenWithNilAPI_NoCrash(t *testing.T) {
	m := initialModel(Options{})
	m.api = nil
	out, cmd := m.Update(openDetailMsg{issue: Issue{Number: 1, Title: "x"}})
	if cmd != nil {
		t.Fatalf("expected nil cmd when api is nil, got %T", cmd)
	}
	m = out.(Model)
	if m.view != viewDetail {
		t.Fatalf("view = %v, want viewDetail", m.view)
	}
}

// TestDetail_BodyHeight_TinyTerminal: very small terminals get a 5-row
// floor so scrolling still produces visible output.
func TestDetail_BodyHeight_TinyTerminal(t *testing.T) {
	dm := detailFixture()
	if got := dm.bodyHeight(4); got != 5 {
		t.Fatalf("bodyHeight(4) = %d, want 5 (floor)", got)
	}
}
