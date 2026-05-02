package tui

import (
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

// TestLabelCache_DispatchStampsGenBeforeResponse pins the load-bearing
// invariant: dispatchLabelFetch must mark the cache entry fetching=true
// AND stamp a fresh gen BEFORE the HTTP request goes out. Without this
// ordering a slow response could land into a cache the user has since
// invalidated and the stale-rejection path would never fire.
func TestLabelCache_DispatchStampsGenBeforeResponse(t *testing.T) {
	m := buildModelWithLabelCache(t)
	pid := int64(7)
	m, _ = m.dispatchLabelFetch(pid)
	entry := m.projectLabels.byProject[pid]
	if !entry.fetching {
		t.Fatal("fetching must be true after dispatch (before response)")
	}
	if entry.gen <= 0 {
		t.Fatalf("gen must be stamped at dispatch (got %d)", entry.gen)
	}
	if entry.pid != pid {
		t.Fatalf("pid = %d, want %d", entry.pid, pid)
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
func TestLabelCache_MismatchedPidResponseDropped(t *testing.T) {
	m := buildModelWithLabelCache(t)
	m.scope = scope{projectID: 8} // active project is 8
	out, _ := m.Update(labelsFetchedMsg{
		pid: 7, gen: 1,
		labels: []LabelCount{{Label: "from7", Count: 1}},
	})
	entry := out.(Model).projectLabels.byProject[7]
	if len(entry.labels) != 0 {
		t.Fatalf("response for pid=7 must drop when target is pid=8; "+
			"got labels=%v", entry.labels)
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
