package tui

import (
	"context"
	"os"
	"sort"
	"strings"
	"time"
	"unicode/utf8"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
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
	// projectLabels caches per-project label aggregates feeding the
	// `+` suggestion menu. Each entry carries its own dispatch
	// generation so a slow ListLabels response can't clobber a newer
	// dispatch's state — see label_cache.go for the gen-stamping
	// rationale. SSE issue.labeled / issue.unlabeled events
	// invalidate per-project entries so the menu re-fetches when the
	// daemon writes new label rows.
	projectLabels *labelCache
	// nextLabelsGen is the monotonic counter behind projectLabels.
	// Bumped at dispatch time (NOT at response time) so the cache's
	// entry.gen is the load-bearing identifier for staleness — the
	// HTTP request reads the just-bumped value, the response carries
	// it back, and handleLabelsFetched compares msg.gen >= entry.gen
	// to decide whether to apply the result.
	nextLabelsGen int64
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
		opts:          opts,
		view:          viewList,
		keymap:        newKeymap(),
		list:          lm,
		detail:        newDetailModel(),
		sseCh:         make(chan tea.Msg, 16),
		sseStatus:     sseConnected,
		cache:         newIssueCache(),
		toastNow:      time.Now,
		projectLabels: newLabelCache(),
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
	// Label-cache responses route back into the cache before any view
	// gets a shot at them. The handler is pure state — no command —
	// so we fall through to dispatchToView for any unhandled message
	// shape after.
	if lf, ok := msg.(labelsFetchedMsg); ok {
		m = m.handleLabelsFetched(lf)
		return m, nil
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
	// Plan-8: label / create mutations may have changed the project's
	// label aggregate. Refresh the cache so the next `+` open shows
	// fresh counts. Doesn't wait on SSE — the user just took the
	// action; the menu should reflect it immediately.
	if mutAffectsLabelCounts(mut) {
		next, cmd = batchLabelRefresh(next, cmd, mut)
	}
	return next, cmd
}

// mutAffectsLabelCounts reports whether a successful mutation could
// have changed the project's label aggregate. Label add/remove and
// create (which since Plan 8 commit 4 may carry labels via the
// multi-field new-issue form) all qualify; the cache refresh is gated
// against the per-project entry's existence so a plain-title create
// against a project that never had a label menu opened remains a
// zero-cost no-op.
func mutAffectsLabelCounts(mut mutationDoneMsg) bool {
	if mut.err != nil {
		return false
	}
	switch mut.kind {
	case "label.add", "label.remove", "create":
		return true
	}
	return false
}

// batchLabelRefresh dispatches a project-label cache refresh for the
// project the mutation touched, batching the resulting cmd with any
// existing cmd from the per-view dispatch. Returns the new model and
// the combined cmd so the caller can return both atomically.
//
// Gated on cache-entry existence: a project the user never opened the
// suggestion menu for has no entry, so a refetch would be a wasted
// HTTP roundtrip — same gate as maybeRefetchLabels for SSE events.
// The first time the user hits `+` against the project, the menu's
// own dispatch primes the entry; later mutations then refresh it.
func batchLabelRefresh(
	next tea.Model, prior tea.Cmd, mut mutationDoneMsg,
) (tea.Model, tea.Cmd) {
	nm, ok := next.(Model)
	if !ok {
		return next, prior
	}
	pid := projectIDFromMutation(nm, mut)
	if pid == 0 {
		return next, prior
	}
	if nm.projectLabels == nil {
		return next, prior
	}
	if _, exists := nm.projectLabels.byProject[pid]; !exists {
		return next, prior
	}
	nm, refresh := nm.dispatchLabelFetch(pid)
	if refresh == nil {
		return nm, prior
	}
	if prior == nil {
		return nm, refresh
	}
	return nm, tea.Batch(prior, refresh)
}

// projectIDFromMutation picks the project id the mutation acted on.
// Detail-side mutations carry it via dm.scopePID; list-side via the
// response's Issue.ProjectID; falls back to the active scope's
// projectID for shapes that omit it.
func projectIDFromMutation(m Model, mut mutationDoneMsg) int64 {
	if mut.resp != nil && mut.resp.Issue != nil && mut.resp.Issue.ProjectID != 0 {
		return mut.resp.Issue.ProjectID
	}
	if m.view == viewDetail && m.detail.scopePID != 0 {
		return m.detail.scopePID
	}
	return m.scope.projectID
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
		next, cmd := m.openInput(msg.kind)
		return next, cmd, true
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
// panel-local prompts, the issue number lands in the prompt title
// AND the formTarget rides on inputState.target so the autocomplete
// dispatch (label suggestions) can scope to the right project. For
// the centered new-issue form (Plan 8 commit 4), the form has no
// issue context — ctrl+s dispatches CreateIssue with the four-field
// payload. For other centered forms (edit-body, comment), openInput
// delegates to openBodyEditForm / openCommentForm — they need extra
// context (current body, issue target) so they're called directly
// from the detail key handler instead of via openInputMsg.
//
// inputLabelPrompt opens with a parallel dispatchLabelFetch so the
// suggestion menu can render its first frame populated (or with a
// "loading…" placeholder for the round trip's duration). Other
// panel-prompt kinds don't need a fetch — the `-` prompt sources
// from dm.issue.Labels (already loaded), and the others (owner /
// parent / blocker / link) have no autocomplete in this commit.
func (m Model) openInput(kind inputKind) (Model, tea.Cmd) {
	switch {
	case kind == inputSearchBar:
		m.input = newSearchBar(m.list.filter)
	case kind == inputOwnerBar:
		m.input = newOwnerBar(m.list.filter)
	case kind == inputNewIssueForm:
		m.nextFormGen++
		s := newNewIssueForm()
		s.formGen = m.nextFormGen
		m.input = s
	case kind.isPanelPrompt():
		target := m.panelPromptTarget()
		m.input = newPanelPrompt(kind, target)
		if kind == inputLabelPrompt {
			return m.dispatchLabelFetchIfNeeded(target.projectID)
		}
	}
	return m, nil
}

// panelPromptTarget builds the formTarget for a detail-side panel
// prompt: scopePID is authoritative (works for both single-project
// and all-projects scope), issueNumber + detailGen come from the
// open detail's identity. Zero values when no detail is open
// (defensive — shouldn't happen via the normal key path).
func (m Model) panelPromptTarget() formTarget {
	if m.detail.issue == nil {
		return formTarget{}
	}
	return formTarget{
		projectID:   m.detail.scopePID,
		issueNumber: m.detail.issue.Number,
		detailGen:   m.detail.gen,
	}
}

// dispatchLabelFetchIfNeeded triggers a label fetch only when the
// cache has no entry for pid. Re-opening the prompt against a project
// we already cached must NOT churn a redundant request — the cache's
// existing entry (and any in-flight fetch flagged on it) is the
// authoritative source. Returns the model and either the dispatch
// cmd or nil.
func (m Model) dispatchLabelFetchIfNeeded(pid int64) (Model, tea.Cmd) {
	if pid == 0 || m.projectLabels == nil {
		return m, nil
	}
	if _, ok := m.projectLabels.byProject[pid]; ok {
		return m, nil
	}
	return m.dispatchLabelFetch(pid)
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
//
// Label prompts (`+` / `-`) post-process the input: ↑/↓ already
// adjusted suggestHighlight in inputState.Update; we wrap it modulo
// the visible-suggestion count here. Tab completes the buffer to the
// highlighted suggestion's label (suggestion source is computed at
// the Model level — see suggestionsForPrompt).
func (m Model) routeInputKey(msg tea.KeyMsg) (Model, tea.Cmd) {
	prevKind := m.input.kind
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
	if isLabelPromptKind(prevKind) {
		m = m.applyLabelPromptKey(msg)
	}
	return m, nil
}

// applyLabelPromptKey post-processes a key into a label prompt:
// wraps suggestHighlight modulo the suggestion count and (on Tab)
// completes the buffer to the highlighted suggestion's label. The
// input layer signals "handled" for ↑/↓/⇥ but doesn't have the
// suggestion list in scope; this is where the wrap + completion run.
func (m Model) applyLabelPromptKey(msg tea.KeyMsg) Model {
	all := m.suggestionsForPrompt(m.input)
	buf := ""
	if f := m.input.activeField(); f != nil {
		buf = f.value()
	}
	visible := filterSuggestions(all, buf)
	n := len(visible)
	if n > 0 {
		m.input.suggestHighlight = ((m.input.suggestHighlight % n) + n) % n
	} else {
		m.input.suggestHighlight = 0
	}
	if msg.Type == tea.KeyTab && n > 0 {
		picked := visible[m.input.suggestHighlight].Label
		if f := m.input.activeField(); f != nil {
			f.setValue(picked)
			// SetCursor takes a rune index (clamped against the
			// bubbles textinput's internal []rune buffer); use
			// utf8.RuneCountInString so multi-byte labels position
			// the cursor at the end visually rather than past it.
			f.input.SetCursor(utf8.RuneCountInString(picked))
			m.input.fields[m.input.active] = *f
		}
	}
	return m
}

// suggestionsForPrompt returns the suggestion source for an open
// label prompt. `+` reads from the per-project label cache; `-` from
// the open detail's currently-attached labels (no separate fetch
// needed — dm.issue.Labels is authoritative). Returns nil for any
// other kind so callers can use the empty slice as a "no menu" gate.
func (m Model) suggestionsForPrompt(s inputState) []LabelCount {
	switch s.kind {
	case inputLabelPrompt:
		if m.projectLabels == nil {
			return nil
		}
		return m.projectLabels.byProject[s.target.projectID].labels
	case inputRemoveLabelPrompt:
		var attached []string
		if m.detail.issue != nil {
			attached = m.detail.issue.Labels
		}
		out := make([]LabelCount, len(attached))
		for i, l := range attached {
			// Count is irrelevant for `-` — the renderer omits the
			// count column when every entry has count==0.
			out[i] = LabelCount{Label: l, Count: 0}
		}
		return out
	}
	return nil
}

// filterSuggestions returns a copy of all sorted by count desc, then
// label asc, with a case-insensitive prefix filter applied. For `-`
// where counts are 0, the count tie reduces the order to label asc
// — the secondary sort is the effective one.
func filterSuggestions(all []LabelCount, prefix string) []LabelCount {
	out := make([]LabelCount, 0, len(all))
	pfx := strings.ToLower(strings.TrimSpace(prefix))
	for _, lc := range all {
		if pfx == "" || strings.HasPrefix(strings.ToLower(lc.Label), pfx) {
			out = append(out, lc)
		}
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Count != out[j].Count {
			return out[i].Count > out[j].Count
		}
		return out[i].Label < out[j].Label
	})
	return out
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
// reuse. The new-issue form returns "create" so a future log/audit
// path can attribute the editor session correctly.
func editorKindFor(k inputKind) string {
	switch k {
	case inputCommentForm:
		return "comment"
	case inputBodyEditForm:
		return "edit"
	case inputNewIssueForm:
		return "create"
	}
	return ""
}

// routeFormMutation dispatches a form-originated mutationDoneMsg.
// Success: close the form and let the rest of the app re-fetch what
// it needs. Failure: surface the error on the form's status line and
// clear saving so the user can retry. A response that arrives after
// the user already cancelled the form (or it was somehow cleared) is
// dropped.
//
// Two success paths:
//
//  1. inputNewIssueForm — route through list create handling so
//     selectedNumber seeds with the new issue's number, the cursor
//     lands on it after refetch, and the list status hint surfaces
//     ("created #N"). The form does NOT auto-open the new issue's
//     detail view; the user lands back on the list with the new row
//     selected (Plan 8 commit 4 — replaces the M4 post-create chain).
//
//  2. inputBodyEditForm / inputCommentForm — re-classify as origin=
//     detail so the existing detail applyMutation logic refreshes
//     the body / comments list.
func (m Model) routeFormMutation(mut mutationDoneMsg) (tea.Model, tea.Cmd) {
	if !m.input.kind.isCenteredForm() {
		return m, nil
	}
	if mut.err != nil {
		m.input.saving = false
		m.input.err = mut.kind + " failed: " + mut.err.Error()
		return m, nil
	}
	if m.input.kind == inputNewIssueForm {
		m.input = inputState{}
		var cmd tea.Cmd
		m.list, cmd = m.list.applyMutation(mutationDoneMsg{
			origin: "list", kind: "create", resp: mut.resp,
		}, m.api, m.scope)
		// Form-create may carry labels (inline Labels field). The
		// daemon emits only issue.created (with labels folded into the
		// payload), NOT a separate issue.labeled event — so the
		// SSE-side maybeRefetchLabels will not fire. Without an
		// explicit refresh here, the per-project label aggregate would
		// stay stale until the next project switch / restart /
		// unrelated label SSE event. Use the same hook routeMutation
		// uses; the cache-existence gate inside batchLabelRefresh
		// keeps it a no-op for projects the user never opened the
		// menu for.
		if mutAffectsLabelCounts(mut) {
			return batchLabelRefresh(m, cmd, mut)
		}
		return m, cmd
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
// via dispatchPanelPromptCommit before the input clears.
//
// For centered forms, commitInput keeps the form open with
// saving=true while the mutation is in flight (so a duplicate
// ctrl+s is absorbed by the form's updateForm gate). The form is
// closed by routeFormMutation when the response arrives. Per-form
// gates: comments require non-empty content; body edits allow empty
// (clearing a body is legitimate); the new-issue form requires a
// non-blank Title and normalizes Labels/Owner.
func (m Model) commitInput() (Model, tea.Cmd) {
	kind := m.input.kind
	rawBuf := ""
	if f := m.input.activeField(); f != nil {
		rawBuf = f.value()
	}
	if kind.isCenteredForm() {
		return m.commitFormInput(kind)
	}
	trimmed := strings.TrimSpace(rawBuf)
	m.input = inputState{}
	if kind.isPanelPrompt() && trimmed != "" {
		var cmd tea.Cmd
		m.detail, cmd = m.detail.dispatchPanelPromptCommit(m.api, kind, trimmed)
		return m, cmd
	}
	return m, nil
}

// commitFormInput handles ctrl+s on a centered form. The form stays
// open with saving=true while the daemon round-trip runs; the
// arriving mutationDoneMsg closes it (success: clear and update
// detail / list; error: surface on the form's status line and leave
// open). Render-side sanitization elsewhere handles display safety;
// mutation payloads go to the wire untouched so the user's content
// is preserved exactly.
func (m Model) commitFormInput(kind inputKind) (Model, tea.Cmd) {
	if kind == inputNewIssueForm {
		return m.commitNewIssueForm()
	}
	rawBuf := ""
	if f := m.input.activeField(); f != nil {
		rawBuf = f.value()
	}
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
	case inputBodyEditForm:
		return m, dispatchFormEditBody(m.api, target, rawBuf, m.list.actor)
	}
	return m, nil
}

// commitNewIssueForm reads the four fields, normalizes Labels and
// Owner, gates on a non-blank Title, and dispatches CreateIssue.
// Title is sent untrimmed so deliberate leading/trailing whitespace
// survives (mirrors the legacy inline-row contract). Labels are
// comma-split with per-token TrimSpace; empty tokens drop. Owner is
// nil-on-wire when blank after trim, otherwise the trimmed value.
func (m Model) commitNewIssueForm() (Model, tea.Cmd) {
	if len(m.input.fields) < 4 {
		return m, nil
	}
	title := m.input.fields[0].input.Value()
	body := m.input.fields[1].area.Value()
	labelsBuf := m.input.fields[2].input.Value()
	ownerBuf := m.input.fields[3].input.Value()
	if strings.TrimSpace(title) == "" {
		m.input.err = "title is required"
		return m, nil
	}
	labels := normalizeLabels(labelsBuf)
	owner := normalizeOwner(ownerBuf)
	m.input.saving = true
	m.input.err = ""
	return m, dispatchFormCreateIssue(
		m.api, m.scope.projectID, title, body, labels, owner, m.list.actor,
	)
}

// normalizeLabels splits buf on commas, trims whitespace per token,
// and drops empty tokens. Returns nil for an all-empty buffer so the
// JSON omitempty tag elides Labels from the wire.
func normalizeLabels(buf string) []string {
	parts := strings.Split(buf, ",")
	out := make([]string, 0, len(parts))
	for _, p := range parts {
		p = strings.TrimSpace(p)
		if p != "" {
			out = append(out, p)
		}
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

// normalizeOwner trims buf and returns nil when blank, else &trimmed.
// nil omits Owner from the wire (the daemon treats omission as "no
// owner change") rather than sending an empty string that would set
// the owner to the literal "".
func normalizeOwner(buf string) *string {
	t := strings.TrimSpace(buf)
	if t == "" {
		return nil
	}
	return &t
}

// dispatchFormCreateIssue is the form-side CreateIssue dispatch.
// Tagged with origin="form" so routeFormMutation matches the response
// to the still-open new-issue form. Title is sent untrimmed; Labels
// and Owner have already been normalized by commitNewIssueForm.
func dispatchFormCreateIssue(
	api *Client, pid int64, title, body string,
	labels []string, owner *string, actor string,
) tea.Cmd {
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		resp, err := api.CreateIssue(ctx, pid, CreateIssueBody{
			Title: title, Body: body, Labels: labels, Owner: owner, Actor: actor,
		})
		return mutationDoneMsg{
			origin: "form", kind: "create", resp: resp, err: err,
		}
	}
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
// Panel-local prompts and centered forms have no live mirror, so
// cancel is just close.
func (m Model) cancelInput() (Model, tea.Cmd) {
	if m.input.kind.isCommandBar() {
		m.list.filter = m.input.preFilter
		m.list = m.list.clampCursorToFilter()
	}
	m.input = inputState{}
	return m, nil
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
//
// Label-cache invalidation runs alongside the list/detail refetch
// path: a label add/remove on the daemon side may have changed the
// project's label aggregate, so any cached entry for the event's
// project gets a fresh dispatch. This is a SECOND, additional cmd —
// it doesn't replace or short-circuit the list/detail refetch above.
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
	if next, cmd := m.maybeRefetchLabels(msg); cmd != nil {
		m = next
		cmds = append(cmds, cmd)
	}
	return m, tea.Batch(cmds...)
}

// maybeRefetchLabels invalidates the project-label cache when an SSE
// label event names a project we have a cached entry for. The
// suggestion menu reads the cache on render, so a fresh dispatch
// keeps the `+` autocomplete in sync with the daemon's authoritative
// counts. No-op when the event isn't a label event, the cache has no
// entry for the project, or projectLabels was never initialized
// (defensive — the field is set by initialModel).
func (m Model) maybeRefetchLabels(msg eventReceivedMsg) (Model, tea.Cmd) {
	if msg.eventType != "issue.labeled" && msg.eventType != "issue.unlabeled" {
		return m, nil
	}
	if m.projectLabels == nil {
		return m, nil
	}
	if _, ok := m.projectLabels.byProject[msg.projectID]; !ok {
		return m, nil
	}
	return m.dispatchLabelFetch(msg.projectID)
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
	// Plan-8: label-prompt autocomplete menu overlays the detail view
	// above the info line. The detail layout reserves the menu's
	// rendered height when computing the tab/body budget — see
	// detail_render.go::renderInfoLine.
	if m.view == viewDetail && isLabelPromptKind(m.input.kind) {
		body = m.overlaySuggestMenu(body)
	}
	return body
}

// overlaySuggestMenu places the suggestion menu above the info line
// (info row = height-2; menu's bottom row = info-line row - 1, so
// the menu sits with its last bordered row one cell above the info
// line). Right-anchored: the menu's right edge meets the inner
// right edge of the panel. Returns body unchanged when the menu has
// no rows to render (defensive — placeholderRows always returns at
// least one).
func (m Model) overlaySuggestMenu(body string) string {
	suggestions := filterSuggestions(
		m.suggestionsForPrompt(m.input),
		m.activeBuffer(),
	)
	entry := m.cacheEntryForPrompt(m.input)
	menu := renderSuggestMenu(m.input, suggestions, entry)
	if menu == "" {
		return body
	}
	menuW := lipgloss.Width(menu)
	menuH := lipgloss.Height(menu)
	// Info line is at height-2 (footer is height-1). Menu's bottom
	// border lives one row above the info line, so the top row is
	// (height - 2) - menuH.
	anchorRow := m.height - 2 - menuH
	anchorCol := m.width - menuW - 1
	return overlayAtCorner(body, menu, m.width, m.height, anchorRow, anchorCol)
}

// activeBuffer returns the current text in the active input field, or
// "" if no field is active. Used to project filterSuggestions's
// prefix so the suggestion menu narrows as the user types.
func (m Model) activeBuffer() string {
	if f := m.input.activeField(); f != nil {
		return f.value()
	}
	return ""
}

// cacheEntryForPrompt returns the labelCacheEntry the menu should
// reflect for s.kind == inputLabelPrompt. For inputRemoveLabelPrompt
// the entry's placeholder fields aren't consulted (suggestions come
// from dm.issue.Labels) so a zero entry is fine.
func (m Model) cacheEntryForPrompt(s inputState) labelCacheEntry {
	if s.kind != inputLabelPrompt || m.projectLabels == nil {
		return labelCacheEntry{}
	}
	return m.projectLabels.byProject[s.target.projectID]
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
