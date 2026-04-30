// Package api types for Plan 4 events endpoints. EventEnvelope is the JSON
// shape carried in SSE data: lines and the events array of PollEventsResponse;
// it mirrors db.Event one-for-one but lives in api so the wire schema stays
// independent of internal storage shape.
package api

import (
	"encoding/json"
	"time"
)

// EventEnvelope is the wire shape for a single event row.
type EventEnvelope struct {
	EventID         int64  `json:"event_id"`
	Type            string `json:"type"`
	ProjectID       int64  `json:"project_id"`
	ProjectIdentity string `json:"project_identity"`
	IssueID         *int64 `json:"issue_id,omitempty"`
	IssueNumber     *int64 `json:"issue_number,omitempty"`
	RelatedIssueID  *int64 `json:"related_issue_id,omitempty"`
	Actor           string `json:"actor"`
	// Payload is the event-type-specific JSON object. Always valid JSON
	// because the schema enforces json_valid(payload) at write time.
	Payload   json.RawMessage `json:"payload,omitempty"`
	CreatedAt time.Time       `json:"created_at"`
}

// EventReset is the data: payload of a sync.reset_required SSE frame and the
// stripped-down content of a poll response when the cursor falls inside a
// purge gap.
type EventReset struct {
	EventID      int64 `json:"event_id"`       // == ResetAfterID; mirrors the SSE id: line.
	ResetAfterID int64 `json:"reset_after_id"` // minimum safe resume cursor.
}

// PollEventsRequest is GET /api/v1/events and GET /api/v1/projects/{id}/events.
// AfterID is exclusive; the response's NextAfterID is the cursor the client
// should pass on the next request. Limit defaults to 100 and is clamped to
// 1000 server-side; non-positive Limit returns 400 validation.
type PollEventsRequest struct {
	ProjectID int64 `path:"project_id"`
	AfterID   int64 `query:"after_id,omitempty"`
	Limit     int   `query:"limit,omitempty"`
}

// PollEventsResponse is the response for both polling endpoints. ResetRequired
// signals a purge-invalidated cursor; when true, Events is empty and the
// client should refetch state and resume from ResetAfterID.
type PollEventsResponse struct {
	Body struct {
		ResetRequired bool            `json:"reset_required"`
		ResetAfterID  int64           `json:"reset_after_id,omitempty"`
		Events        []EventEnvelope `json:"events"`        // always non-nil; empty array on no rows
		NextAfterID   int64           `json:"next_after_id"` // = max events.id in response, or after_id if empty
	}
}
