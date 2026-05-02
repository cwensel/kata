package tui

import (
	"context"
	"testing"
)

// buildModelWithLabelCache returns a minimal Model with the label-
// cache state initialized. api is nil because the dispatch + acceptance
// tests inspect cache state, not Cmd execution — fetchLabelsCmd's
// nil-api branch returns a synthetic message that doesn't reach the
// network.
func buildModelWithLabelCache(_ *testing.T) Model {
	return Model{
		view:          viewList,
		projectLabels: newLabelCache(),
	}
}

// fakeLabelLister is a labelLister stub that returns a canned slice
// without touching the network. Used by the race-fix coverage so the
// test exercises the rejection path with a real (non-empty) response,
// not the nil-api synthetic-empty fallback.
type fakeLabelLister struct {
	labels []LabelCount
	err    error
	calls  int
}

func (f *fakeLabelLister) ListLabels(_ context.Context, _ int64) ([]LabelCount, error) {
	f.calls++
	return f.labels, f.err
}

// TestLabelCache_DispatchStampsGenBeforeResponse pins the load-bearing
// invariant: dispatchLabelFetch must stamp a fresh gen on the cache
// entry BEFORE the HTTP request goes out. The race the fix prevents:
// dispatch1 (gen=1) → dispatch2 (gen=2) → cmd1 runs and returns
// labelsFetchedMsg{gen=1} → handler must drop it because the entry's
// gen has been bumped to 2.
//
// This test exercises that race end-to-end: it captures the FIRST
// cmd, fires a SECOND dispatch (which bumps cache.gen to 2), runs the
// first cmd against a fake api that returns real labels, and asserts
// the resulting gen=1 message is rejected. A regression that moved
// the gen-stamp into the cmd (or dropped the gen check in the
// handler) would let the stale labels overwrite the cache.
func TestLabelCache_DispatchStampsGenBeforeResponse(t *testing.T) {
	m := buildModelWithLabelCache(t)
	pid := int64(7)
	m.scope = scope{projectID: pid}
	fake := &fakeLabelLister{labels: []LabelCount{{Label: "from-first", Count: 1}}}
	// First dispatch — gen stamped to 1, fetching=true. Capture the
	// cmd; do NOT run it yet so the second dispatch can bump gen.
	m, _ = m.dispatchLabelFetch(pid)
	if got := m.projectLabels.byProject[pid].gen; got != 1 {
		t.Fatalf("first dispatch gen = %d, want 1", got)
	}
	// Build the cmd the way dispatchLabelFetch would have, but with
	// the fake lister so it returns real labels rather than nil-api
	// synthetic empties — this is what makes the rejection meaningful.
	firstCmd := fetchLabelsCmd(fake, pid, 1)
	// Second dispatch — bumps cache.gen to 2 BEFORE firstCmd runs.
	m, _ = m.dispatchLabelFetch(pid)
	if got := m.projectLabels.byProject[pid].gen; got != 2 {
		t.Fatalf("second dispatch gen = %d, want 2 (must bump even "+
			"though first cmd hasn't run)", got)
	}
	// Now run the first cmd — it produces labelsFetchedMsg{gen: 1, ...}.
	msg := firstCmd()
	out, _ := m.Update(msg)
	nm := out.(Model)
	entry := nm.projectLabels.byProject[pid]
	if len(entry.labels) != 0 {
		t.Fatalf("stale gen=1 response must be rejected because "+
			"cache.gen=2; got labels=%v", entry.labels)
	}
	if fake.calls != 1 {
		t.Fatalf("fake.ListLabels calls = %d, want 1", fake.calls)
	}
}

// TestLabelCache_StaleGenResponseDropped: a response whose gen lags
// behind the cache's current gen must NOT populate the cache. The
// acceptance check on response is gen >= cache.gen; older messages
// are silently discarded so a slow first-dispatch can't overwrite a
// freshly-invalidated cache entry.
func TestLabelCache_StaleGenResponseDropped(t *testing.T) {
	m := buildModelWithLabelCache(t)
	pid := int64(7)
	m.scope = scope{projectID: pid}
	m, _ = m.dispatchLabelFetch(pid) // gen=1
	m, _ = m.dispatchLabelFetch(pid) // gen=2 (newer)
	out, _ := m.Update(labelsFetchedMsg{
		pid: pid, gen: 1, labels: []LabelCount{{Label: "old", Count: 1}},
	})
	entry := out.(Model).projectLabels.byProject[pid]
	if len(entry.labels) != 0 {
		t.Fatalf("stale gen=1 response must NOT populate cache "+
			"(cache.gen=2); got labels=%v", entry.labels)
	}
}

// TestLabelCache_MismatchedPidResponseDropped: a response carrying a
// pid that doesn't match the current target's pid must be dropped.
// A user can switch projects between dispatch and response — the
// no-longer-active project's cache entry must NOT be silently
// repopulated by a slow ListLabels call.
//
// We dispatch for pid=7 first so the cache entry EXISTS (defeats
// handleLabelsFetched's `!exists` short-circuit), then switch scope
// to pid=8, then send the stale pid=7 response. The response must
// be dropped specifically because msg.pid != targetPID(), not just
// because no entry was there to populate.
func TestLabelCache_MismatchedPidResponseDropped(t *testing.T) {
	m := buildModelWithLabelCache(t)
	m.scope = scope{projectID: 7} // active project is 7
	// Dispatch creates the entry for pid=7 with gen=1, fetching=true.
	m, _ = m.dispatchLabelFetch(7)
	// User switches project to pid=8 BEFORE the response lands.
	m.scope = scope{projectID: 8}
	// Stale response for pid=7 arrives from the now-superseded fetch.
	out, _ := m.Update(labelsFetchedMsg{
		pid: 7, gen: 1,
		labels: []LabelCount{{Label: "from7", Count: 1}},
	})
	entry := out.(Model).projectLabels.byProject[7]
	if len(entry.labels) != 0 {
		t.Fatalf("response for pid=7 must drop when target is pid=8 "+
			"(pid-mismatch branch), even with a live entry; "+
			"got labels=%v", entry.labels)
	}
}

// TestBatchLabelRefresh_GatesOnCacheExistence pins I1: a successful
// label.add mutation against a project the user has NEVER opened the
// `+` menu for (no cache entry yet) must NOT trigger a wasted
// ListLabels fetch. Mirrors maybeRefetchLabels's SSE gate so the two
// invalidation paths behave identically.
func TestBatchLabelRefresh_GatesOnCacheExistence(t *testing.T) {
	m := buildModelWithLabelCache(t)
	m.scope = scope{projectID: 7}
	// No cache entry for pid=7 — the user never opened the menu.
	mut := mutationDoneMsg{
		kind: "label.add",
		resp: &MutationResp{Issue: &Issue{ProjectID: 7, Number: 42}},
	}
	out, cmd := batchLabelRefresh(m, nil, mut)
	nm := out.(Model)
	if cmd != nil {
		t.Fatal("batchLabelRefresh must not dispatch when the project " +
			"has no cache entry — wastes a ListLabels HTTP roundtrip")
	}
	if _, exists := nm.projectLabels.byProject[7]; exists {
		t.Fatal("batchLabelRefresh must not create a cache entry " +
			"for an unopened project")
	}
}

// TestBatchLabelRefresh_DispatchesWhenEntryExists: the complement of
// the gate test — when a cache entry IS present (the user has hit `+`
// against the project at least once), a successful label mutation
// MUST dispatch a refresh so the menu's count column stays accurate.
func TestBatchLabelRefresh_DispatchesWhenEntryExists(t *testing.T) {
	m := buildModelWithLabelCache(t)
	m.scope = scope{projectID: 7}
	// Prime an entry as if the user had previously opened the menu.
	m.projectLabels.byProject[7] = labelCacheEntry{
		pid: 7, gen: 1,
		labels: []LabelCount{{Label: "bug", Count: 2}},
	}
	mut := mutationDoneMsg{
		kind: "label.add",
		resp: &MutationResp{Issue: &Issue{ProjectID: 7, Number: 42}},
	}
	out, cmd := batchLabelRefresh(m, nil, mut)
	nm := out.(Model)
	if cmd == nil {
		t.Fatal("batchLabelRefresh must dispatch when the project has " +
			"an existing cache entry")
	}
	if !nm.projectLabels.byProject[7].fetching {
		t.Fatal("dispatched entry must be fetching=true")
	}
}

// TestMutAffectsLabelCounts_CreateDeferredToCommit4 pins I1: until
// commit 4 wires the multi-field create form's Labels payload, plain
// "create" mutations carry no label changes and must NOT trip the
// label-cache invalidation. Without this gate every successful issue
// create burns a ListLabels HTTP roundtrip against a project that
// may not even have an open suggestion menu.
func TestMutAffectsLabelCounts_CreateDeferredToCommit4(t *testing.T) {
	if mutAffectsLabelCounts(mutationDoneMsg{kind: "create"}) {
		t.Fatal("'create' must NOT trigger a label-aggregate refetch " +
			"in commit 3 — labels payload is wired in commit 4")
	}
	if !mutAffectsLabelCounts(mutationDoneMsg{kind: "label.add"}) {
		t.Fatal("'label.add' must trigger a label-aggregate refetch")
	}
	if !mutAffectsLabelCounts(mutationDoneMsg{kind: "label.remove"}) {
		t.Fatal("'label.remove' must trigger a label-aggregate refetch")
	}
}

// TestLabelCache_SSEEventInvalidatesSuggestionCacheOnly: an
// issue.labeled SSE event for a project that has a cache entry must
// trigger a refetch (entry.fetching=true, entry.gen advanced). The
// list/detail refetch path is independent — this test asserts the
// suggestion-cache invalidation specifically.
func TestLabelCache_SSEEventInvalidatesSuggestionCacheOnly(t *testing.T) {
	m := buildModelWithLabelCache(t)
	pid := int64(7)
	m.scope = scope{projectID: pid}
	m.cache = newIssueCache()
	m.sseCh = nil // no SSE bridge to re-arm
	m.nextLabelsGen = 5
	m.projectLabels.byProject[pid] = labelCacheEntry{
		labels: []LabelCount{{Label: "stale", Count: 1}},
		gen:    5, pid: pid,
	}
	out, _ := m.Update(eventReceivedMsg{
		eventType: "issue.labeled", projectID: pid, issueNumber: 42,
	})
	nm := out.(Model)
	entry := nm.projectLabels.byProject[pid]
	if !entry.fetching {
		t.Fatal("SSE event must trigger refetch (fetching=true)")
	}
	if entry.gen <= 5 {
		t.Fatalf("gen must advance past 5; got %d", entry.gen)
	}
}
