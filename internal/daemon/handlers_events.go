package daemon

import (
	"context"
	"encoding/json"
	"net/http"

	"github.com/danielgtaylor/huma/v2"

	"github.com/wesm/kata/internal/api"
	"github.com/wesm/kata/internal/db"
)

const (
	pollLimitDefault = 100
	pollLimitMax     = 1000
)

func registerEventsHandlers(humaAPI huma.API, mux *http.ServeMux, cfg ServerConfig) {
	huma.Register(humaAPI, huma.Operation{
		OperationID: "pollEvents",
		Method:      "GET",
		Path:        "/api/v1/events",
	}, func(ctx context.Context, in *api.PollEventsGlobalRequest) (*api.PollEventsResponse, error) {
		return doPollEvents(ctx, cfg, in.AfterID, in.Limit, 0)
	})

	huma.Register(humaAPI, huma.Operation{
		OperationID: "pollProjectEvents",
		Method:      "GET",
		Path:        "/api/v1/projects/{project_id}/events",
	}, func(ctx context.Context, in *api.PollEventsRequest) (*api.PollEventsResponse, error) {
		return doPollEvents(ctx, cfg, in.AfterID, in.Limit, in.ProjectID)
	})

	// SSE endpoint placeholder — see Tasks 6 / 7.
	_ = mux
}

// resolveLimit normalizes the optional Limit query param: explicit non-positive
// values are a 400 validation error; missing or zero values default to
// pollLimitDefault; values above pollLimitMax silently clamp.
func resolveLimit(rawLimit api.OptionalInt) (int, error) {
	if rawLimit.IsSet && rawLimit.Value <= 0 {
		return 0, api.NewError(400, "validation", "limit must be a positive integer", "", nil)
	}
	if !rawLimit.IsSet {
		return pollLimitDefault, nil
	}
	if rawLimit.Value > pollLimitMax {
		return pollLimitMax, nil
	}
	return rawLimit.Value, nil
}

// doPollEvents is the shared implementation for both polling endpoints. When
// projectID is 0 it is a cross-project poll; otherwise events are filtered to
// that project.
func doPollEvents(
	ctx context.Context,
	cfg ServerConfig,
	afterID int64,
	rawLimit api.OptionalInt,
	projectID int64,
) (*api.PollEventsResponse, error) {
	limit, err := resolveLimit(rawLimit)
	if err != nil {
		return nil, err
	}

	resetTo, err := cfg.DB.PurgeResetCheck(ctx, afterID, projectID)
	if err != nil {
		return nil, api.NewError(500, "internal", err.Error(), "", nil)
	}
	if resetTo > 0 {
		out := &api.PollEventsResponse{}
		out.Body.ResetRequired = true
		out.Body.ResetAfterID = resetTo
		out.Body.Events = []api.EventEnvelope{}
		out.Body.NextAfterID = resetTo
		return out, nil
	}

	// Unknown project_id: return empty events rather than 404. Polling is
	// idempotent and a fresh client may legitimately race a project's creation.
	rows, err := cfg.DB.EventsAfter(ctx, db.EventsAfterParams{
		AfterID:   afterID,
		ProjectID: projectID,
		Limit:     limit,
	})
	if err != nil {
		return nil, api.NewError(500, "internal", err.Error(), "", nil)
	}

	out := &api.PollEventsResponse{}
	out.Body.ResetRequired = false
	out.Body.Events = toEnvelopes(rows)
	out.Body.NextAfterID = nextAfterID(rows, afterID)
	return out, nil
}

func toEnvelopes(rows []db.Event) []api.EventEnvelope {
	out := make([]api.EventEnvelope, 0, len(rows))
	for _, r := range rows {
		out = append(out, eventToEnvelope(r))
	}
	return out
}

func eventToEnvelope(e db.Event) api.EventEnvelope {
	var payload json.RawMessage
	if e.Payload != "" {
		payload = json.RawMessage(e.Payload)
	}
	return api.EventEnvelope{
		EventID:         e.ID,
		Type:            e.Type,
		ProjectID:       e.ProjectID,
		ProjectIdentity: e.ProjectIdentity,
		IssueID:         e.IssueID,
		IssueNumber:     e.IssueNumber,
		RelatedIssueID:  e.RelatedIssueID,
		Actor:           e.Actor,
		Payload:         payload,
		CreatedAt:       e.CreatedAt,
	}
}

func nextAfterID(rows []db.Event, afterID int64) int64 {
	if len(rows) == 0 {
		return afterID
	}
	return rows[len(rows)-1].ID
}
