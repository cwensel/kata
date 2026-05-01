package tui

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"
)

// TestSSEParser_KeepalivesAreSkipped: a leading ": keepalive\n\n" must
// not produce a frame; the issue.created frame after it must.
func TestSSEParser_KeepalivesAreSkipped(t *testing.T) {
	in := ": keepalive\n\n" +
		"id: 1\nevent: issue.created\ndata: {\"event_id\":1,\"type\":\"issue.created\"}\n\n"
	frames, err := parseAllFrames(strings.NewReader(in))
	if err != nil && !errors.Is(err, io.EOF) {
		t.Fatal(err)
	}
	if len(frames) != 1 {
		t.Fatalf("got %d frames, want 1", len(frames))
	}
	if frames[0].kind != frameEvent || frames[0].eventType != "issue.created" {
		t.Fatalf("unexpected: %+v", frames[0])
	}
	if frames[0].id != 1 {
		t.Fatalf("id = %d, want 1", frames[0].id)
	}
}

// TestSSEParser_MultipleFrames: two consecutive event blocks both arrive.
func TestSSEParser_MultipleFrames(t *testing.T) {
	in := "id: 1\nevent: issue.created\ndata: {\"event_id\":1}\n\n" +
		"id: 2\nevent: issue.commented\ndata: {\"event_id\":2}\n\n"
	frames, err := parseAllFrames(strings.NewReader(in))
	if err != nil {
		t.Fatal(err)
	}
	if len(frames) != 2 {
		t.Fatalf("got %d frames, want 2", len(frames))
	}
	if frames[0].id != 1 || frames[1].id != 2 {
		t.Fatalf("ids = %d,%d, want 1,2", frames[0].id, frames[1].id)
	}
}

// TestSSEParser_ResetRequired: a sync.reset_required frame is classified
// as frameReset. The id: line carries the resume cursor (== reset_after_id
// per api.EventReset's contract); the JSON payload's reset_after_id is
// intentionally not lifted onto the frame.
func TestSSEParser_ResetRequired(t *testing.T) {
	in := "id: 42\nevent: sync.reset_required\n" +
		"data: {\"event_id\":42,\"reset_after_id\":42}\n\n"
	frames, err := parseAllFrames(strings.NewReader(in))
	if err != nil {
		t.Fatal(err)
	}
	if len(frames) != 1 {
		t.Fatalf("got %d frames, want 1", len(frames))
	}
	if frames[0].kind != frameReset {
		t.Fatalf("kind = %d, want frameReset", frames[0].kind)
	}
	if frames[0].id != 42 {
		t.Fatalf("id = %d, want 42 (the resume cursor)", frames[0].id)
	}
}

// TestSSEParser_MalformedFrameSkipped: a frame with no data: line is
// dropped, the next well-formed frame still arrives. Regression for
// "single bad frame wedges the consumer."
func TestSSEParser_MalformedFrameSkipped(t *testing.T) {
	in := "id: 1\nevent: issue.created\n\n" + // no data line
		"id: 2\nevent: issue.commented\ndata: {\"event_id\":2}\n\n"
	frames, err := parseAllFrames(strings.NewReader(in))
	if err != nil {
		t.Fatal(err)
	}
	if len(frames) != 1 {
		t.Fatalf("got %d frames, want 1 (malformed dropped)", len(frames))
	}
	if frames[0].id != 2 {
		t.Fatalf("id = %d, want 2 (the well-formed one)", frames[0].id)
	}
}

// TestSSEParser_EOFNoTrailingFrame: an in-progress frame at EOF is
// dropped (no blank-line terminator means no commit).
func TestSSEParser_EOFNoTrailingFrame(t *testing.T) {
	in := "id: 1\nevent: issue.created\ndata: {\"event_id\":1}\n"
	frames, err := parseAllFrames(strings.NewReader(in))
	if err != nil {
		t.Fatal(err)
	}
	if len(frames) != 0 {
		t.Fatalf("got %d frames, want 0 (no terminator)", len(frames))
	}
}

// TestSSEParser_DecodeEventReceived: a well-formed frame's payload is
// decoded into eventReceivedMsg with type+projectID+issueNumber.
func TestSSEParser_DecodeEventReceived(t *testing.T) {
	body := []byte(`{"type":"issue.created","project_id":7,"issue_number":42}`)
	got := decodeEventReceived(frame{kind: frameEvent, data: body})
	if got.eventType != "issue.created" {
		t.Fatalf("eventType = %q, want issue.created", got.eventType)
	}
	if got.projectID != 7 {
		t.Fatalf("projectID = %d, want 7", got.projectID)
	}
	if got.issueNumber != 42 {
		t.Fatalf("issueNumber = %d, want 42", got.issueNumber)
	}
}

// TestSSEParser_DecodeEventReceived_NilIssueNumber: an envelope without
// issue_number falls through as 0 (no panic on a nil pointer).
func TestSSEParser_DecodeEventReceived_NilIssueNumber(t *testing.T) {
	body := []byte(`{"type":"sync.reset_required","project_id":7}`)
	got := decodeEventReceived(frame{kind: frameEvent, data: body})
	if got.issueNumber != 0 {
		t.Fatalf("issueNumber = %d, want 0 (missing)", got.issueNumber)
	}
}

// TestNextBackoff_Doubles_Caps: doubles each call until the ceiling,
// then stays at ceiling.
func TestNextBackoff_Doubles_Caps(t *testing.T) {
	ceiling := 30 * time.Second
	d := time.Second
	want := []time.Duration{
		2 * time.Second, 4 * time.Second, 8 * time.Second,
		16 * time.Second, 30 * time.Second, 30 * time.Second,
	}
	for i, w := range want {
		d = nextBackoff(d, ceiling)
		if d != w {
			t.Fatalf("step %d: backoff = %v, want %v", i, d, w)
		}
	}
}

// TestSSE_StreamForwardsMessages drives readSSEStream against an
// httptest.Server emitting two frames and asserts both arrive on the
// channel as the matching tea.Msg variants. The first message is the
// sseConnected status (deferred until the first frame arrives); the two
// frames follow. Last-Event-ID is omitted on the first connect.
func TestSSE_StreamForwardsMessages(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Last-Event-ID") != "" {
			t.Errorf("Last-Event-ID set on first connect: %q",
				r.Header.Get("Last-Event-ID"))
		}
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		_, _ = io.WriteString(w,
			"id: 1\nevent: issue.created\ndata: {\"type\":\"issue.created\","+
				"\"project_id\":7}\n\n")
		w.(http.Flusher).Flush()
		_, _ = io.WriteString(w,
			"id: 2\nevent: sync.reset_required\ndata: {\"reset_after_id\":2}\n\n")
		w.(http.Flusher).Flush()
	}))
	defer srv.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	ch := make(chan tea.Msg, 4)
	var lastID int64
	connected, _ := readSSEStream(ctx, srv.Client(), srv.URL, nil, 0, ch, &lastID)
	if !connected {
		t.Fatal("connected = false, want true")
	}
	if lastID != 2 {
		t.Fatalf("lastID = %d, want 2", lastID)
	}
	gotStatus := drainOne(t, ch)
	if st, ok := gotStatus.(sseStatusMsg); !ok {
		t.Fatalf("first msg = %T, want sseStatusMsg", gotStatus)
	} else if st.state != sseConnected {
		t.Fatalf("sseStatusMsg.state = %v, want sseConnected", st.state)
	}
	gotEvent := drainOne(t, ch)
	if ev, ok := gotEvent.(eventReceivedMsg); !ok {
		t.Fatalf("second msg = %T, want eventReceivedMsg", gotEvent)
	} else if ev.projectID != 7 {
		t.Fatalf("eventReceivedMsg.projectID = %d, want 7", ev.projectID)
	}
	gotReset := drainOne(t, ch)
	if _, ok := gotReset.(resetRequiredMsg); !ok {
		t.Fatalf("third msg = %T, want resetRequiredMsg", gotReset)
	}
}

// TestSSE_NoConnectedStatusBeforeFirstFrame drives readSSEStream against
// a server that returns 200 OK and immediately closes with no frames.
// Asserts that no sseConnected status is ever pushed — the only message
// the channel sees on this connection is what the caller emits. This
// regression-locks Fix I1: a flapping daemon must not flicker
// connected ↔ reconnecting between frame-less retries.
func TestSSE_NoConnectedStatusBeforeFirstFrame(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		// Return immediately — body closes with no frames.
	}))
	defer srv.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	ch := make(chan tea.Msg, 4)
	var lastID int64
	connected, _ := readSSEStream(ctx, srv.Client(), srv.URL, nil, 0, ch, &lastID)
	if connected {
		t.Fatal("connected = true, want false (no frames arrived)")
	}
	select {
	case msg := <-ch:
		t.Fatalf("expected no message, got %T = %+v", msg, msg)
	case <-time.After(50 * time.Millisecond):
		// Expected: no message on the channel.
	}
}

// TestSSE_ReconnectSendsLastEventID drives startSSE through one frame,
// closes the response, and verifies the second connection request
// carries Last-Event-ID matching the last frame seen on the first.
func TestSSE_ReconnectSendsLastEventID(t *testing.T) {
	var connects int32
	var secondHeader atomic.Value
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		n := atomic.AddInt32(&connects, 1)
		if n >= 2 {
			secondHeader.Store(r.Header.Get("Last-Event-ID"))
			// Hold so the test has time to see the header.
			<-r.Context().Done()
			return
		}
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		_, _ = io.WriteString(w,
			"id: 5\nevent: issue.created\ndata: {\"type\":\"issue.created\","+
				"\"project_id\":7}\n\n")
		w.(http.Flusher).Flush()
		// Close the connection by returning.
	}))
	defer srv.Close()

	ctx, cancel := context.WithCancel(context.Background())
	ch := make(chan tea.Msg, 8)
	done := make(chan struct{})
	go func() {
		startSSE(ctx, srv.Client(), srv.URL, nil, ch)
		close(done)
	}()

	// Wait for the first event to arrive.
	deadline := time.After(2 * time.Second)
	for {
		select {
		case <-deadline:
			t.Fatal("never saw issue.created frame on first connect")
		case msg := <-ch:
			if _, ok := msg.(eventReceivedMsg); ok {
				goto Reconnect
			}
		}
	}
Reconnect:
	// Wait for the SSE goroutine to reconnect (second connect arrives
	// after the 1s reconnect backoff). The test deadline must outlast
	// that — we use 4s for slack.
	deadline = time.After(4 * time.Second)
	for {
		select {
		case <-deadline:
			t.Fatal("second connect never arrived")
		case <-time.After(50 * time.Millisecond):
			if atomic.LoadInt32(&connects) >= 2 {
				goto Done
			}
		}
	}
Done:
	cancel()
	<-done
	// The second connect carries Last-Event-ID: 5.
	hdr, _ := secondHeader.Load().(string)
	if hdr != "5" {
		t.Fatalf("Last-Event-ID on reconnect = %q, want 5", hdr)
	}
}

// drainOne reads the next message off ch with a deadline so a stuck
// channel surfaces as a test failure rather than a hang.
func drainOne(t *testing.T, ch <-chan tea.Msg) tea.Msg {
	t.Helper()
	select {
	case msg := <-ch:
		return msg
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for SSE message")
	}
	return nil
}

// TestSSE_BuildRequest_AllProjectsOmitsQuery: nil projectID leaves the
// URL clean.
func TestSSE_BuildRequest_AllProjectsOmitsQuery(t *testing.T) {
	req, err := buildSSERequest(context.Background(), "http://x", nil, 0)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(req.URL.RawQuery, "project_id") {
		t.Fatalf("URL = %s, must not include project_id in all-projects mode",
			req.URL.String())
	}
	if got := req.Header.Get("Last-Event-ID"); got != "" {
		t.Fatalf("Last-Event-ID = %q on first connect, want empty", got)
	}
}

// TestSSE_BuildRequest_SingleProjectAddsQuery: project scope adds the
// query param; lastID > 0 sets Last-Event-ID.
func TestSSE_BuildRequest_SingleProjectAddsQuery(t *testing.T) {
	pid := int64(7)
	req, err := buildSSERequest(context.Background(), "http://x", &pid, 9)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(req.URL.RawQuery, "project_id=7") {
		t.Fatalf("URL = %s, want project_id=7", req.URL.String())
	}
	if got := req.Header.Get("Last-Event-ID"); got != "9" {
		t.Fatalf("Last-Event-ID = %q, want 9", got)
	}
}
