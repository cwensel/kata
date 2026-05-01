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
func (m Model) fetchInitial() tea.Cmd {
	api, sc, filter := m.api, m.scope, initialFilter(m.opts)
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
		return initialFetchMsg{issues: issues, err: err}
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
	switch msg := msg.(type) {
	case initialFetchMsg:
		m = m.populateCache(msg.issues, msg.err)
	case refetchedMsg:
		m = m.populateCache(msg.issues, msg.err)
	}
	return m.dispatchToView(msg)
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
		if m.canQuit() && m.keymap.Quit.matches(msg) {
			return m, tea.Quit, true
		}
	case openDetailMsg:
		next, cmd := m.handleOpenDetail(msg)
		return next, cmd, true
	case popDetailMsg:
		m.view = viewList
		return m, nil, true
	}
	return m, nil, false
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
// fetch. Errors leave the cache untouched so a transient failure does
// not erase the prior snapshot. The slot key is the current scope+filter
// so a follow-up filter change starts from a clean slate.
func (m Model) populateCache(issues []Issue, err error) Model {
	if err != nil || m.cache == nil {
		return m
	}
	m.cache.put(cacheKey{
		allProjects: m.scope.allProjects,
		projectID:   m.scope.projectID,
		filter:      m.list.filter,
	}, issues)
	return m
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

// maybeRefetchOpenDetail dispatches a single-issue GetIssue refetch
// when the event names the currently-open detail issue. The fetch is
// tagged with the current detail-open gen so applyFetched can drop it
// if the user navigates away before the response lands.
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
	return fetchIssue(m.api, m.detail.scopePID, m.detail.issue.Number, m.detail.gen)
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
	}
	return m, tea.Batch(cmds...)
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

// canQuit reports whether a 'q' keystroke should be honored as Quit.
// False while a list prompt is open (the buffer must absorb the rune)
// or while a detail modal is open (same reason — the user is typing a
// label/owner/link target, not asking to exit).
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
// the Enter-jump path (Task 8) has them without re-resolving scope.
//
// The detail-open generation increments on every open so an in-flight
// fetch from a previously-open issue is dropped by applyFetched when
// its tagged gen no longer matches dm.gen. The actor is seeded from
// the list model so detail-side mutations carry the resolved identity
// rather than the empty string.
func (m Model) handleOpenDetail(msg openDetailMsg) (tea.Model, tea.Cmd) {
	iss := msg.issue
	pid := detailProjectID(iss, m.scope)
	priorGen := m.detail.gen
	// Reset on open is the spec — no per-issue scroll memory.
	m.detail = newDetailModel()
	m.detail.gen = priorGen + 1
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

// View returns the rendered string for the active sub-view, with the
// SSE reconnect indicator and any active toast appended below. Both
// extras render as empty strings in the steady state so the view does
// not gain spurious blank lines.
func (m Model) View() string {
	body := m.viewBody()
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
	case viewList:
		return m.list.View(m.width, m.height)
	case viewDetail:
		return m.detail.View(m.width, m.height)
	}
	return ""
}
