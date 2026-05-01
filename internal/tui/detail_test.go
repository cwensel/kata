package tui

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"
)

// detailFixture seeds a detailModel with one issue, two comments, two
// events, and one link so every per-tab renderer has data to render.
// The body has many lines so j scrolling has somewhere to go.
func detailFixture() detailModel {
	iss := Issue{
		ProjectID: 7, Number: 42, Title: "fix login bug on Safari",
		Status: "open", Author: "wesm",
	}
	body := strings.Repeat("line\n", 20) + "tail"
	iss.Body = body
	return detailModel{
		issue:    &iss,
		scopePID: 7,
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

	dm, _ = dm.Update(tabKey, km, nil)
	if dm.activeTab != tabEvents {
		t.Fatalf("after tab: activeTab = %d, want tabEvents (%d)", dm.activeTab, tabEvents)
	}
	dm, _ = dm.Update(tabKey, km, nil)
	if dm.activeTab != tabLinks {
		t.Fatalf("after tab tab: activeTab = %d, want tabLinks (%d)", dm.activeTab, tabLinks)
	}
	dm, _ = dm.Update(tabKey, km, nil)
	if dm.activeTab != tabComments {
		t.Fatalf("after wrap: activeTab = %d, want tabComments (%d)", dm.activeTab, tabComments)
	}
	dm, _ = dm.Update(shiftTab, km, nil)
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
		t.Fatalf("comments header missing:\n%s", out)
	}
	dm, _ = dm.Update(tea.KeyMsg{Type: tea.KeyTab}, km, nil)
	out = dm.View(80, 24)
	if !strings.Contains(out, "Events (2)") {
		t.Fatalf("events header missing after tab:\n%s", out)
	}
	dm, _ = dm.Update(tea.KeyMsg{Type: tea.KeyTab}, km, nil)
	out = dm.View(80, 24)
	if !strings.Contains(out, "Links (1)") {
		t.Fatalf("links header missing after second tab:\n%s", out)
	}
}

// TestDetail_Scroll_BoundsAtTop: with no comments, k at scroll==0 must
// clamp at zero. The fixture HAS comments so we use a body-only model.
func TestDetail_Scroll_BoundsAtTop(t *testing.T) {
	dm := detailModel{issue: &Issue{Number: 1, Body: "x\ny"}}
	km := newKeymap()
	dm, _ = dm.Update(runeKey('k'), km, nil)
	if dm.scroll != 0 {
		t.Fatalf("scroll = %d, want 0 (clamped at top)", dm.scroll)
	}
}

// TestDetail_Scroll_DownIncreases: j increments dm.scroll WHEN the
// active tab has no rows. The fixture comments would steal j, so
// build a tab-empty model for the body-scroll path.
func TestDetail_Scroll_DownIncreases(t *testing.T) {
	dm := detailModel{issue: &Issue{Number: 1, Body: "x\ny"}}
	km := newKeymap()
	dm, _ = dm.Update(runeKey('j'), km, nil)
	dm, _ = dm.Update(runeKey('j'), km, nil)
	if dm.scroll != 2 {
		t.Fatalf("scroll = %d, want 2", dm.scroll)
	}
}

// TestDetail_Back_EmitsPopMsg: esc returns a tea.Cmd that emits
// popDetailMsg when the nav stack is empty.
func TestDetail_Back_EmitsPopMsg(t *testing.T) {
	dm := detailFixture()
	km := newKeymap()
	_, cmd := dm.Update(tea.KeyMsg{Type: tea.KeyEsc}, km, nil)
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
	out, _ := m.Update(runeKey('j'))
	m = out.(Model)
	out, cmd := m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	m = out.(Model)
	if cmd == nil {
		t.Fatal("expected open cmd from Enter")
	}
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
	if m.detail.scopePID != 7 {
		t.Fatalf("detail.scopePID = %d, want 7", m.detail.scopePID)
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
	}, km, nil)
	if len(dm.comments) != 1 {
		t.Fatalf("comments not populated: %+v", dm.comments)
	}
	dm, _ = dm.Update(eventsFetchedMsg{
		events: []EventLogEntry{{ID: 1, Type: "issue.created"}},
	}, km, nil)
	if len(dm.events) != 1 {
		t.Fatalf("events not populated: %+v", dm.events)
	}
	dm, _ = dm.Update(linksFetchedMsg{
		links: []LinkEntry{{ID: 1, Type: "blocks"}},
	}, km, nil)
	if len(dm.links) != 1 {
		t.Fatalf("links not populated: %+v", dm.links)
	}
}

// TestDetail_FetchedMsgs_ErrorRecorded: an error on any tab fetch
// surfaces on dm.err.
func TestDetail_FetchedMsgs_ErrorRecorded(t *testing.T) {
	dm := detailModel{issue: &Issue{Number: 1}}
	km := newKeymap()
	dm, _ = dm.Update(commentsFetchedMsg{err: errors.New("boom")}, km, nil)
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

// TestDetail_HardWrap covers the body-line wrapper.
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

// TestDetail_HardWrap_OversizeRune confirms the wrapper makes progress
// when the leading rune is wider than the requested width.
func TestDetail_HardWrap_OversizeRune(t *testing.T) {
	got := hardWrap("你好世界", 1)
	if len(got) != 4 {
		t.Fatalf("expected 4 chunks, got %d: %v", len(got), got)
	}
}

// fakeDetailAPI is the test double for detailAPI used by the fetch-cmd
// tests and the jump-nav tests. The exported result fields seed the
// return values; lastGetIssue captures the most recent GetIssue call so
// jump tests can assert on the issue number that was fetched. The
// mutation counters/last-* fields support the Task 9 mutation tests.
type fakeDetailAPI struct {
	commentsResult []CommentEntry
	eventsResult   []EventLogEntry
	linksResult    []LinkEntry
	commentsErr    error
	eventsErr      error
	linksErr       error

	getIssueResult *Issue
	getIssueErr    error
	lastGetIssue   int64

	closeCalls       int
	reopenCalls      int
	addLabelCalls    int
	removeLabelCalls int
	assignCalls      int
	addLinkCalls     int
	editBodyCalls    int
	addCommentCalls  int

	lastProjectID int64
	lastNumber    int64
	lastActor     string
	lastLabel     string
	lastOwner     string
	lastBody      string
	lastLinkBody  LinkBody

	mutationResult *MutationResp
	mutationErr    error
}

func (f *fakeDetailAPI) GetIssue(
	_ context.Context, _, number int64,
) (*Issue, error) {
	f.lastGetIssue = number
	return f.getIssueResult, f.getIssueErr
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

func (f *fakeDetailAPI) Close(
	_ context.Context, projectID, number int64, actor string,
) (*MutationResp, error) {
	f.closeCalls++
	f.lastProjectID = projectID
	f.lastNumber = number
	f.lastActor = actor
	return f.mutationResult, f.mutationErr
}

func (f *fakeDetailAPI) Reopen(
	_ context.Context, projectID, number int64, actor string,
) (*MutationResp, error) {
	f.reopenCalls++
	f.lastProjectID = projectID
	f.lastNumber = number
	f.lastActor = actor
	return f.mutationResult, f.mutationErr
}

func (f *fakeDetailAPI) AddLabel(
	_ context.Context, projectID, number int64, label, actor string,
) (*MutationResp, error) {
	f.addLabelCalls++
	f.lastProjectID = projectID
	f.lastNumber = number
	f.lastLabel = label
	f.lastActor = actor
	return f.mutationResult, f.mutationErr
}

func (f *fakeDetailAPI) RemoveLabel(
	_ context.Context, projectID, number int64, label, actor string,
) (*MutationResp, error) {
	f.removeLabelCalls++
	f.lastProjectID = projectID
	f.lastNumber = number
	f.lastLabel = label
	f.lastActor = actor
	return f.mutationResult, f.mutationErr
}

func (f *fakeDetailAPI) Assign(
	_ context.Context, projectID, number int64, owner, actor string,
) (*MutationResp, error) {
	f.assignCalls++
	f.lastProjectID = projectID
	f.lastNumber = number
	f.lastOwner = owner
	f.lastActor = actor
	return f.mutationResult, f.mutationErr
}

func (f *fakeDetailAPI) AddLink(
	_ context.Context, projectID, number int64, body LinkBody, actor string,
) (*MutationResp, error) {
	f.addLinkCalls++
	f.lastProjectID = projectID
	f.lastNumber = number
	f.lastLinkBody = body
	f.lastActor = actor
	return f.mutationResult, f.mutationErr
}

func (f *fakeDetailAPI) EditBody(
	_ context.Context, projectID, number int64, body, actor string,
) (*MutationResp, error) {
	f.editBodyCalls++
	f.lastProjectID = projectID
	f.lastNumber = number
	f.lastBody = body
	f.lastActor = actor
	return f.mutationResult, f.mutationErr
}

func (f *fakeDetailAPI) AddComment(
	_ context.Context, projectID, number int64, body, actor string,
) (*MutationResp, error) {
	f.addCommentCalls++
	f.lastProjectID = projectID
	f.lastNumber = number
	f.lastBody = body
	f.lastActor = actor
	return f.mutationResult, f.mutationErr
}

// TestDetail_FetchCommands_RoundTrip exercises the three fetch wrappers
// through their tea.Cmd contracts.
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
	if m.detail.scopePID != 99 {
		t.Fatalf("detail.scopePID = %d, want 99", m.detail.scopePID)
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

// TestDetail_RenderCommentsTab_FormatsAuthorAndIndentsBody confirms the
// per-comment header is "[author] timestamp" and body lines are indented
// by 2 spaces under the header.
func TestDetail_RenderCommentsTab_FormatsAuthorAndIndentsBody(t *testing.T) {
	cs := []CommentEntry{
		{
			ID: 1, Author: "alice",
			Body:      "hello there\nthis is line two",
			CreatedAt: time.Date(2025, 1, 2, 15, 4, 0, 0, time.UTC),
		},
	}
	out := renderCommentsTab(cs, 80, 20, -1)
	if !strings.Contains(out, "[alice] 2025-01-02 15:04") {
		t.Fatalf("missing author/timestamp header:\n%s", out)
	}
	if !strings.Contains(out, "  hello there") {
		t.Fatalf("body line not indented:\n%s", out)
	}
	if !strings.Contains(out, "  this is line two") {
		t.Fatalf("multi-line body not indented:\n%s", out)
	}
}

// TestDetail_RenderCommentsTab_EmptyShowsHint shows the placeholder when
// there are no comments.
func TestDetail_RenderCommentsTab_EmptyShowsHint(t *testing.T) {
	out := renderCommentsTab(nil, 80, 5, -1)
	if !strings.Contains(out, "no comments yet") {
		t.Fatalf("expected placeholder, got:\n%s", out)
	}
	if !strings.Contains(out, "Comments (0)") {
		t.Fatalf("expected zero-count header, got:\n%s", out)
	}
}

// TestDetail_RenderEventsTab_FormatsCommonEventTypes covers a slice
// over the type vocabulary so the description column is in lockstep.
func TestDetail_RenderEventsTab_FormatsCommonEventTypes(t *testing.T) {
	when := time.Date(2025, 1, 2, 15, 4, 0, 0, time.UTC)
	to := int64(11)
	es := []EventLogEntry{
		{Type: "issue.created", Actor: "a", CreatedAt: when},
		{Type: "issue.closed", Actor: "a", CreatedAt: when,
			Payload: map[string]any{"reason": "wontfix"}},
		{Type: "issue.labeled", Actor: "b", CreatedAt: when,
			Payload: map[string]any{"label": "bug"}},
		{Type: "issue.linked", Actor: "c", CreatedAt: when,
			IssueNumber: &to,
			Payload: map[string]any{
				"type": "blocks", "to_number": float64(11),
			}},
		{Type: "issue.assigned", Actor: "d", CreatedAt: when,
			Payload: map[string]any{"owner": "wesm"}},
	}
	out := renderEventsTab(es, 200, 20, -1)
	checks := []string{
		"[issue.created] 2025-01-02 15:04 a — created",
		"[issue.closed] 2025-01-02 15:04 a — closed (wontfix)",
		"[issue.labeled] 2025-01-02 15:04 b — labeled bug",
		"[issue.linked] 2025-01-02 15:04 c — linked blocks #11",
		"[issue.assigned] 2025-01-02 15:04 d — assigned wesm",
	}
	for _, want := range checks {
		if !strings.Contains(out, want) {
			t.Fatalf("missing %q:\n%s", want, out)
		}
	}
}

// TestDetail_RenderEventsTab_UnknownTypeFallback: an unrecognized event
// type renders with the "issue." prefix stripped so the row still says
// something coherent.
func TestDetail_RenderEventsTab_UnknownTypeFallback(t *testing.T) {
	es := []EventLogEntry{{Type: "issue.future_thing", Actor: "a"}}
	out := renderEventsTab(es, 80, 5, -1)
	if !strings.Contains(out, "future_thing") {
		t.Fatalf("expected fallback description:\n%s", out)
	}
}

// TestDetail_RenderLinksTab_FormatsLinkLine confirms the link line shape.
func TestDetail_RenderLinksTab_FormatsLinkLine(t *testing.T) {
	when := time.Date(2025, 1, 2, 15, 4, 0, 0, time.UTC)
	ls := []LinkEntry{
		{ID: 1, Type: "blocks", FromNumber: 42, ToNumber: 7,
			Author: "wesm", CreatedAt: when},
	}
	out := renderLinksTab(ls, 200, 5, -1)
	want := "[blocks] → #7 ← #42  by wesm @ 2025-01-02 15:04"
	if !strings.Contains(out, want) {
		t.Fatalf("missing link line %q in:\n%s", want, out)
	}
}

// TestDetail_RenderLinksTab_EmptyShowsHint shows the placeholder when
// there are no links.
func TestDetail_RenderLinksTab_EmptyShowsHint(t *testing.T) {
	out := renderLinksTab(nil, 80, 5, -1)
	if !strings.Contains(out, "no links") {
		t.Fatalf("expected placeholder, got:\n%s", out)
	}
}

// TestDetail_TabCursor_MovesWithJK: on a tab with rows, j/k moves the
// tab cursor (not the body scroll).
func TestDetail_TabCursor_MovesWithJK(t *testing.T) {
	dm := detailFixture() // 2 comments
	km := newKeymap()
	dm, _ = dm.Update(runeKey('j'), km, nil)
	if dm.tabCursor != 1 {
		t.Fatalf("after j: tabCursor = %d, want 1", dm.tabCursor)
	}
	if dm.scroll != 0 {
		t.Fatalf("after j: scroll = %d, want 0 (j on tab moves cursor)", dm.scroll)
	}
	dm, _ = dm.Update(runeKey('j'), km, nil)
	if dm.tabCursor != 1 { // clamped at len-1
		t.Fatalf("after second j: tabCursor = %d, want 1 (clamped)", dm.tabCursor)
	}
	dm, _ = dm.Update(runeKey('k'), km, nil)
	if dm.tabCursor != 0 {
		t.Fatalf("after k: tabCursor = %d, want 0", dm.tabCursor)
	}
}

// TestDetail_TabSwitch_ResetsCursor: switching tabs resets the row
// cursor so a stale index doesn't carry over to a different-length tab.
func TestDetail_TabSwitch_ResetsCursor(t *testing.T) {
	dm := detailFixture()
	km := newKeymap()
	dm, _ = dm.Update(runeKey('j'), km, nil)
	if dm.tabCursor != 1 {
		t.Fatalf("setup: tabCursor = %d, want 1", dm.tabCursor)
	}
	dm, _ = dm.Update(tea.KeyMsg{Type: tea.KeyTab}, km, nil)
	if dm.tabCursor != 0 {
		t.Fatalf("after tab switch: tabCursor = %d, want 0", dm.tabCursor)
	}
}

// runBatch unwraps a tea.Batch wrapper and runs every nested cmd in
// sequence so jump-nav tests can observe the side effects of fetchIssue.
// We deliberately ignore tea.Cmd return values from sub-cmds because the
// fake API records what we want to assert.
func runBatch(cmd tea.Cmd) {
	if cmd == nil {
		return
	}
	msg := cmd()
	batch, ok := msg.(tea.BatchMsg)
	if !ok {
		return
	}
	for _, sub := range batch {
		if sub != nil {
			_ = sub()
		}
	}
}

// TestDetail_EnterOnEventWithIssueRef_JumpsAndStacks: pressing Enter on
// a link event whose payload carries to_number pushes the current dm
// onto the nav stack and dispatches a GetIssue for the referenced
// issue. The fake API captures the issue number requested.
func TestDetail_EnterOnEventWithIssueRef_JumpsAndStacks(t *testing.T) {
	api := &fakeDetailAPI{
		getIssueResult: &Issue{Number: 11, Title: "linked target"},
	}
	dm := detailFixture()
	dm.activeTab = tabEvents
	dm.events = []EventLogEntry{
		{Type: "issue.linked", Actor: "wesm",
			Payload: map[string]any{
				"type": "blocks", "to_number": float64(11),
			}},
	}
	dm.tabCursor = 0
	km := newKeymap()
	dm, cmd := dm.Update(tea.KeyMsg{Type: tea.KeyEnter}, km, api)
	if cmd == nil {
		t.Fatal("expected jump cmd from Enter")
	}
	if len(dm.navStack) != 1 {
		t.Fatalf("navStack length = %d, want 1", len(dm.navStack))
	}
	if dm.issue != nil {
		t.Fatalf("expected new dm to be loading (issue=nil), got %+v", dm.issue)
	}
	runBatch(cmd)
	if api.lastGetIssue != 11 {
		t.Fatalf("api.lastGetIssue = %d, want 11", api.lastGetIssue)
	}
}

// TestDetail_EnterOnLinkEntry_JumpsToTarget: pressing Enter on a link
// row jumps to the link's ToNumber.
func TestDetail_EnterOnLinkEntry_JumpsToTarget(t *testing.T) {
	api := &fakeDetailAPI{
		getIssueResult: &Issue{Number: 7, Title: "target"},
	}
	dm := detailFixture()
	dm.activeTab = tabLinks
	dm.tabCursor = 0
	km := newKeymap()
	dm, cmd := dm.Update(tea.KeyMsg{Type: tea.KeyEnter}, km, api)
	if cmd == nil {
		t.Fatal("expected jump cmd from Enter on link")
	}
	if len(dm.navStack) != 1 {
		t.Fatalf("navStack length = %d, want 1", len(dm.navStack))
	}
	runBatch(cmd)
	if api.lastGetIssue != 7 {
		t.Fatalf("api.lastGetIssue = %d, want 7", api.lastGetIssue)
	}
}

// TestDetail_EnterOnComment_NoJump: pressing Enter on a comment row
// does not jump (comments tab has no jump action).
func TestDetail_EnterOnComment_NoJump(t *testing.T) {
	api := &fakeDetailAPI{}
	dm := detailFixture() // active tab is tabComments
	km := newKeymap()
	dm, cmd := dm.Update(tea.KeyMsg{Type: tea.KeyEnter}, km, api)
	if cmd != nil {
		t.Fatalf("expected nil cmd from Enter on comment, got %T", cmd)
	}
	if len(dm.navStack) != 0 {
		t.Fatalf("navStack should be empty after no-op Enter, got %d", len(dm.navStack))
	}
}

// TestDetail_EscFromStackedDetail_PopsToPrior: Esc on a stacked detail
// pops the nav stack, restoring the prior detailModel verbatim.
func TestDetail_EscFromStackedDetail_PopsToPrior(t *testing.T) {
	prior := detailModel{
		issue:     &Issue{Number: 42, Title: "prior"},
		activeTab: tabEvents,
		tabCursor: 1,
	}
	current := detailModel{
		issue:    &Issue{Number: 11, Title: "stacked"},
		navStack: []detailModel{prior},
	}
	km := newKeymap()
	got, cmd := current.Update(tea.KeyMsg{Type: tea.KeyEsc}, km, nil)
	if cmd != nil {
		t.Fatalf("expected nil cmd (no popDetailMsg), got %T", cmd)
	}
	if got.issue == nil || got.issue.Number != 42 {
		t.Fatalf("expected pop to issue #42, got %+v", got.issue)
	}
	if got.activeTab != tabEvents {
		t.Fatalf("activeTab not restored: got %d, want tabEvents", got.activeTab)
	}
	if got.tabCursor != 1 {
		t.Fatalf("tabCursor not restored: got %d, want 1", got.tabCursor)
	}
	if len(got.navStack) != 0 {
		t.Fatalf("navStack should be empty after pop, got %d", len(got.navStack))
	}
}

// TestDetail_EscFromTopLevelDetail_ReturnsToList: with an empty nav
// stack, Esc emits popDetailMsg as before.
func TestDetail_EscFromTopLevelDetail_ReturnsToList(t *testing.T) {
	dm := detailFixture()
	km := newKeymap()
	_, cmd := dm.Update(tea.KeyMsg{Type: tea.KeyEsc}, km, nil)
	if cmd == nil {
		t.Fatal("expected popDetailCmd")
	}
	if _, ok := cmd().(popDetailMsg); !ok {
		t.Fatalf("expected popDetailMsg, got %T", cmd())
	}
}

// TestDetail_NavStackCappedAtOne: trying to jump from a level-2 detail
// no-ops because the stack is at cap. Esc still pops as expected.
func TestDetail_NavStackCappedAtOne(t *testing.T) {
	api := &fakeDetailAPI{getIssueResult: &Issue{Number: 99}}
	prior := detailModel{issue: &Issue{Number: 42}, activeTab: tabLinks}
	dm := detailModel{
		issue:     &Issue{Number: 11},
		activeTab: tabLinks,
		links:     []LinkEntry{{ID: 1, Type: "blocks", ToNumber: 99}},
		navStack:  []detailModel{prior}, // already at cap
	}
	km := newKeymap()
	dm, cmd := dm.Update(tea.KeyMsg{Type: tea.KeyEnter}, km, api)
	if cmd != nil {
		t.Fatalf("expected nil cmd at nav cap, got %T", cmd)
	}
	if dm.issue.Number != 11 {
		t.Fatalf("dm.issue should be unchanged, got %d", dm.issue.Number)
	}
	if len(dm.navStack) != 1 {
		t.Fatalf("navStack should still be at 1, got %d", len(dm.navStack))
	}
}

// TestDetail_EnterOnEventWithoutPayload_NoOp: an event whose payload
// has no to_number/issue_number is not jumpable.
func TestDetail_EnterOnEventWithoutPayload_NoOp(t *testing.T) {
	api := &fakeDetailAPI{getIssueResult: &Issue{Number: 1}}
	dm := detailModel{
		issue:     &Issue{Number: 11},
		activeTab: tabEvents,
		events:    []EventLogEntry{{Type: "issue.created"}},
	}
	km := newKeymap()
	_, cmd := dm.Update(tea.KeyMsg{Type: tea.KeyEnter}, km, api)
	if cmd != nil {
		t.Fatalf("expected nil cmd for non-jumpable event, got %T", cmd)
	}
}

// TestDetail_ApplyRowCursor_StylesOnlyWhenSelected confirms the cursor
// indirection: the helper styles the line via selectedStyle when
// isCursor=true and otherwise returns the raw text. The package-level
// selectedStyle is not asserted against ANSI escapes here because the
// test environment may run in colorNone mode (no TTY); we instead
// confirm the function defers to selectedStyle.Render(line) which the
// theme tests verify is materially different from the raw input.
func TestDetail_ApplyRowCursor_StylesOnlyWhenSelected(t *testing.T) {
	plain := applyRowCursor("hello", false)
	if plain != "hello" {
		t.Fatalf("non-cursor branch should return raw text, got %q", plain)
	}
	styled := applyRowCursor("hello", true)
	want := selectedStyle.Render("hello")
	if styled != want {
		t.Fatalf("cursor branch did not apply selectedStyle: got %q want %q",
			styled, want)
	}
}
