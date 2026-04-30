package daemon_test

import (
	"context"
	"path/filepath"
	"strconv"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/wesm/kata/internal/daemon"
	"github.com/wesm/kata/internal/db"
	"github.com/wesm/kata/internal/testenv"
)

// TestSSE_OutOfOrderBroadcastsEmitInIDOrder pins the wakeup-and-requery
// guarantee: even if Broadcast(102) fires before Broadcast(101), the SSE
// consumer sees frame id=101 first, then id=102. The DB is the ordering
// authority; the broadcaster only signals "something changed at or below N".
func TestSSE_OutOfOrderBroadcastsEmitInIDOrder(t *testing.T) {
	env := testenv.New(t)
	project, err := env.DB.CreateProject(context.Background(), "github.com/test/a", "a")
	require.NoError(t, err)

	// Sentinel: insert + broadcast a setup event before the test events. We
	// wait for its frame to confirm the SSE handler has finished its drain
	// phase and entered the live wakeup loop. Without this anchor, the test
	// events could land in the drain (which delivers in id order trivially)
	// instead of exercising the wakeup-and-requery path under test.
	_, sentinel, err := env.DB.CreateIssue(context.Background(), db.CreateIssueParams{
		ProjectID: project.ID, Title: "sentinel", Author: "tester",
	})
	require.NoError(t, err)

	hwm, err := env.DB.MaxEventID(context.Background())
	require.NoError(t, err)
	resp := openSSE(t, env, "after_id="+strconv.FormatInt(hwm-1, 10), nil)
	defer func() { _ = resp.Body.Close() }()
	require.Equal(t, 200, resp.StatusCode)

	framer := newSSEFramer(resp.Body)
	sentinelFrame, ok := framer.Next(t, 2*time.Second)
	require.True(t, ok, "sentinel drain frame should arrive")
	require.Equal(t, strconv.FormatInt(sentinel.ID, 10), sentinelFrame.id)

	// Now in live phase. Insert two events directly via DB so the handler-side
	// broadcast does NOT fire; broadcast manually in inverted order to pin the
	// wakeup-and-requery ordering claim.
	_, evt1, err := env.DB.CreateIssue(context.Background(), db.CreateIssueParams{
		ProjectID: project.ID, Title: "first", Author: "tester",
	})
	require.NoError(t, err)
	_, evt2, err := env.DB.CreateIssue(context.Background(), db.CreateIssueParams{
		ProjectID: project.ID, Title: "second", Author: "tester",
	})
	require.NoError(t, err)

	// Inverted: evt2 first, then evt1.
	env.Broadcaster.Broadcast(daemon.StreamMsg{
		Kind: "event", Event: &evt2, ProjectID: project.ID,
	})
	env.Broadcaster.Broadcast(daemon.StreamMsg{
		Kind: "event", Event: &evt1, ProjectID: project.ID,
	})

	first, ok := framer.Next(t, 2*time.Second)
	require.True(t, ok)
	second, ok := framer.Next(t, 2*time.Second)
	require.True(t, ok)
	assert.Equal(t, strconv.FormatInt(evt1.ID, 10), first.id,
		"first frame must be the lower id, regardless of broadcast order")
	assert.Equal(t, strconv.FormatInt(evt2.ID, 10), second.id)
}

// TestSSE_LivePhaseChecksPurgeResetBeforeReplay pins the cross-cutting fix
// that surfaces sync.reset_required even when the corresponding "reset"
// broadcast lost the race to a post-purge "event" broadcast. The handler
// re-checks PurgeResetCheck on every event wakeup so the client cannot
// receive a post-purge frame and silently advance past the reset cursor.
func TestSSE_LivePhaseChecksPurgeResetBeforeReplay(t *testing.T) {
	env := testenv.New(t)
	project, err := env.DB.CreateProject(context.Background(), "github.com/test/a", "a")
	require.NoError(t, err)

	// Create a sentinel event so the SSE handler enters live phase.
	sentinelIssue, sentinelEvt, err := env.DB.CreateIssue(context.Background(), db.CreateIssueParams{
		ProjectID: project.ID, Title: "sentinel", Author: "tester",
	})
	require.NoError(t, err)

	hwm, err := env.DB.MaxEventID(context.Background())
	require.NoError(t, err)
	resp := openSSE(t, env, "after_id="+strconv.FormatInt(hwm-1, 10), nil)
	defer func() { _ = resp.Body.Close() }()
	require.Equal(t, 200, resp.StatusCode)

	framer := newSSEFramer(resp.Body)
	first, ok := framer.Next(t, 2*time.Second)
	require.True(t, ok)
	require.Equal(t, strconv.FormatInt(sentinelEvt.ID, 10), first.id)

	// Now create a second issue and purge the sentinel directly via DB so
	// the handler-side broadcasts do NOT fire. Then broadcast ONLY an
	// "event" message (simulating the broadcaster reordering: reset lost
	// the race). The handler must still emit a reset because
	// PurgeResetCheck sees the purge committed.
	_, evt2, err := env.DB.CreateIssue(context.Background(), db.CreateIssueParams{
		ProjectID: project.ID, Title: "post-purge", Author: "tester",
	})
	require.NoError(t, err)
	_, err = env.DB.PurgeIssue(context.Background(), sentinelIssue.ID, "tester", nil)
	require.NoError(t, err)

	env.Broadcaster.Broadcast(daemon.StreamMsg{
		Kind: "event", Event: &evt2, ProjectID: project.ID,
	})

	frame, ok := framer.Next(t, 2*time.Second)
	require.True(t, ok)
	assert.Equal(t, "sync.reset_required", frame.event,
		"live phase must surface the reset before replaying post-purge events")
}

// TestBroadcaster_ConcurrentSubscribeBroadcastUnsub is a -race fuzz of
// concurrent Subscribe/Broadcast/Unsub. It asserts the broadcaster doesn't
// deadlock, panic, or leak goroutines.
func TestBroadcaster_ConcurrentSubscribeBroadcastUnsub(t *testing.T) {
	d, err := db.Open(context.Background(), filepath.Join(t.TempDir(), "kata.db"))
	require.NoError(t, err)
	defer func() { _ = d.Close() }()

	b := daemon.NewEventBroadcaster()
	var wg sync.WaitGroup
	const N = 100
	for i := 0; i < N; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			sub := b.Subscribe(daemon.SubFilter{ProjectID: int64(i % 3)})
			drain := make(chan struct{})
			go func() {
				for range sub.Ch { //nolint:revive // empty body: drain only, values discarded
				}
				close(drain)
			}()
			time.Sleep(time.Microsecond * time.Duration(i%5))
			sub.Unsub()
			<-drain
		}(i)
	}
	for i := 0; i < N; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			evt := &db.Event{ID: int64(i + 1), ProjectID: int64(i % 3), Type: "issue.created"}
			b.Broadcast(daemon.StreamMsg{
				Kind: "event", Event: evt, ProjectID: evt.ProjectID,
			})
		}(i)
	}
	wg.Wait()
}
