package tui

import (
	"context"
	"os"
	"strings"
	"time"

	tea "github.com/charmbracelet/bubbletea"
)

type viewID int

const (
	viewList viewID = iota
	viewDetail
	viewHelp
	viewEmpty
)

// Model is the top-level Bubble Tea model. Sub-views are embedded by
// value so Update can mutate them in place without indirection. The
// detail sub-view is held by value (not pointer) so its scroll/tab
// state lives across opens of the same issue, and so popDetailMsg
// returns to a list whose cursor and filters are unchanged.
//
// SSE state lives on the parent model so the consumer goroutine has a
// fixed channel to push into and the detail/list sub-views can route
// invalidation. sseCh bridges the long-lived goroutine into the TEA
// loop via waitForSSE; sseStatus drives the status-bar reconnect
// indicator; pendingRefetch coalesces bursts of events into a single
// 150ms-debounced list refetch; cache holds the current list snapshot
// so a stale-mark + clean refetch can short-circuit redundant work.
//
// toastNow is a clock injection point: production uses time.Now, tests
// replace it to drive deterministic toast expiry.
type Model struct {
	opts           Options
	api            *Client
	scope          scope
	view           viewID
	prevView       viewID
	width          int
	height         int
	keymap         keymap
	list           listModel
	detail         detailModel
	sseCh          chan tea.Msg
	sseStatus      sseConnState
	pendingRefetch bool
	cache          *issueCache
	toast          *toast
	toastNow       func() time.Time
	// nextGen is the monotonic detail-open generation counter. Every
	// open or jump allocates a fresh value via ++ so a fetch in flight
	// from a previously-jumped issue cannot match a newly-opened issue
	// that happens to occupy the smaller-gen snapshot's place after a
	// handleBack restoration. Detail-side fetches and mutations carry
	// the gen at dispatch time; applyFetched/applyMutation drop
	// messages whose gen no longer matches dm.gen.
	nextGen int64
	// input is the active inline command bar / panel-local prompt /
	// centered form. inputNone means no input is open and global keys
	// route normally. While an input is open, all non-Quit keys go to
	// the input's bubbles model; canQuit() gates global keys.
	input inputState
	// modal is the active centered confirm/info overlay (M3.5b: the
	// quit-confirm modal; future plans add delete-confirm etc.).
	// modalNone is the quiescent state. While a modal is open it
	// owns key dispatch — `y`/`n`/`esc` route through it instead of
	// reaching list/detail handlers.
	modal modalKind
	// nextFormGen is the monotonic centered-form ID counter. Every
	// open of an M4 centered form (body editor / comment) allocates
	// a fresh value via ++. The form's formGen rides with the
	// $EDITOR handoff so a stale editorReturnedMsg arriving after
	// the form was closed (or re-opened against a different issue)
	// is rejected before its content can land in a different form's
	// textarea.
	nextFormGen int64
}

// initialModel constructs the root Bubble Tea model. Style vars are
// populated against opts.Stdout (or os.Stdout when nil) so unit tests
// that bypass Run still see live styles. Run re-runs applyDefaultColorMode
// once it has the opts.Stdout to pin color detection to the real stream.
//
// sseCh is allocated buffered (16) so a brief stall in Update does not
// block the SSE goroutine on its forwardFrame send. cache is allocated
// here rather than on first event so the SSE-driven invalidation never
// has to nil-check it.
func initialModel(opts Options) Model {
	applyDefaultColorMode(opts.Stdout)
	lm := newListModel()
	lm.actor = resolveTUIActor()
	return Model{
		opts:      opts,
		view:      viewList,
		keymap:    newKeymap(),
		list:      lm,
		detail:    newDetailModel(),
		sseCh:     make(chan tea.Msg, 16),
		sseStatus: sseConnected,
		cache:     newIssueCache(),
		toastNow:  time.Now,
	}
}

// resolveTUIActor mirrors cmd/kata's actor precedence (env → fallback)
// minus the --as flag and git fallback: the TUI has no flag plumbing
// here and we keep the dependency surface small. Tasks 9/10 re-evaluate
// once the broader mutation path lands and may add a git fallback.
func resolveTUIActor() string {
	if v := os.Getenv("KATA_AUTHOR"); v != "" {
		return v
	}
	return "anonymous"
}

// Init dispatches the initial fetch unless boot landed on the empty
// state or no client is wired (the latter happens in unit tests that
// drive the model directly via teatest.NewTestModel and feed
// initialFetchMsg by hand). The list view sets loading=true at
// construction so the spinner shows until initialFetchMsg arrives.
//
// waitForSSE is registered alongside fetchInitial so the SSE goroutine
// (spawned by Run after this Init returns) has a reader the moment its
// first frame is ready. The reader is replenished on every SSE message
// in Update so the channel is continuously drained.
func (m Model) Init() tea.Cmd {
	// EnableBracketedPaste makes multi-rune pastes arrive as a single
	// KeyMsg the textinput can ingest atomically (via its own
	// Sanitize). Without it, every rune comes through as a separate
	// keystroke — slow visible characters and any newline in the
	// clipboard prematurely fires Enter on the inline new-issue row /
	// command bars.
	if m.view == viewEmpty || m.api == nil {
		return tea.Batch(tea.EnableBracketedPaste, m.waitForSSE())
	}
	return tea.Batch(tea.EnableBracketedPaste, m.fetchInitial(), m.waitForSSE())
}

// waitForSSE is the bridge from the SSE goroutine into the TEA loop. It
// returns a tea.Cmd that blocks on the next message in m.sseCh. tea.Cmds
// are one-shot, so every Update branch that consumes an SSE message
// returns waitForSSE() again to keep the bridge alive. A nil sseCh
// (zeroed Model in tests that bypass initialModel) is treated as a
// terminating bridge so unit tests don't deadlock waiting on a channel
// that will never see a write.
func (m Model) waitForSSE() tea.Cmd {
	if m.sseCh == nil {
		return nil
	}
	ch := m.sseCh
	return func() tea.Msg {
		msg, ok := <-ch
		if !ok {
			return nil
		}
		return msg
	}
}

// fetchInitial returns a command that issues the first list fetch. The
// scope drives whether this is single-project or cross-project. The
// 5s ceiling matches the daemon's typical p95 list latency.
//
// dispatchKey captures the scope/filter the request was sent under;
// populateCache drops the response if the user has changed scope or
// filter since dispatch so a slow initial fetch can't clobber a fresh
// post-toggle list.
func (m Model) fetchInitial() tea.Cmd {
	api, sc, filter := m.api, m.scope, initialFilter(m.opts)
	dispatchKey := cacheKey{
		allProjects: sc.allProjects, projectID: sc.projectID, filter: filter,
	}
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		var (
			issues []Issue
			err    error
		)
		if sc.allProjects {
			issues, err = api.ListAllIssues(ctx, filter)
		} else {
			issues, err = api.ListIssues(ctx, sc.projectID, filter)
		}
		return initialFetchMsg{dispatchKey: dispatchKey, issues: issues, err: err}
	}
}

// initialFilter projects opts into the ListFilter the boot fetch uses.
// Today there is nothing on Options that drives the boot filter, but
// keeping this seam means future tasks can add it without re-shaping
// fetchInitial. The wire request itself only carries Status because the
// daemon's ListIssuesRequest accepts {status, limit} and nothing else.
func initialFilter(_ Options) ListFilter {
	return ListFilter{}
}

// Update routes messages to the active sub-view. Quit is handled at the
// top level so it works from every view, EXCEPT while a list-view inline
// prompt or a detail-view modal is active: typing 'q' into a prompt or
// modal must reach the buffer instead of quitting. The same gate applies
// to ?, R, and any future global key.
//
// openDetailMsg / popDetailMsg are intercepted before the per-view
// dispatch because the view switch lives at this level. The detail
// sub-model is reset on open so a new issue starts at scroll=0 with the
// comments tab — but the list sub-model is untouched on pop, preserving
// the user's cursor and filter state across the round trip.
func (m Model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	if next, cmd, ok := m.routeTopLevel(msg); ok {
		return next, cmd
	}
	if next, cmd, ok := m.routeSSE(msg); ok {
		return next, cmd
	}
	switch msg.(type) {
	case initialFetchMsg, refetchedMsg:
		if m.isStaleListFetch(msg) {
			return m, nil
		}
		m = m.populateCache(msg)
	}
	if mut, ok := msg.(mutationDoneMsg); ok {
		next, cmd := m.routeMutation(mut)
		return next, cmd
	}
	// Editor returns from a centered form's ctrl+e handoff land here
	// before dispatchToView so the writeback can hit m.input. formGen=0
	// (legacy detail-side shell-out) falls through to detail.Update.
	if er, ok := msg.(editorReturnedMsg); ok && er.formGen != 0 {
		next, cmd := m.routeEditorReturn(er)
		return next, cmd
	}
	return m.dispatchToView(msg)
}

// isStaleListFetch reports whether a list-fetch message was dispatched
// under a scope/filter that no longer matches the current state. Stale
// fetches are dropped before reaching populateCache or dispatchToView
// so the cache/list aren't churned by a slow reply that the user has
// already moved past.
func (m Model) isStaleListFetch(msg tea.Msg) bool {
	dispatchKey, _, _ := fetchPayload(msg)
	return !cacheKeysEqual(dispatchKey, m.currentCacheKey())
}

// routeMutation dispatches a mutationDoneMsg to the view that
// originated the mutation, with a gen-aware path for detail
// completions that arrive after the user opened a different issue.
//
// Three cases:
//
//  1. origin=list, view!=viewList → apply directly to listModel so
//     the list status/refetch fires even though the user is in
//     detail view now.
//  2. origin=detail, view!=viewDetail → apply directly to dm; its
//     gen is unchanged (no new open since pop) so applyMutation
//     accepts the message.
//  3. origin=detail, view==viewDetail, mut.gen != m.detail.gen →
//     the user opened a *different* detail issue between dispatch
//     and arrival. dm.applyMutation would silently drop the message
//     on the gen mismatch and leave the list cache stale. Mark the
//     cache stale here so the next list refetch (or SSE invalidation)
//     repopulates the rows the original mutation touched.
//
// Without case (3), a "close issue A in detail → jump to issue B
// before the close completes" sequence would update neither A's UI
// (it's gone) nor any cache, and the list rows would stay stale
// until an unrelated SSE event happened to refresh them.
func (m Model) routeMutation(mut mutationDoneMsg) (tea.Model, tea.Cmd) {
	if mut.origin == "form" {
		return m.routeFormMutation(mut)
	}
	if mut.origin == "list" && m.view != viewList {
		var cmd tea.Cmd
		m.list, cmd = m.list.applyMutation(mut, m.api, m.scope)
		return m, cmd
	}
	if mut.origin == "detail" {
		if m.view != viewDetail {
			var cmd tea.Cmd
			m.detail, cmd = m.detail.applyMutation(mut, m.api)
			return m, cmd
		}
		if mut.gen != m.detail.gen {
			// Stale-to-current-detail: the original UI is gone but
			// the underlying data still changed. Mark the list cache
			// stale and schedule a debounced refetch so the rows the
			// original mutation touched repopulate without waiting
			// for SSE (roborev #102 finding 1 follow-up).
			if m.cache != nil {
				m.cache.markStale()
			}
			if !m.pendingRefetch && m.api != nil {
				m.pendingRefetch = true
				return m, debouncedRefetch(refetchDebounce)
			}
			return m, nil
		}
	}
	next, cmd := m.dispatchToView(mut)
	// Post-create chain (M4): a successful inline-row create opens the
	// post-create body editor for the newly-created issue. The list-
	// side applyMutation has already seeded selectedNumber/cursor for
	// the new row by the time we get here, so esc-out lands the user
	// on the right list cursor.
	if isCreateSuccess(mut) {
		nm, ok := next.(Model)
		if ok {
			nm = nm.openBodyEditPostCreate(mut.resp.Issue.Number)
			return nm, cmd
		}
	}
	return next, cmd
}

// isCreateSuccess reports whether mut is a successful create-issue
// mutation that should chain into the post-create body editor.
func isCreateSuccess(mut mutationDoneMsg) bool {
	return mut.origin == "list" && mut.kind == "create" &&
		mut.err == nil && mut.resp != nil && mut.resp.Issue != nil
}

// routeTopLevel handles non-SSE messages that the parent Model owns:
// resize, global quit, view-switch, detail-open/pop, input shell
// open/key. ok=true means the message was handled here.
func (m Model) routeTopLevel(msg tea.Msg) (tea.Model, tea.Cmd, bool) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.width, m.height = msg.Width, msg.Height
		return m, nil, true
	case tea.KeyMsg:
		// Modal owns input when active. Enter the modal-specific
		// handler before falling through to input/global routing.
		if m.modal != modalNone {
			next, cmd := m.routeModalKey(msg)
			return next, cmd, true
		}
		if m.input.kind != inputNone {
			next, cmd := m.routeInputKey(msg)
			return next, cmd, true
		}
		if next, cmd, ok := m.routeGlobalKey(msg); ok {
			return next, cmd, true
		}
		// Detail-view `e` and `c` open M4 centered forms instead of
		// shelling out to $EDITOR. Routed at the Model level because
		// the form lives on m.input, which detail.Update can't reach.
		if next, cmd, ok := m.routeDetailFormKey(msg); ok {
			return next, cmd, true
		}
	case openInputMsg:
		next := m.openInput(msg.kind)
		return next, nil, true
	case openDetailMsg:
		next, cmd := m.handleOpenDetail(msg)
		return next, cmd, true
	case jumpDetailMsg:
		next, cmd := m.handleJumpDetail(msg)
		return next, cmd, true
	case popDetailMsg:
		m.view = viewList
		return m, nil, true
	}
	return m, nil, false
}

// openInput constructs the inputState for a kind and applies the
// initial state mutations the input needs (e.g. preFilter snapshot
// for bars; issue context for panel prompts). For inline command
// bars, the search/owner buffer pre-fills from the existing filter
// so the user can refine an active filter without retyping. For
// panel-local prompts, the issue number lands in the prompt title.
// For the inline new-issue row (M3.5c), the row state has no issue
// context — Enter dispatches CreateIssue with the typed title. For
// centered forms, openInput delegates to openBodyEditForm /
// openCommentForm — they need extra context (current body, issue
// target) so they're called directly from the detail key handler
// instead of via openInputMsg.
func (m Model) openInput(kind inputKind) Model {
	switch {
	case kind == inputSearchBar:
		m.input = newSearchBar(m.list.filter)
	case kind == inputOwnerBar:
		m.input = newOwnerBar(m.list.filter)
	case kind == inputNewIssueRow:
		m.input = newNewIssueRow()
	case kind.isPanelPrompt():
		num := int64(0)
		if m.detail.issue != nil {
			num = m.detail.issue.Number
		}
		m.input = newPanelPrompt(kind, num)
	}
	return m
}

// openBodyEditForm opens the centered body editor for the currently-
// open detail issue. Allocates a fresh formGen so a stale editor
// return from a previous form is rejected. Returns the model
// untouched if there's no open detail issue.
func (m Model) openBodyEditForm() Model {
	if m.detail.issue == nil {
		return m
	}
	target := formTarget{
		projectID:   m.scope.projectID,
		issueNumber: m.detail.issue.Number,
		detailGen:   m.detail.gen,
	}
	m.nextFormGen++
	form := newBodyEditForm(target, m.detail.issue.Body)
	form.formGen = m.nextFormGen
	m.input = form
	return m
}

// openBodyEditPostCreate opens the post-create body editor for a
// freshly-created issue. Called from the create-mutation success
// branch so the user can immediately add body content; esc keeps the
// body empty (the issue exists either way) and lands the user on the
// new issue's detail view.
func (m Model) openBodyEditPostCreate(issueNumber int64) Model {
	target := formTarget{
		projectID:   m.scope.projectID,
		issueNumber: issueNumber,
		detailGen:   m.detail.gen,
	}
	m.nextFormGen++
	form := newBodyEditPostCreate(target)
	form.formGen = m.nextFormGen
	m.input = form
	return m
}

// openCommentForm opens the centered comment editor for the
// currently-open detail issue.
func (m Model) openCommentForm() Model {
	if m.detail.issue == nil {
		return m
	}
	target := formTarget{
		projectID:   m.scope.projectID,
		issueNumber: m.detail.issue.Number,
		detailGen:   m.detail.gen,
	}
	m.nextFormGen++
	form := newCommentForm(target)
	form.formGen = m.nextFormGen
	m.input = form
	return m
}

// routeInputKey delivers a key into the active input shell and
// applies the resulting action. Bars apply their buffer to lm.filter
// live on every keystroke (no debounce — filters are client-side).
// Panel prompts (M3b) commit on action only — no live mirror; they
// dispatch the mutation via dispatchPanelPromptCommit. Commit closes
// the input; cancel restores any pre-open snapshot (bars only).
func (m Model) routeInputKey(msg tea.KeyMsg) (Model, tea.Cmd) {
	next, action := m.input.Update(msg)
	m.input = next
	switch action {
	case actionCommit:
		return m.commitInput()
	case actionCancel:
		return m.cancelInput()
	case actionEditorHandoff:
		return m.handoffToEditor()
	}
	if m.input.kind.isCommandBar() {
		m = m.applyLiveBarFilter()
	}
	return m, nil
}

// handoffToEditor launches editorCmd on the current form's buffer,
// tagging the request with the form's formGen so the eventual
// editorReturnedMsg can reject itself if the form was closed or
// re-opened in the meantime. The form state stays in m.input — the
// editor return writes back into the textarea instead of submitting.
func (m Model) handoffToEditor() (Model, tea.Cmd) {
	if !m.input.kind.isCenteredForm() {
		return m, nil
	}
	f := m.input.activeField()
	if f == nil {
		return m, nil
	}
	editorKind := editorKindFor(m.input.kind)
	return m, editorCmd(editorKind, f.value(), m.input.formGen)
}

// routeDetailFormKey intercepts the detail-view `e` and `c` keys
// and opens the corresponding centered form instead of letting them
// reach the (now-removed) shell-out path. Returns ok=false for
// non-detail views so the key falls through to dispatchToView.
//
// The form needs Model-level state (m.input + nextFormGen counter),
// so this can't live in detail.Update. Gated by view + the absence
// of an open issue so an `e` press during loading is a no-op.
func (m Model) routeDetailFormKey(msg tea.KeyMsg) (Model, tea.Cmd, bool) {
	if m.view != viewDetail || m.detail.issue == nil {
		return m, nil, false
	}
	switch {
	case m.keymap.EditBody.matches(msg):
		return m.openBodyEditForm(), nil, true
	case m.keymap.NewComment.matches(msg):
		return m.openCommentForm(), nil, true
	}
	return m, nil, false
}

// editorKindFor maps a form kind onto the editorReturnedMsg kind tag.
// The tag is informational only at the Model layer (the formGen is
// what gates routing) but kept consistent with editor.go for future
// reuse.
func editorKindFor(k inputKind) string {
	switch k {
	case inputCommentForm:
		return "comment"
	case inputBodyEditForm, inputBodyEditPostCreate:
		return "edit"
	}
	return ""
}

// routeFormMutation dispatches a form-originated mutationDoneMsg.
// Success: close the form and let the rest of the app re-fetch what
// it needs (the daemon's SSE push will invalidate caches, and an
// open detail view's mutationDoneMsg path will reflect the change).
// Failure: surface the error on the form's status line and clear
// saving so the user can retry. A response that arrives after the
// user already cancelled the form (or it was somehow cleared) is
// dropped.
func (m Model) routeFormMutation(mut mutationDoneMsg) (tea.Model, tea.Cmd) {
	if !m.input.kind.isCenteredForm() {
		return m, nil
	}
	if mut.err != nil {
		m.input.saving = false
		m.input.err = mut.kind + " failed: " + mut.err.Error()
		return m, nil
	}
	m.input = inputState{}
	// Hand off to the existing per-view mutation routing so the
	// detail's body / comments list updates. Re-classify as if it
	// came from detail (gen=current detail gen) so existing
	// applyMutation logic kicks in.
	mut.origin = "detail"
	mut.gen = m.detail.gen
	return m.routeMutation(mut)
}

// routeEditorReturn handles editorReturnedMsg at the Model level.
// formGen > 0 means the request came from a centered form's ctrl+e
// handoff; the return is matched against the currently-open form's
// formGen and either writes the content back into the textarea or
// is dropped as stale. formGen == 0 is the legacy detail-side
// shell-out path and falls through to dm.applyEditorReturned.
//
// On editor cancel/error (non-nil err), the form stays open with
// its previous buffer intact and the err surfaces on the form's
// status line. The textarea is NOT repopulated — preserves what the
// user typed before the editor opened.
func (m Model) routeEditorReturn(msg editorReturnedMsg) (Model, tea.Cmd) {
	if msg.formGen == 0 {
		return m, nil
	}
	if !m.input.kind.isCenteredForm() || m.input.formGen != msg.formGen {
		return m, nil
	}
	if msg.err != nil {
		m.input.err = "editor: " + msg.err.Error()
		return m, nil
	}
	if f := m.input.activeField(); f != nil {
		f.setValue(msg.content)
		m.input.fields[m.input.active] = *f
	}
	m.input.err = ""
	return m, nil
}

// applyLiveBarFilter mirrors the active bar's buffer into the
// corresponding lm.filter slot. Each keystroke re-applies the
// filter, which then narrows filteredIssues at render time without a
// network call (Search/Owner are client-side).
func (m Model) applyLiveBarFilter() Model {
	if m.input.kind == inputNone {
		return m
	}
	v := m.input.activeField().value()
	switch m.input.kind {
	case inputSearchBar:
		m.list.filter.Search = v
	case inputOwnerBar:
		m.list.filter.Owner = v
	}
	// Filter changed — clamp the cursor to the new visible-row count
	// so the highlighted row never falls past the end.
	m.list = m.list.clampCursorToFilter()
	return m
}

// commitInput closes the input shell. For command bars, the live-
// mirrored filter stays applied. For panel-local prompts, the
// trimmed buffer dispatches the corresponding detail-side mutation
// via dispatchPanelPromptCommit before the input clears. For the
// inline new-issue row, the title (untrimmed — preserves intentional
// whitespace) dispatches CreateIssue via lm.dispatchCreateIssue.
//
// For centered forms, commitInput keeps the form open with
// saving=true while the mutation is in flight (so a duplicate
// ctrl+s is absorbed by the form's updateForm gate). The form is
// closed by routeMutation when the response arrives. Empty comment
// content surfaces an in-form error and leaves the form open;
// empty body content is allowed (clearing a body is legitimate).
func (m Model) commitInput() (Model, tea.Cmd) {
	kind := m.input.kind
	rawBuf := ""
	if f := m.input.activeField(); f != nil {
		rawBuf = f.value()
	}
	if kind.isCenteredForm() {
		return m.commitFormInput(kind, rawBuf)
	}
	trimmed := strings.TrimSpace(rawBuf)
	m.input = inputState{}
	switch {
	case kind.isPanelPrompt() && trimmed != "":
		var cmd tea.Cmd
		m.detail, cmd = m.detail.dispatchPanelPromptCommit(m.api, kind, trimmed)
		return m, cmd
	case kind == inputNewIssueRow:
		var cmd tea.Cmd
		m.list, cmd = m.list.dispatchCreateIssue(m.api, m.scope, rawBuf)
		return m, cmd
	}
	return m, nil
}

// commitFormInput handles ctrl+s on a centered form. The form stays
// open with saving=true while the daemon round-trip runs; the
// arriving mutationDoneMsg closes it (success: clear and update
// detail; error: surface on the form's status line and leave open).
//
// Comments require non-empty content (an empty comment carries no
// information); body edits accept empty content (clearing a body is
// legitimate). Render-side sanitization elsewhere handles display
// safety; mutation payloads go to the wire untouched so the user's
// content is preserved exactly.
func (m Model) commitFormInput(kind inputKind, rawBuf string) (Model, tea.Cmd) {
	if kind == inputCommentForm && strings.TrimSpace(rawBuf) == "" {
		m.input.err = "comment cannot be empty"
		return m, nil
	}
	m.input.saving = true
	m.input.err = ""
	target := m.input.target
	switch kind {
	case inputCommentForm:
		return m, dispatchFormAddComment(m.api, target, rawBuf, m.list.actor)
	case inputBodyEditForm, inputBodyEditPostCreate:
		return m, dispatchFormEditBody(m.api, target, rawBuf, m.list.actor)
	}
	return m, nil
}

// dispatchFormAddComment is the form-side AddComment dispatch. Tagged
// with origin="form" + the form's formGen so routeMutation can match
// the response to the still-open form.
func dispatchFormAddComment(
	api *Client, target formTarget, body, actor string,
) tea.Cmd {
	pid, num := target.projectID, target.issueNumber
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		resp, err := api.AddComment(ctx, pid, num, body, actor)
		return mutationDoneMsg{
			origin: "form", kind: "form.comment.add", resp: resp, err: err,
		}
	}
}

// dispatchFormEditBody is the form-side EditBody dispatch. Same
// shape as dispatchFormAddComment.
func dispatchFormEditBody(
	api *Client, target formTarget, body, actor string,
) tea.Cmd {
	pid, num := target.projectID, target.issueNumber
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		resp, err := api.EditBody(ctx, pid, num, body, actor)
		return mutationDoneMsg{
			origin: "form", kind: "form.body.edit", resp: resp, err: err,
		}
	}
}

// cancelInput restores any pre-open filter snapshot (bars only) and
// closes the input — undoing every live keystroke the user typed.
// Panel-local prompts have no live mirror, so cancel is just close.
//
// Special case: esc on the post-create body form opens the new
// issue's detail view. The issue exists with an empty body — esc is
// "I don't want to add a body right now," not "discard the issue."
// The user lands on the detail view they would have eventually
// reached anyway; the list refetch the create dispatched will have
// the new issue at the top.
func (m Model) cancelInput() (Model, tea.Cmd) {
	if m.input.kind.isCommandBar() {
		m.list.filter = m.input.preFilter
		m.list = m.list.clampCursorToFilter()
	}
	postCreate := m.input.kind == inputBodyEditPostCreate
	target := m.input.target
	m.input = inputState{}
	if postCreate {
		return m, openDetailFromTarget(target)
	}
	return m, nil
}

// openDetailFromTarget emits an openDetailMsg for the post-create
// chain's esc path, seeding a minimal Issue (the create response
// already has number + project; the rest fills in via the detail
// fetch the open kicks off).
func openDetailFromTarget(t formTarget) tea.Cmd {
	return func() tea.Msg {
		return openDetailMsg{issue: Issue{
			Number: t.issueNumber, ProjectID: t.projectID,
		}}
	}
}

// routeGlobalKey handles the global key family (quit, help, scope
// toggle), gated by canQuit so an open input/modal absorbs the key.
// viewEmpty honors only quit/ctrl+c; ?, R, and any other binding
// fall through silently because the only meaningful action is exit.
//
// `q` opens the quit-confirm modal (msgvault pattern); `ctrl+c`
// remains the immediate-quit escape hatch for power users.
func (m Model) routeGlobalKey(msg tea.KeyMsg) (Model, tea.Cmd, bool) {
	if !m.canQuit() {
		return m, nil, false
	}
	// ctrl+c bypasses the confirm modal — fast quit for power users.
	if msg.Type == tea.KeyCtrlC {
		return m, tea.Quit, true
	}
	if m.keymap.Quit.matches(msg) {
		m.modal = modalQuitConfirm
		return m, nil, true
	}
	if m.view == viewEmpty {
		return m, nil, true
	}
	if m.keymap.Help.matches(msg) {
		return m.toggleHelp(), nil, true
	}
	if m.keymap.ToggleScope.matches(msg) {
		next, cmd := m.handleScopeToggle()
		return next, cmd, true
	}
	return m, nil, false
}

// toggleHelp swaps between viewHelp and the previous view. Pressing ?
// from list/detail enters viewHelp; pressing ? from viewHelp restores
// whatever view the user came from. prevView is preserved so the round
// trip is reversible — q from viewHelp still quits per routeGlobalKey.
func (m Model) toggleHelp() Model {
	if m.view == viewHelp {
		m.view = m.prevView
		return m
	}
	m.prevView = m.view
	m.view = viewHelp
	return m
}

// routeSSE handles the SSE-side message family. Splitting this off
// Update keeps both functions inside the project's ≤8 cyclomatic
// budget. ok=true means the message was handled here.
func (m Model) routeSSE(msg tea.Msg) (tea.Model, tea.Cmd, bool) {
	switch msg := msg.(type) {
	case eventReceivedMsg:
		next, cmd := m.handleEventReceived(msg)
		return next, cmd, true
	case resetRequiredMsg:
		next, cmd := m.handleResetRequired(msg)
		return next, cmd, true
	case sseStatusMsg:
		m.sseStatus = msg.state
		return m, m.waitForSSE(), true
	case refetchTickMsg:
		next, cmd := m.handleRefetchTick()
		return next, cmd, true
	case toastExpiredMsg:
		next, cmd := m.handleToastExpired()
		return next, cmd, true
	}
	return m, nil, false
}

// populateCache updates the single-slot cache after a successful list
// fetch and forwards the result into lm.applyFetched so list state stays
// in sync with the cache. Doing this here (rather than in
// listModel.Update via dispatchToView) keeps the list rows fresh even
// when the help overlay or detail view is active when the fetch lands —
// otherwise toggling back to the list would render the pre-fetch
// snapshot. Errors still update lm.err and clear loading via
// applyFetched but leave the cache untouched so a transient failure
// does not erase the prior snapshot.
//
// Caller responsibility: drop stale fetches via isStaleListFetch
// before invoking populateCache — see Update.
func (m Model) populateCache(msg tea.Msg) Model {
	_, issues, err := fetchPayload(msg)
	if err == nil && m.cache != nil {
		m.cache.put(m.currentCacheKey(), issues)
	}
	m.list = m.list.applyFetched(msg)
	return m
}

// currentCacheKey is the cacheKey for the current scope/filter — the
// authority for "is this fetch still relevant" comparisons.
func (m Model) currentCacheKey() cacheKey {
	return cacheKey{
		allProjects: m.scope.allProjects,
		projectID:   m.scope.projectID,
		filter:      m.list.filter,
	}
}

// fetchPayload extracts (dispatchKey, issues, err) from the two list-
// fetch message shapes so populateCache can share one staleness +
// cache-update path across them.
func fetchPayload(msg tea.Msg) (cacheKey, []Issue, error) {
	switch m := msg.(type) {
	case initialFetchMsg:
		return m.dispatchKey, m.issues, m.err
	case refetchedMsg:
		return m.dispatchKey, m.issues, m.err
	}
	return cacheKey{}, nil, nil
}

// cacheKeysEqual reports whether two cacheKeys denote the same
// scope+filter. cacheKey can't be compared with == because filter.Labels
// is a slice — Go's spec disallows slice equality outside reflect.
func cacheKeysEqual(a, b cacheKey) bool {
	if a.allProjects != b.allProjects || a.projectID != b.projectID {
		return false
	}
	af, bf := a.filter, b.filter
	if af.Status != bf.Status || af.Owner != bf.Owner ||
		af.Author != bf.Author || af.Search != bf.Search {
		return false
	}
	if len(af.Labels) != len(bf.Labels) {
		return false
	}
	for i := range af.Labels {
		if af.Labels[i] != bf.Labels[i] {
			return false
		}
	}
	return true
}

// handleEventReceived marks the cache stale, kicks off (or coalesces
// into) a 150ms-debounced refetch when the event affects the current
// view, refetches the open detail issue when the event names it, and
// always re-arms the SSE bridge so the next frame is awaited.
//
// Affects-view: in single-project scope an event is interesting only
// when it carries our projectID; in all-projects scope every event is
// interesting. Cross-project (projectID == 0) events fall through as
// "ignore" so an unscoped daemon push cannot churn an unrelated view.
func (m Model) handleEventReceived(msg eventReceivedMsg) (tea.Model, tea.Cmd) {
	cmds := []tea.Cmd{m.waitForSSE()}
	if m.eventAffectsView(msg) {
		m.cache.markStale()
		if !m.pendingRefetch {
			m.pendingRefetch = true
			cmds = append(cmds, debouncedRefetch(refetchDebounce))
		}
	}
	if cmd := m.maybeRefetchOpenDetail(msg); cmd != nil {
		cmds = append(cmds, cmd)
	}
	return m, tea.Batch(cmds...)
}

// eventAffectsView is the per-message gate for invalidation. Returns
// true when the event's scope overlaps the current view's scope. An
// empty event projectID can be a system-wide event (not currently
// emitted) — we ignore it rather than refetch every time so a future
// daemon broadcast for an unscoped event can't churn the list.
func (m Model) eventAffectsView(msg eventReceivedMsg) bool {
	if msg.projectID == 0 {
		return false
	}
	if m.scope.allProjects {
		return true
	}
	return msg.projectID == m.scope.projectID
}

// maybeRefetchOpenDetail dispatches the four detail fetches (issue +
// per-tab) when an SSE event names the currently-open detail issue.
// All four run because the event-kind alone isn't enough to know which
// tab needs refreshing — for example, issue.commented refreshes
// comments but issue.linked refreshes links, and issue.relabeled
// touches the body header. Refetching all four is cheap (the daemon
// has these in cache) and keeps every tab fresh without a kind switch.
//
// The match requires both projectID and issueNumber to align with the
// open detail. In all-projects scope, issue numbers are project-scoped,
// so a project-B #42 event must NOT churn an open project-A #42 view.
// Each fetch is tagged with the current detail-open gen so applyFetched
// drops the result if the user navigates away before the response
// lands.
func (m Model) maybeRefetchOpenDetail(msg eventReceivedMsg) tea.Cmd {
	if m.view != viewDetail || m.api == nil {
		return nil
	}
	if m.detail.issue == nil {
		return nil
	}
	if msg.issueNumber == 0 || msg.issueNumber != m.detail.issue.Number {
		return nil
	}
	if msg.projectID != m.detail.scopePID {
		return nil
	}
	pid := m.detail.scopePID
	num := m.detail.issue.Number
	gen := m.detail.gen
	return tea.Batch(
		fetchIssue(m.api, pid, num, gen),
		fetchComments(m.api, pid, num, gen),
		fetchEvents(m.api, pid, num, gen),
		fetchLinks(m.api, pid, num, gen),
	)
}

// handleRefetchTick fires after the debounce window. Clears the
// pending flag and dispatches a refetch when the cache is stale; if a
// race cleared the stale flag (e.g., a manual filter change refetched
// already), the tick is a no-op so we don't spin a redundant request.
func (m Model) handleRefetchTick() (tea.Model, tea.Cmd) {
	m.pendingRefetch = false
	if !m.cache.isStale() {
		return m, m.waitForSSE()
	}
	cmd := m.list.refetchCmd(m.api, m.scope)
	return m, tea.Batch(cmd, m.waitForSSE())
}

// handleResetRequired is the terminal-cache branch: drop everything,
// dispatch an immediate refetch, and surface a 2s 'resynced' toast so
// the user knows the view repopulated under their feet. We re-arm the
// SSE bridge so subsequent frames are awaited, but the goroutine that
// pushed this frame may itself have closed the stream — startSSE will
// reconnect from the same checkpoint via Last-Event-ID. The daemon's
// contract (internal/api/events.go EventReset.EventID == ResetAfterID)
// makes the SSE id: line on this frame the authoritative resume
// cursor, so resetRequiredMsg deliberately carries no payload.
func (m Model) handleResetRequired(_ resetRequiredMsg) (tea.Model, tea.Cmd) {
	m.cache.drop()
	m.pendingRefetch = false
	m.toast = &toast{
		text:      "resynced",
		level:     toastInfo,
		expiresAt: m.toastNow().Add(toastResyncedTTL),
	}
	cmds := []tea.Cmd{m.waitForSSE(), toastExpireCmd(toastResyncedTTL)}
	if m.api != nil {
		cmds = append(cmds, m.list.refetchCmd(m.api, m.scope))
		// If the user is in detail view, the open issue + tabs are also
		// stale — the cursor invalidation behind reset_required means
		// any cached detail data is suspect, not just the list. Batch
		// the four detail fetches so the active detail tab is fresh by
		// the next render.
		if cmd := m.refetchOpenDetail(); cmd != nil {
			cmds = append(cmds, cmd)
		}
	}
	return m, tea.Batch(cmds...)
}

// refetchOpenDetail batches the four detail fetches when the user is
// looking at a detail pane. Used by reset_required and any other path
// that needs to repopulate the open issue without an issue-targeted
// SSE event to drive maybeRefetchOpenDetail.
func (m Model) refetchOpenDetail() tea.Cmd {
	if m.view != viewDetail || m.api == nil || m.detail.issue == nil {
		return nil
	}
	pid := m.detail.scopePID
	num := m.detail.issue.Number
	gen := m.detail.gen
	return tea.Batch(
		fetchIssue(m.api, pid, num, gen),
		fetchComments(m.api, pid, num, gen),
		fetchEvents(m.api, pid, num, gen),
		fetchLinks(m.api, pid, num, gen),
	)
}

// handleToastExpired clears m.toast iff the active toast is past its
// expiry. The wall-clock check guards against a stale tick that fires
// after the user replaced the toast with a fresh one — we don't want
// the second toast to die on the first toast's timer.
func (m Model) handleToastExpired() (tea.Model, tea.Cmd) {
	if m.toast != nil && !m.toastNow().Before(m.toast.expiresAt) {
		m.toast = nil
	}
	return m, m.waitForSSE()
}

// refetchDebounce is the coalescing window for SSE-driven refetches.
// 150ms matches the master spec (§7.1) — long enough that a burst of
// related events (e.g., issue.created + issue.labeled within the same
// mutation) collapses to one fetch, short enough that the user sees
// fresh data before they take their next action.
const refetchDebounce = 150 * time.Millisecond

// toastResyncedTTL is how long the 'resynced' toast lingers before
// toastExpireCmd clears it. 2s matches the plan's spec.
const toastResyncedTTL = 2 * time.Second

// toastNoBindingTTL is how long the "no project bound" toast (R toggle
// without a default project) sticks around. Slightly longer than the
// resynced toast because the user has to act on the hint, not just notice.
const toastNoBindingTTL = 3 * time.Second

// debouncedRefetch returns a tea.Cmd that emits refetchTickMsg after d.
// The TEA loop receives the message, checks the cache, and dispatches
// the actual list refetch via lm.refetchCmd.
func debouncedRefetch(d time.Duration) tea.Cmd {
	return tea.Tick(d, func(time.Time) tea.Msg { return refetchTickMsg{} })
}

// toastExpireCmd schedules a toastExpiredMsg at d. The Update branch
// double-checks the wall clock before clearing the toast so a fresher
// toast cannot be cut short by an earlier timer.
func toastExpireCmd(d time.Duration) tea.Cmd {
	return tea.Tick(d, func(time.Time) tea.Msg { return toastExpiredMsg{} })
}

// canQuit reports whether a global keystroke (q, ?, R) should be
// honored. False while an input shell (M3a/M3b/M3.5c bars/prompts
// /forms) or a confirm modal (M3.5b quit confirm) is open. Both
// gates redirect global keys into the field instead of firing.
func (m Model) canQuit() bool {
	if m.modal != modalNone {
		return false
	}
	if m.input.kind != inputNone {
		return false
	}
	return true
}

// routeModalKey delivers a key to the active centered modal. M3.5b
// only handles modalQuitConfirm: y/Y commits → tea.Quit; n/N/esc
// cancels → close the modal. Other keys are absorbed (the modal owns
// dispatch; nothing reaches the underlying view).
//
// ctrl+c always fast-quits regardless of which modal is open — the
// power-user escape hatch must not be trapped behind a confirmation
// (roborev #111 finding 1).
func (m Model) routeModalKey(msg tea.KeyMsg) (Model, tea.Cmd) {
	if msg.Type == tea.KeyCtrlC {
		return m, tea.Quit
	}
	switch m.modal {
	case modalQuitConfirm:
		switch msg.String() {
		case "y", "Y":
			return m, tea.Quit
		case "n", "N", "esc":
			m.modal = modalNone
			return m, nil
		}
	}
	return m, nil
}

// handleOpenDetail seeds m.detail with the chosen issue and dispatches
// the four concurrent fetches via tea.Batch. The fetches run in
// parallel so the user sees data on whichever tab is active first. The
// detail model also remembers the project_id and all-projects flag so
// the Enter-jump path has them without re-resolving scope.
//
// fetchIssue rides alongside the three tab fetches because the list-row
// Issue seeded above carries no Labels (list rows don't include them
// today) — without the show-response refresh, the detail header would
// stay label-less until a manual refetch. handleJumpDetail dispatches
// the same four-fetch batch; this brings the open-from-list path to
// parity. Same fetchIssue helper, additional call site.
//
// The detail-open generation is allocated from m.nextGen — a Model-
// level monotonic counter — so it never collides with a previously
// jumped-and-backed snapshot's gen. The actor is seeded from the list
// model so detail-side mutations carry the resolved identity rather
// than the empty string.
func (m Model) handleOpenDetail(msg openDetailMsg) (tea.Model, tea.Cmd) {
	iss := msg.issue
	pid := detailProjectID(iss, m.scope)
	m.nextGen++
	// Reset on open is the spec — no per-issue scroll memory.
	m.detail = newDetailModel()
	m.detail.gen = m.nextGen
	m.detail.issue = &iss
	m.detail.scopePID = pid
	m.detail.allProjects = m.scope.allProjects
	m.detail.actor = m.list.actor
	// Per-tab loading flags drive the placeholder strings until each
	// fetch returns; they're cleared (with the per-tab err set) by
	// applyFetched.
	m.detail.commentsLoading = true
	m.detail.eventsLoading = true
	m.detail.linksLoading = true
	m.view = viewDetail
	if m.api == nil {
		return m, nil
	}
	gen := m.detail.gen
	cmds := []tea.Cmd{
		fetchIssue(m.api, pid, iss.Number, gen),
		fetchComments(m.api, pid, iss.Number, gen),
		fetchEvents(m.api, pid, iss.Number, gen),
		fetchLinks(m.api, pid, iss.Number, gen),
	}
	return m, tea.Batch(cmds...)
}

// handleJumpDetail performs an Enter-jump from the detail view to a
// referenced issue. The current detailModel is snapshotted onto its
// own navStack so handleBack can restore it; a fresh detailModel is
// seeded with a new monotonic gen and the four fetches dispatch in
// parallel. The active tab and actor are preserved so the user lands
// in the same context.
//
// detail.handleEnter emits jumpDetailMsg rather than building the new
// detail itself: the gen must come from m.nextGen so a snapshot
// restored by handleBack with an older gen can't trick the next
// jump's gen into colliding with a stale fetch.
func (m Model) handleJumpDetail(msg jumpDetailMsg) (tea.Model, tea.Cmd) {
	if m.api == nil {
		return m, nil
	}
	// jumpDetailCmd is asynchronous (emits jumpDetailMsg via tea.Cmd),
	// so the user can pop back to the list — or the help overlay can
	// open — between the keypress and Model.Update seeing the message.
	// Without this guard the queued jump would mutate hidden detail
	// state and dispatch four fetches against an issue the user is no
	// longer looking at. View check first; navStack cap second.
	if m.view != viewDetail {
		return m, nil
	}
	if len(m.detail.navStack) >= detailNavCap {
		return m, nil
	}
	prior := m.detail
	prior.navStack = nil
	pid := m.detail.scopePID
	m.nextGen++
	gen := m.nextGen
	next := detailModel{
		loading:         true,
		gen:             gen,
		activeTab:       m.detail.activeTab,
		navStack:        append(m.detail.navStack, prior),
		scopePID:        pid,
		allProjects:     m.detail.allProjects,
		actor:           m.detail.actor,
		commentsLoading: true,
		eventsLoading:   true,
		linksLoading:    true,
	}
	m.detail = next
	cmds := []tea.Cmd{
		fetchIssue(m.api, pid, msg.number, gen),
		fetchComments(m.api, pid, msg.number, gen),
		fetchEvents(m.api, pid, msg.number, gen),
		fetchLinks(m.api, pid, msg.number, gen),
	}
	return m, tea.Batch(cmds...)
}

// dispatchToView forwards msg to the active sub-view's Update.
func (m Model) dispatchToView(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch m.view {
	case viewList:
		var cmd tea.Cmd
		m.list, cmd = m.list.Update(msg, m.keymap, m.api, m.scope)
		return m, cmd
	case viewDetail:
		var cmd tea.Cmd
		m.detail, cmd = m.detail.Update(msg, m.keymap, m.api)
		return m, cmd
	}
	return m, nil
}

// View returns the rendered string for the active sub-view. The list
// view consumes its own SSE state + toast inline (via the M1 chrome);
// other views still get the SSE/toast extras appended below since they
// don't carry a status line of their own. Both extras render as empty
// strings in the steady state so the view does not gain spurious blank
// lines.
func (m Model) View() string {
	body := m.viewBody()
	if m.view != viewList && m.view != viewDetail {
		extras := []string{}
		if s := renderSSEStatus(m.sseStatus); s != "" {
			extras = append(extras, s)
		}
		if s := renderToast(m.toast); s != "" {
			extras = append(extras, s)
		}
		if len(extras) > 0 {
			body = joinNonEmpty(append([]string{body}, extras...))
		}
	}
	// M3.5b: a centered modal overlays the rendered view when active.
	if m.modal == modalQuitConfirm {
		return overlayModal(body, renderQuitConfirmModal(), m.width, m.height)
	}
	// M4: centered form overlays the rendered view when active.
	if m.input.kind.isCenteredForm() {
		form := renderCenteredForm(m.input, m.width, m.height)
		return overlayModal(body, form, m.width, m.height)
	}
	return body
}

// viewBody returns the active sub-view rendering. Splitting it off
// View keeps View's cyclomatic budget under the project limit.
func (m Model) viewBody() string {
	switch m.view {
	case viewHelp:
		return renderHelp(m.keymap, m.width, m.list.filter)
	case viewEmpty:
		return renderEmpty(m.width, m.height)
	case viewList:
		return m.list.View(m.width, m.height, m.chrome())
	case viewDetail:
		return m.detail.View(m.width, m.height, m.chrome())
	}
	return ""
}

// chrome assembles the cross-cutting render inputs both the list view
// and the detail view need from Model state: scope, SSE status,
// pending invalidation flag, the active toast (if any), the
// build-time version string, and the active input shell. Centralising
// this keeps the sub-views free of Model coupling.
func (m Model) chrome() viewChrome {
	return viewChrome{
		scope:     m.scope,
		sseStatus: m.sseStatus,
		pending:   m.pendingRefetch,
		toast:     m.toast,
		version:   kataVersion,
		input:     m.input,
	}
}
