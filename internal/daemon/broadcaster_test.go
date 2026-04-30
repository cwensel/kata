package daemon_test

import (
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/wesm/kata/internal/daemon"
	"github.com/wesm/kata/internal/db"
)

func TestBroadcaster_SubscribeAndUnsubLifecycle(t *testing.T) {
	b := daemon.NewEventBroadcaster()
	sub := b.Subscribe(daemon.SubFilter{})
	sub.Unsub()
	// Calling Unsub twice must be safe — closes only once.
	sub.Unsub()
	// Channel must be closed.
	select {
	case _, ok := <-sub.Ch:
		assert.False(t, ok, "channel must be closed after Unsub")
	case <-time.After(time.Second):
		t.Fatal("channel was not closed within 1s")
	}
}

func TestBroadcaster_BroadcastFansToMatchingFiltersOnly(t *testing.T) {
	b := daemon.NewEventBroadcaster()
	all := b.Subscribe(daemon.SubFilter{})
	a := b.Subscribe(daemon.SubFilter{ProjectID: 1})
	other := b.Subscribe(daemon.SubFilter{ProjectID: 2})
	defer all.Unsub()
	defer a.Unsub()
	defer other.Unsub()

	evt := &db.Event{ID: 100, ProjectID: 1, Type: "issue.created"}
	b.Broadcast(daemon.StreamMsg{Kind: "event", Event: evt, ProjectID: 1})

	select {
	case got := <-all.Ch:
		assert.Equal(t, "event", got.Kind)
		assert.Equal(t, int64(100), got.Event.ID)
	case <-time.After(time.Second):
		t.Fatal("cross-project subscriber should have received the event")
	}
	select {
	case got := <-a.Ch:
		assert.Equal(t, int64(100), got.Event.ID)
	case <-time.After(time.Second):
		t.Fatal("project-1 subscriber should have received the event")
	}
	select {
	case got := <-other.Ch:
		t.Fatalf("project-2 subscriber must not receive a project-1 event, got %+v", got)
	case <-time.After(50 * time.Millisecond):
		// expected: no delivery
	}
}

func TestBroadcaster_ResetFansToAllMatchingFilters(t *testing.T) {
	b := daemon.NewEventBroadcaster()
	all := b.Subscribe(daemon.SubFilter{})
	a := b.Subscribe(daemon.SubFilter{ProjectID: 1})
	defer all.Unsub()
	defer a.Unsub()

	b.Broadcast(daemon.StreamMsg{Kind: "reset", ResetID: 999, ProjectID: 1})

	for i, ch := range []<-chan daemon.StreamMsg{all.Ch, a.Ch} {
		select {
		case got := <-ch:
			assert.Equal(t, "reset", got.Kind)
			assert.Equal(t, int64(999), got.ResetID)
		case <-time.After(time.Second):
			t.Fatalf("subscriber %d did not receive reset", i)
		}
	}
}

func TestBroadcaster_OverflowDisconnectsSlowSubscriberOnly(t *testing.T) {
	b := daemon.NewEventBroadcaster()
	slow := b.Subscribe(daemon.SubFilter{})
	fast := b.Subscribe(daemon.SubFilter{})
	defer fast.Unsub()
	// Don't Unsub slow — broadcast saturates its buffer (256) and we expect
	// the broadcaster to close it.

	for i := int64(0); i < 300; i++ {
		evt := &db.Event{ID: i + 1, ProjectID: 1, Type: "issue.created"}
		b.Broadcast(daemon.StreamMsg{Kind: "event", Event: evt, ProjectID: 1})
	}

	// slow.Ch must close (overflow disconnect).
	closed := false
	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		select {
		case _, ok := <-slow.Ch:
			if !ok {
				closed = true
				break
			}
		case <-time.After(20 * time.Millisecond):
		}
		if closed {
			break
		}
	}
	assert.True(t, closed, "slow subscriber's channel must close on overflow")

	// fast must still be live: drain it and assert at least one delivery.
	got := 0
loop:
	for {
		select {
		case _, ok := <-fast.Ch:
			if !ok {
				break loop
			}
			got++
		case <-time.After(20 * time.Millisecond):
			break loop
		}
	}
	assert.Greater(t, got, 0, "fast subscriber should still be receiving")
}

func TestBroadcaster_RaceFuzz(t *testing.T) {
	// -race coverage for concurrent Subscribe/Broadcast/Unsub.
	b := daemon.NewEventBroadcaster()
	var wg sync.WaitGroup
	const N = 200
	for i := 0; i < N; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			sub := b.Subscribe(daemon.SubFilter{ProjectID: int64(i % 5)})
			// drain whatever arrives without blocking
			go func() {
				for range sub.Ch {
				}
			}()
			time.Sleep(time.Microsecond)
			sub.Unsub()
		}(i)
	}
	for i := 0; i < N; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			evt := &db.Event{ID: int64(i + 1), ProjectID: int64(i % 5), Type: "issue.created"}
			b.Broadcast(daemon.StreamMsg{Kind: "event", Event: evt, ProjectID: evt.ProjectID})
		}(i)
	}
	wg.Wait()
}
