package tui

import "testing"

// TestCache_PutThenStaleThenRefetch covers the steady-state SSE flow:
// fetch populates the slot, an event marks it stale, and a fresh fetch
// clears stale and replaces the data.
func TestCache_PutThenStaleThenRefetch(t *testing.T) {
	c := newIssueCache()
	k := cacheKey{projectID: 7}
	c.put(k, []Issue{{Number: 1}})
	if c.isStale() {
		t.Fatal("freshly put cache must not be stale")
	}
	c.markStale()
	if !c.isStale() {
		t.Fatal("after markStale, isStale must be true")
	}
	c.put(k, []Issue{{Number: 2}})
	if c.isStale() {
		t.Fatal("after re-put, stale must clear")
	}
	if len(c.data) != 1 || c.data[0].Number != 2 {
		t.Fatalf("data = %+v, want [{Number:2}]", c.data)
	}
}

// TestCache_DropEmpties confirms drop() leaves the slot empty so a
// follow-up isStale returns false (no slot, nothing to be stale about).
// This is the sync.reset_required path.
func TestCache_DropEmpties(t *testing.T) {
	c := newIssueCache()
	c.put(cacheKey{projectID: 7}, []Issue{{Number: 1}})
	c.markStale()
	c.drop()
	if c.isStale() {
		t.Fatal("after drop, isStale must be false")
	}
	if c.set {
		t.Fatal("after drop, set must be false")
	}
	if len(c.data) != 0 {
		t.Fatalf("after drop, data must be empty, got %+v", c.data)
	}
}

// TestCache_MarkStaleIdempotent: multiple events in a 150ms window all
// flip stale; the second markStale on an already-stale cache is a no-op.
func TestCache_MarkStaleIdempotent(t *testing.T) {
	c := newIssueCache()
	c.put(cacheKey{projectID: 7}, []Issue{{Number: 1}})
	c.markStale()
	c.markStale()
	c.markStale()
	if !c.isStale() {
		t.Fatal("triple markStale should leave isStale=true")
	}
}

func TestCache_RenderFilterDoesNotChangeSlotKey(t *testing.T) {
	m := Model{
		scope: scope{projectID: 7},
		list: listModel{filter: ListFilter{
			Status: "open", Owner: "alice", Search: "bug", Labels: []string{"prio-1"},
		}},
	}
	want := cacheKey{projectID: 7, limit: queueFetchLimit}
	if !cacheKeysEqual(m.currentCacheKey(), want) {
		t.Fatalf("currentCacheKey = %+v, want %+v", m.currentCacheKey(), want)
	}
}

// TestCache_EmptyIsNotStale: a freshly constructed cache is not stale —
// stale=true requires a real slot to be stale about.
func TestCache_EmptyIsNotStale(t *testing.T) {
	c := newIssueCache()
	if c.isStale() {
		t.Fatal("empty cache must not report stale")
	}
}
