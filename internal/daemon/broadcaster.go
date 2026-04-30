package daemon

import (
	"sync"

	"github.com/wesm/kata/internal/db"
)

// channelBuffer is the per-subscriber send buffer. Full channels trigger
// overflow disconnect (Broadcast closes the channel and removes the
// subscriber). Plan 7 may expose this as a `kata config` knob; for now it's
// a const matching spec §11.
const channelBuffer = 256

// StreamMsg is the envelope on each subscriber's channel. Kind discriminates
// between an event wakeup and a reset signal so callers can never confuse the
// two.
type StreamMsg struct {
	Kind      string    // "event" | "reset"
	Event     *db.Event // non-nil iff Kind == "event"
	ResetID   int64     // non-zero iff Kind == "reset"
	ProjectID int64     // 0 = cross-project; used for filter matching
}

// SubFilter restricts which broadcasts a subscriber receives. ProjectID 0
// (zero value) means cross-project — every event flows through.
type SubFilter struct {
	ProjectID int64
}

func (f SubFilter) matches(msg StreamMsg) bool {
	if f.ProjectID == 0 {
		return true
	}
	return msg.ProjectID == f.ProjectID
}

// Subscription is the handle returned by Subscribe. Caller must call Unsub()
// when done. Ch is closed by the broadcaster on overflow disconnect or by
// Unsub on caller exit. Unsub is safe to call multiple times.
type Subscription struct {
	Ch    <-chan StreamMsg
	Unsub func()
}

// EventBroadcaster fans out wakeups and reset signals to subscribers. It
// holds no DB reference; the SSE handler captures its own high-water mark
// (via db.MaxEventID) after Subscribe.
type EventBroadcaster struct {
	mu     sync.Mutex
	nextID int
	subs   map[int]*subscriber
}

type subscriber struct {
	ch     chan StreamMsg
	filter SubFilter
}

// NewEventBroadcaster constructs an empty broadcaster. The daemon owns one
// instance; its lifetime matches the server process.
func NewEventBroadcaster() *EventBroadcaster {
	return &EventBroadcaster{subs: map[int]*subscriber{}}
}

// Subscribe registers a new subscriber with the given filter. Returned
// Subscription holds a read-only Ch and an Unsub closure that's safe to call
// repeatedly.
//
// Callers must invoke Unsub (typically via defer) to release resources; the
// only automatic cleanup is overflow eviction when the channel fills.
func (b *EventBroadcaster) Subscribe(filter SubFilter) Subscription {
	b.mu.Lock()
	defer b.mu.Unlock()
	id := b.nextID
	b.nextID++
	ch := make(chan StreamMsg, channelBuffer)
	b.subs[id] = &subscriber{ch: ch, filter: filter}
	return Subscription{
		Ch: ch,
		Unsub: func() {
			b.mu.Lock()
			defer b.mu.Unlock()
			if sub, ok := b.subs[id]; ok {
				delete(b.subs, id)
				close(sub.ch)
			}
		},
	}
}

// Broadcast fans msg out to every matching subscriber. Sends are non-blocking;
// when a subscriber's buffer is full the broadcaster closes its channel and
// removes it (overflow disconnect). The SSE handler reading on the closed
// channel returns; the client reconnects with Last-Event-ID and resumes via
// the durable replay path.
//
// Single full Lock keeps the implementation small; single-user daemon
// throughput doesn't justify an RLock+Lock dance.
func (b *EventBroadcaster) Broadcast(msg StreamMsg) {
	b.mu.Lock()
	defer b.mu.Unlock()
	for id, sub := range b.subs {
		if !sub.filter.matches(msg) {
			continue
		}
		select {
		case sub.ch <- msg:
		default:
			close(sub.ch)
			delete(b.subs, id)
		}
	}
}
