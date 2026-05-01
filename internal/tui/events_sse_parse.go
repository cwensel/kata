package tui

import (
	"bufio"
	"encoding/json"
	"errors"
	"io"
	"strconv"
	"strings"
)

// frameKind discriminates an SSE frame's purpose. frameReset is the
// terminal sync.reset_required frame that drops the cache.
type frameKind int

const (
	frameEvent frameKind = iota
	frameReset
)

// frame is the parsed shape of one SSE event block. id mirrors the
// "id:" line so we can resume via Last-Event-ID. resetAfterID is
// populated only when kind == frameReset.
type frame struct {
	kind         frameKind
	id           int64
	eventType    string
	data         []byte
	resetAfterID int64
}

// sseResetPayload mirrors api.EventReset; sseEventPayload mirrors the
// fields of api.EventEnvelope the TUI inspects. Both live here so the
// parser does not pull internal/api into the TUI tree.
type sseResetPayload struct {
	ResetAfterID int64 `json:"reset_after_id"`
}

type sseEventPayload struct {
	Type        string `json:"type"`
	ProjectID   int64  `json:"project_id"`
	IssueNumber *int64 `json:"issue_number,omitempty"`
}

// errSSEEOF is the sentinel readNextFrame returns when the underlying
// reader is exhausted with no in-progress frame.
var errSSEEOF = errors.New("sse: stream ended")

// parseAllFrames consumes r to EOF and returns every valid frame. A
// test-only entry point — production reads frames one at a time so the
// consumer can dispatch as they arrive.
func parseAllFrames(r io.Reader) ([]frame, error) {
	br := bufio.NewReader(r)
	var out []frame
	for {
		f, err := readNextFrame(br)
		if errors.Is(err, errSSEEOF) {
			return out, nil
		}
		if err != nil {
			return out, err
		}
		out = append(out, f)
	}
}

// readNextFrame reads one frame off br. Malformed frames (no data line
// or blank event type) reset on the blank-line terminator and continue
// so a single bad frame can't wedge the long-lived consumer.
func readNextFrame(br *bufio.Reader) (frame, error) {
	cur := frame{}
	var hasEvent, hasData bool
	for {
		line, err := br.ReadString('\n')
		if err != nil {
			if errors.Is(err, io.EOF) {
				return frame{}, errSSEEOF
			}
			return frame{}, err
		}
		line = strings.TrimRight(line, "\r\n")
		if line == "" {
			if !hasEvent || !hasData {
				cur, hasEvent, hasData = frame{}, false, false
				continue
			}
			return finalizeFrame(cur), nil
		}
		if strings.HasPrefix(line, ":") {
			continue
		}
		applySSEField(&cur, line, &hasEvent, &hasData)
	}
}

// applySSEField mutates cur for a single id/event/data line. Unknown
// fields are ignored per the SSE spec.
func applySSEField(cur *frame, line string, hasEvent, hasData *bool) {
	switch {
	case strings.HasPrefix(line, "id: "):
		if n, err := strconv.ParseInt(strings.TrimPrefix(line, "id: "), 10, 64); err == nil {
			cur.id = n
		}
	case strings.HasPrefix(line, "event: "):
		v := strings.TrimPrefix(line, "event: ")
		if v != "" {
			cur.eventType = v
			*hasEvent = true
		}
	case strings.HasPrefix(line, "data: "):
		cur.data = []byte(strings.TrimPrefix(line, "data: "))
		*hasData = true
	}
}

// finalizeFrame classifies the frame and lifts reset_after_id off the
// JSON body for sync.reset_required so the consumer receives an int.
func finalizeFrame(f frame) frame {
	if f.eventType == "sync.reset_required" {
		var p sseResetPayload
		_ = json.Unmarshal(f.data, &p)
		f.resetAfterID = p.ResetAfterID
		f.kind = frameReset
		return f
	}
	f.kind = frameEvent
	return f
}

// decodeEventReceived parses the JSON body into eventReceivedMsg.
// Missing fields fall through as zero values.
func decodeEventReceived(f frame) eventReceivedMsg {
	var p sseEventPayload
	_ = json.Unmarshal(f.data, &p)
	out := eventReceivedMsg{eventType: p.Type, projectID: p.ProjectID}
	if p.IssueNumber != nil {
		out.issueNumber = *p.IssueNumber
	}
	return out
}
