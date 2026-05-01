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

// TestCache_FilterChange_ReplacesSlot: a filter change replaces the
// slot rather than evicting; the old data is no longer reachable but
// the cache is not empty.
func TestCache_FilterChange_ReplacesSlot(t *testing.T) {
	c := newIssueCache()
	c.put(cacheKey{projectID: 7, filter: ListFilter{Status: "open"}},
		[]Issue{{Number: 1}})
	c.put(cacheKey{projectID: 7, filter: ListFilter{Status: "closed"}},
		[]Issue{{Number: 99}})
	if c.key.filter.Status != "closed" {
		t.Fatalf("key.filter.Status = %q, want closed", c.key.filter.Status)
	}
	if len(c.data) != 1 || c.data[0].Number != 99 {
		t.Fatalf("data = %+v, want [{Number:99}]", c.data)
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
