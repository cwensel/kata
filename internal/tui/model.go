package tui

import (
	"context"
	"os"
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
	if m.view == viewEmpty || m.api == nil {
		return m.waitForSSE()
	}
	return tea.Batch(m.fetchInitial(), m.waitForSSE())
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
// originated the mutation, regardless of which view is now active. If
// the originating view is also the active view, the result is just one
// dispatch (the active sub-view consumes it). If the user view-switched
// after dispatching the mutation (e.g. closed an issue from the list,
// then opened a different issue's detail before the close completed),
// we still apply the result to the originating model so its next
// render is correct — list and detail each keep their own cache.
//
// Without this top-level routing, listModel.applyMutation drops
// origin != "list" and detailModel.applyMutation drops origin !=
// "detail", so a view-switched mutation completion would land nowhere
// and the originating cache would stay stale until SSE invalidation
// caught up.
func (m Model) routeMutation(mut mutationDoneMsg) (tea.Model, tea.Cmd) {
	if mut.origin == "list" && m.view != viewList {
		var cmd tea.Cmd
		m.list, cmd = m.list.applyMutation(mut, m.api, m.scope)
		return m, cmd
	}
	if mut.origin == "detail" && m.view != viewDetail {
		var cmd tea.Cmd
		m.detail, cmd = m.detail.applyMutation(mut, m.api)
		return m, cmd
	}
	return m.dispatchToView(mut)
}

// routeTopLevel handles non-SSE messages that the parent Model owns:
// resize, global quit, view-switch, detail-open/pop. ok=true means the
// message was handled here.
func (m Model) routeTopLevel(msg tea.Msg) (tea.Model, tea.Cmd, bool) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.width, m.height = msg.Width, msg.Height
		return m, nil, true
	case tea.KeyMsg:
		if next, cmd, ok := m.routeGlobalKey(msg); ok {
			return next, cmd, true
		}
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

// routeGlobalKey handles the global key family (quit, help, scope toggle)
// gated by the prompt/modal input check so a buffer doesn't see them.
// viewEmpty honors only quit; ?, R, and any other binding fall through
// silently because the only meaningful action is `q` to exit.
func (m Model) routeGlobalKey(msg tea.KeyMsg) (Model, tea.Cmd, bool) {
	if !m.canQuit() {
		return m, nil, false
	}
	if m.keymap.Quit.matches(msg) {
		return m, tea.Quit, true
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
// honored. False while a list prompt is open (the buffer must absorb
// the rune) or while a detail modal is open (same reason — the user is
// typing a label/owner/link target, not asking to exit).
func (m Model) canQuit() bool {
	if m.list.search.inputting {
		return false
	}
	if m.view == viewDetail && m.detail.modal.active() {
		return false
	}
	return true
}

// handleOpenDetail seeds m.detail with the chosen issue and dispatches
// the three concurrent tab fetches via tea.Batch. The fetches run in
// parallel so the user sees data on whichever tab is active first. The
// detail model also remembers the project_id and all-projects flag so
// the Enter-jump path has them without re-resolving scope.
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
	if m.view == viewList || m.view == viewDetail {
		// viewChrome already accounted for SSE + toast inline.
		return body
	}
	extras := []string{}
	if s := renderSSEStatus(m.sseStatus); s != "" {
		extras = append(extras, s)
	}
	if s := renderToast(m.toast); s != "" {
		extras = append(extras, s)
	}
	if len(extras) == 0 {
		return body
	}
	return joinNonEmpty(append([]string{body}, extras...))
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
// pending invalidation flag, the active toast (if any), and the
// build-time version string. Centralising this keeps the sub-views
// free of Model coupling.
func (m Model) chrome() viewChrome {
	return viewChrome{
		scope:     m.scope,
		sseStatus: m.sseStatus,
		pending:   m.pendingRefetch,
		toast:     m.toast,
		version:   kataVersion,
	}
}

// kataVersion is the build-time version string used by the title bar.
// Hardcoded today; a future plan can wire `-ldflags
// -X`-style injection through an internal/version package.
const kataVersion = "v0.1.0"
