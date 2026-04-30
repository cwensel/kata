package daemon

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"

	"github.com/danielgtaylor/huma/v2"

	"github.com/wesm/kata/internal/api"
	"github.com/wesm/kata/internal/db"
)

const (
	pollLimitDefault = 100
	pollLimitMax     = 1000

	// sseDrainCap is the max number of events the drain phase replays. Spec §4.8
	// says "bounded ~10k rows"; we query LIMIT cap+1 so we can detect "too far
	// behind" and emit sync.reset_required instead.
	sseDrainCap = 10000
	// sseLiveBatch caps each live-phase re-query at this many rows. A single
	// wakeup typically returns 1; we still cap to avoid pathological cases.
	sseLiveBatch = 1000 //nolint:unused // used by Task 7 runLivePhase implementation
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
		if in.ProjectID <= 0 {
			return nil, api.NewError(400, "validation", "project_id must be a positive integer", "", nil)
		}
		if _, err := cfg.DB.ProjectByID(ctx, in.ProjectID); err != nil {
			if errors.Is(err, db.ErrNotFound) {
				return nil, api.NewError(404, "project_not_found", "project not found", "", nil)
			}
			return nil, api.NewError(500, "internal", err.Error(), "", nil)
		}
		return doPollEvents(ctx, cfg, in.AfterID, in.Limit, in.ProjectID)
	})

	// SSE: not Huma — needs a streaming http.HandlerFunc on the raw mux.
	mux.HandleFunc("/api/v1/events/stream", sseHandler(cfg))
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

// sseHandler implements GET /api/v1/events/stream.
//
// Order of operations: (1) Accept negotiation — 406 on miss/wrong;
// (2) cursor parse — 400 cursor_conflict if both header and ?after_id set;
// (3) write SSE handshake bytes and flush; (4) subscribe to broadcaster;
// (5) capture hwm = MaxEventID; (6) PurgeResetCheck — if hit, write reset
// frame and return; (7) drain events (cursor, hwm] up to sseDrainCap+1;
// (8) if drain hit cap+1, emit reset frame at hwm and return (stale-cap);
// (9) write drained frames in id order; (10) live phase (Task 7).
//
// Steps 4–6 are Subscribe-first / check-second so a purge that fires between
// cursor parse and Subscribe lands on sub.Ch via the live channel; one
// committed before parse is captured by PurgeResetCheck. See spec §5.3.
func sseHandler(cfg ServerConfig) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if !acceptableForSSE(r.Header.Get("Accept")) {
			api.WriteEnvelope(w, http.StatusNotAcceptable, "not_acceptable",
				"Accept must be text/event-stream")
			return
		}

		cursor, hadHeader, hadQuery, perr := parseSSECursor(r)
		if perr != nil {
			renderAPIError(w, perr)
			return
		}
		if hadHeader && hadQuery {
			renderAPIError(w, api.NewError(400, "cursor_conflict",
				"pass either Last-Event-ID or ?after_id, not both", "", nil))
			return
		}

		var projectID int64
		if pidStr := r.URL.Query().Get("project_id"); pidStr != "" {
			n, err := strconv.ParseInt(pidStr, 10, 64)
			if err != nil || n <= 0 {
				renderAPIError(w, api.NewError(400, "validation",
					"project_id must be a positive integer", "", nil))
				return
			}
			projectID = n
		}

		flusher, ok := w.(http.Flusher)
		if !ok {
			renderAPIError(w, api.NewError(500, "internal", "streaming not supported", "", nil))
			return
		}
		w.Header().Set("Content-Type", "text/event-stream")
		w.Header().Set("Cache-Control", "no-cache")
		w.Header().Set("Connection", "keep-alive")
		w.WriteHeader(http.StatusOK)
		if _, err := io.WriteString(w, ": connected\n\n"); err != nil {
			return
		}
		flusher.Flush()

		sub := cfg.Broadcaster.Subscribe(SubFilter{ProjectID: projectID})
		defer sub.Unsub()

		ctx := r.Context()
		hwm, err := cfg.DB.MaxEventID(ctx)
		if err != nil {
			return
		}

		resetTo, err := cfg.DB.PurgeResetCheck(ctx, cursor, projectID)
		if err != nil {
			return
		}
		if resetTo > 0 {
			writeResetFrame(w, resetTo)
			flusher.Flush()
			return
		}

		rows, err := cfg.DB.EventsAfter(ctx, db.EventsAfterParams{
			AfterID: cursor, ProjectID: projectID, ThroughID: hwm, Limit: sseDrainCap + 1,
		})
		if err != nil {
			return
		}

		if len(rows) == sseDrainCap+1 {
			writeResetFrame(w, hwm)
			flusher.Flush()
			return
		}

		lastSent := cursor
		for _, ev := range rows {
			writeEventFrame(w, ev)
			flusher.Flush()
			lastSent = ev.ID
		}

		runLivePhase(ctx, w, flusher, cfg, sub.Ch, projectID, lastSent)
	}
}

// runLivePhase is implemented in Task 7. The Task 6 stub blocks on ctx so
// the existing tests (drain only) pass without seeing immediate stream
// closure.
func runLivePhase(
	ctx context.Context,
	w http.ResponseWriter,
	flusher http.Flusher,
	cfg ServerConfig,
	ch <-chan StreamMsg,
	projectID, lastSent int64,
) {
	<-ctx.Done()
	_ = w
	_ = flusher
	_ = cfg
	_ = ch
	_ = projectID
	_ = lastSent
}

func acceptableForSSE(accept string) bool {
	if accept == "" {
		return false
	}
	for _, part := range strings.Split(accept, ",") {
		mt := strings.TrimSpace(strings.SplitN(part, ";", 2)[0])
		if mt == "text/event-stream" || mt == "*/*" {
			return true
		}
	}
	return false
}

func parseSSECursor(r *http.Request) (cursor int64, hadHeader, hadQuery bool, err error) {
	if v := r.Header.Get("Last-Event-ID"); v != "" {
		hadHeader = true
		n, perr := strconv.ParseInt(v, 10, 64)
		if perr != nil || n < 0 {
			err = api.NewError(400, "validation",
				"Last-Event-ID must be a non-negative integer", "", nil)
			return
		}
		cursor = n
	}
	if v := r.URL.Query().Get("after_id"); v != "" {
		hadQuery = true
		n, perr := strconv.ParseInt(v, 10, 64)
		if perr != nil || n < 0 {
			err = api.NewError(400, "validation",
				"after_id must be a non-negative integer", "", nil)
			return
		}
		cursor = n
	}
	return
}

func renderAPIError(w http.ResponseWriter, e error) {
	var ae *api.APIError
	if !errors.As(e, &ae) {
		api.WriteEnvelope(w, 500, "internal", e.Error())
		return
	}
	api.WriteEnvelope(w, ae.Status, ae.Code, ae.Message)
}

func writeEventFrame(w io.Writer, e db.Event) {
	env := eventToEnvelope(e)
	body, _ := json.Marshal(env)
	//nolint:gosec // G705: SSE wire format, not HTML; XSS taint is a false positive.
	_, _ = fmt.Fprintf(w, "id: %d\nevent: %s\ndata: %s\n\n", e.ID, e.Type, body)
}

func writeResetFrame(w io.Writer, resetID int64) {
	body, _ := json.Marshal(api.EventReset{EventID: resetID, ResetAfterID: resetID})
	//nolint:gosec // G705: SSE wire format, not HTML; XSS taint is a false positive.
	_, _ = fmt.Fprintf(w, "id: %d\nevent: sync.reset_required\ndata: %s\n\n", resetID, body)
}
