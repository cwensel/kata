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

	hwm, err := env.DB.MaxEventID(context.Background())
	require.NoError(t, err)
	resp := openSSE(t, env, "after_id="+strconv.FormatInt(hwm, 10), nil)
	defer func() { _ = resp.Body.Close() }()
	require.Equal(t, 200, resp.StatusCode)

	// Drain the : connected\n\n preamble.
	preamble := make([]byte, 16)
	_, err = resp.Body.Read(preamble)
	require.NoError(t, err)

	// Insert two events directly via DB so the handler-side broadcast does NOT
	// fire (testenv.DB.CreateIssue bypasses HTTP). We'll Broadcast manually in
	// inverted order below.
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

	frames := readSSEFramesUntilN(t, resp.Body, 2, 2*time.Second)
	require.Len(t, frames, 2)
	assert.Equal(t, strconv.FormatInt(evt1.ID, 10), frames[0].id,
		"first frame must be the lower id, regardless of broadcast order")
	assert.Equal(t, strconv.FormatInt(evt2.ID, 10), frames[1].id)
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
