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
// "id:" line so we can resume via Last-Event-ID. The reset frame's
// reset_after_id payload field is intentionally ignored: it is the
// daemon's contract (api.EventReset.EventID == ResetAfterID) that the
// frame's id: line is the authoritative resume cursor, and the consumer
// already updates Last-Event-ID off id: on every frame.
type frame struct {
	kind      frameKind
	id        int64
	eventType string
	data      []byte
}

// sseEventPayload mirrors the fields of api.EventEnvelope the TUI
// inspects. Lives here so the parser does not pull internal/api into
// the TUI tree.
type sseEventPayload struct {
	Type            string          `json:"type"`
	ProjectID       int64           `json:"project_id"`
	ProjectUID      string          `json:"project_uid,omitempty"`
	IssueNumber     *int64          `json:"issue_number,omitempty"`
	IssueUID        string          `json:"issue_uid,omitempty"`
	RelatedIssueUID string          `json:"related_issue_uid,omitempty"`
	Payload         json.RawMessage `json:"payload,omitempty"`
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

// finalizeFrame classifies the frame. sync.reset_required becomes
// frameReset; everything else is frameEvent.
func finalizeFrame(f frame) frame {
	if f.eventType == "sync.reset_required" {
		f.kind = frameReset
		return f
	}
	f.kind = frameEvent
	return f
}

// decodeEventReceived parses the JSON body into eventReceivedMsg.
// Missing fields fall through as zero values.
//
// issue.created events carry their initial parent link (when present)
// folded into a `links` array on the payload — see
// internal/db/queries.go::buildCreatedPayload. The TUI's open-detail
// refetch logic needs that parent reference to know whether the new
// child belongs to the issue currently on screen, so we surface the
// first parent entry as msg.link with FromNumber filled in from the
// new issue's own number (the payload only carries to_number; the
// from is implicit). issue.linked / issue.unlinked retain their
// original single-link shape (link object directly on payload).
func decodeEventReceived(f frame) eventReceivedMsg {
	var p sseEventPayload
	_ = json.Unmarshal(f.data, &p)
	out := eventReceivedMsg{
		eventType:       p.Type,
		projectID:       p.ProjectID,
		projectUID:      p.ProjectUID,
		issueUID:        p.IssueUID,
		relatedIssueUID: p.RelatedIssueUID,
	}
	if p.IssueNumber != nil {
		out.issueNumber = *p.IssueNumber
	}
	if p.Type == "issue.linked" || p.Type == "issue.unlinked" {
		var link linkPayload
		if len(p.Payload) > 0 && json.Unmarshal(p.Payload, &link) == nil {
			out.link = &link
		}
	}
	if p.Type == "issue.created" && len(p.Payload) > 0 {
		out.link = parentLinkFromCreatedPayload(p.Payload, out.issueNumber, out.issueUID)
	}
	return out
}

// parentLinkFromCreatedPayload extracts the first parent link from an
// issue.created payload's `links` array. Returns nil when the payload
// has no parent link or fails to parse. fromNumber is the new issue's
// own number — the payload only stores to_number, so we fill in from
// at decode time so downstream matchesIssueNumber checks both ends.
func parentLinkFromCreatedPayload(payload []byte, fromNumber int64, fromIssueUID string) *linkPayload {
	var body struct {
		Links []struct {
			Type       string `json:"type"`
			ToNumber   int64  `json:"to_number"`
			ToIssueUID string `json:"to_issue_uid,omitempty"`
		} `json:"links"`
	}
	if err := json.Unmarshal(payload, &body); err != nil {
		return nil
	}
	for _, l := range body.Links {
		if l.Type == "parent" {
			return &linkPayload{
				Type:         "parent",
				FromNumber:   fromNumber,
				ToNumber:     l.ToNumber,
				FromIssueUID: fromIssueUID,
				ToIssueUID:   l.ToIssueUID,
			}
		}
	}
	return nil
}
