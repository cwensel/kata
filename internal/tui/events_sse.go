package tui

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"time"

	tea "github.com/charmbracelet/bubbletea"
)

// sseClient is the subset of *http.Client startSSE needs. Defining it
// as an interface lets sse_test.go drive readSSEStream against an
// httptest.Server-built client without exposing http.Client internals.
type sseClient interface {
	Do(req *http.Request) (*http.Response, error)
}

// startSSE is the long-lived consumer goroutine. Loops over
// readSSEStream, reconnects with exponential backoff (1s → 30s, capped),
// and resumes via Last-Event-ID once at least one frame was emitted on
// the prior connection.
func startSSE(
	ctx context.Context, hc sseClient, base string, projectID *int64, sseCh chan<- tea.Msg,
) {
	const maxBackoff = 30 * time.Second
	backoff := time.Second
	var lastID int64
	for {
		if ctx.Err() != nil {
			return
		}
		notifyStatus(ctx, sseCh, sseConnected)
		connected, err := readSSEStream(ctx, hc, base, projectID, lastID, sseCh, &lastID)
		if err == nil || ctx.Err() != nil {
			return
		}
		notifyStatus(ctx, sseCh, sseReconnecting)
		if connected {
			backoff = time.Second
		}
		select {
		case <-ctx.Done():
			return
		case <-time.After(backoff):
		}
		backoff = nextBackoff(backoff, maxBackoff)
	}
}

// nextBackoff doubles d but caps at ceiling so the goroutine doesn't
// spin at 1s forever yet doesn't sleep longer than the reconnect cap.
func nextBackoff(d, ceiling time.Duration) time.Duration {
	if d >= ceiling {
		return ceiling
	}
	d *= 2
	if d > ceiling {
		d = ceiling
	}
	return d
}

// notifyStatus pushes an sseStatusMsg without blocking past ctx cancel.
func notifyStatus(ctx context.Context, sseCh chan<- tea.Msg, st sseConnState) {
	select {
	case sseCh <- sseStatusMsg{state: st}:
	case <-ctx.Done():
	}
}

// readSSEStream issues GET /api/v1/events/stream and consumes frames
// until disconnect. lastID rides Last-Event-ID for resume when >0.
// connected is true once at least one frame was emitted so callers
// reset their backoff only on a productive connection.
func readSSEStream(
	ctx context.Context, hc sseClient, base string, projectID *int64,
	lastID int64, sseCh chan<- tea.Msg, updateLastID *int64,
) (bool, error) {
	req, err := buildSSERequest(ctx, base, projectID, lastID)
	if err != nil {
		return false, err
	}
	resp, err := hc.Do(req)
	if err != nil {
		return false, err
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		return false, fmt.Errorf("sse: status %d", resp.StatusCode)
	}
	br := bufio.NewReader(resp.Body)
	connected := false
	for {
		f, perr := readNextFrame(br)
		if errors.Is(perr, errSSEEOF) {
			return connected, errSSEEOF
		}
		if perr != nil {
			return connected, perr
		}
		connected = true
		*updateLastID = f.id
		if !forwardFrame(ctx, sseCh, f) {
			return connected, ctx.Err()
		}
	}
}

// buildSSERequest composes the streaming request. project_id is omitted
// in all-projects mode; Last-Event-ID is omitted on first connect.
func buildSSERequest(
	ctx context.Context, base string, projectID *int64, lastID int64,
) (*http.Request, error) {
	url := base + "/api/v1/events/stream"
	if projectID != nil {
		url += "?project_id=" + strconv.FormatInt(*projectID, 10)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Accept", "text/event-stream")
	if lastID > 0 {
		req.Header.Set("Last-Event-ID", strconv.FormatInt(lastID, 10))
	}
	return req, nil
}

// forwardFrame dispatches the parsed frame as the matching tea.Msg.
// Returns false on ctx cancel so the caller exits the read loop.
func forwardFrame(ctx context.Context, sseCh chan<- tea.Msg, f frame) bool {
	var msg tea.Msg
	if f.kind == frameReset {
		msg = resetRequiredMsg{resetAfterID: f.resetAfterID}
	} else {
		msg = decodeEventReceived(f)
	}
	select {
	case sseCh <- msg:
		return true
	case <-ctx.Done():
		return false
	}
}
