package tui

import (
	"fmt"
	"net/url"
	"strconv"
	"strings"
	"time"
)

// Issue is a strict subset of the daemon's wire shape. Labels rides on
// list-row decode (the daemon embeds them per row) and on a manual
// copy from showIssue's body.labels for detail open; the omitempty tag
// keeps absence on a show response from blanking a previously-populated
// slice. The deleted bool is derived from DeletedAt being non-nil.
type Issue struct {
	ID           int64        `json:"id"`
	ProjectID    int64        `json:"project_id"`
	Number       int64        `json:"number"`
	Title        string       `json:"title"`
	Body         string       `json:"body"`
	Status       string       `json:"status"`
	ClosedReason *string      `json:"closed_reason,omitempty"`
	Owner        *string      `json:"owner,omitempty"`
	Author       string       `json:"author"`
	CreatedAt    time.Time    `json:"created_at"`
	UpdatedAt    time.Time    `json:"updated_at"`
	ClosedAt     *time.Time   `json:"closed_at,omitempty"`
	DeletedAt    *time.Time   `json:"deleted_at,omitempty"`
	Labels       []string     `json:"labels,omitempty"`
	ParentNumber *int64       `json:"parent_number,omitempty"`
	ChildCounts  *ChildCounts `json:"child_counts,omitempty"`
}

type ChildCounts struct {
	Open  int `json:"open"`
	Total int `json:"total"`
}

type IssueRef struct {
	Number int64  `json:"number"`
	Title  string `json:"title"`
	Status string `json:"status"`
}

// ListFilter is the union of filters used by list views. Limit is sent
// on the wire for capped working-set fetches. Status/Owner/Author/
// Labels/Search are applied client-side after the daemon returns results.
//
// IncludeDeleted is deliberately absent: the daemon's list endpoint
// hard-codes deleted_at IS NULL (internal/db/queries.go::ListIssues) and
// has no include_deleted query param, so a client-side flag would
// promise something the wire cannot deliver. Re-introducing it requires
// daemon work and is deferred to a future task.
type ListFilter struct {
	Status, Owner, Author, Search string
	Labels                        []string
	Limit                         int
}

// values returns the query params the daemon honors for the TUI queue
// fetch path.
func (f ListFilter) values() url.Values {
	v := url.Values{}
	if f.Status != "" {
		v.Set("status", f.Status)
	}
	if f.Limit > 0 {
		v.Set("limit", strconv.Itoa(f.Limit))
	}
	return v
}

type CreateInitialLinkBody struct {
	Type     string `json:"type"`
	ToNumber int64  `json:"to_number"`
}

// CreateIssueBody is the input to CreateIssue. IdempotencyKey rides the
// Idempotency-Key header per spec §4.4 instead of the JSON body.
//
// Owner and Labels are populated by the new-issue form (Plan 8 commit 4)
// when the user fills in the optional fields; they are omitted from the
// payload when zero so an inline-row commit does not promise the daemon
// fields it has no value for.
type CreateIssueBody struct {
	Title          string                  `json:"title"`
	Body           string                  `json:"body,omitempty"`
	Actor          string                  `json:"actor"`
	Owner          *string                 `json:"owner,omitempty"`
	Labels         []string                `json:"labels,omitempty"`
	Links          []CreateInitialLinkBody `json:"links,omitempty"`
	ForceNew       bool                    `json:"force_new,omitempty"`
	IdempotencyKey string                  `json:"-"`
}

// LinkBody is the request projection for POST /links.
type LinkBody struct {
	Type     string `json:"type"`
	ToNumber int64  `json:"to_number"`
}

// MutationResp mirrors the §4.5 mutation envelope.
type MutationResp struct {
	Issue   *Issue         `json:"issue"`
	Event   *EventEnvelope `json:"event,omitempty"`
	Changed bool           `json:"changed"`
	Reused  bool           `json:"reused,omitempty"`
}

// EventEnvelope is the minimal event projection embedded in mutation
// responses. The richer poll/SSE shape uses EventLogEntry.
type EventEnvelope struct {
	ID        int64     `json:"id"`
	Type      string    `json:"type"`
	CreatedAt time.Time `json:"created_at"`
}

// ResolveResp is the body of POST /projects/resolve.
type ResolveResp struct {
	Project struct {
		ID              int64  `json:"id"`
		Identity        string `json:"identity"`
		Name            string `json:"name"`
		NextIssueNumber int64  `json:"next_issue_number"`
	} `json:"project"`
	WorkspaceRoot string `json:"workspace_root"`
}

// ProjectSummary is one row of GET /projects.
type ProjectSummary struct {
	ID       int64  `json:"id"`
	Identity string `json:"identity"`
	Name     string `json:"name"`
}

// CommentEntry is the per-comment projection rendered in the comments tab.
type CommentEntry struct {
	ID        int64     `json:"id"`
	Author    string    `json:"author"`
	Body      string    `json:"body"`
	CreatedAt time.Time `json:"created_at"`
}

// EventLogEntry is the per-event projection used by the events tab.
type EventLogEntry struct {
	ID          int64          `json:"event_id"`
	Type        string         `json:"type"`
	Actor       string         `json:"actor"`
	IssueNumber *int64         `json:"issue_number,omitempty"`
	CreatedAt   time.Time      `json:"created_at"`
	Payload     map[string]any `json:"payload,omitempty"`
}

// LinkEntry mirrors the daemon's LinkOut wire shape.
type LinkEntry struct {
	ID         int64     `json:"id"`
	Type       string    `json:"type"`
	FromNumber int64     `json:"from_number"`
	ToNumber   int64     `json:"to_number"`
	Author     string    `json:"author"`
	CreatedAt  time.Time `json:"created_at"`
}

// APIError is the structured form of the §4.6 error envelope.
type APIError struct {
	Method, Path string
	Status       int
	Code         string
	Message      string
	Hint         string
}

// Error returns "code: message[: hint: ...]" when the daemon supplied a
// structured envelope. When Code and Message are both empty (a 404 with
// no body, a 502 from a proxy, etc.) it falls back to a method+path+status
// summary so toasts stay actionable.
func (e *APIError) Error() string {
	if e.Code == "" && e.Message == "" {
		return fmt.Sprintf("%s %s: HTTP %d", e.Method, e.Path, e.Status)
	}
	parts := []string{e.Code, e.Message}
	if e.Hint != "" {
		parts = append(parts, "hint: "+e.Hint)
	}
	return strings.Join(parts, ": ")
}

// showIssueBody mirrors the daemon's GET /issues/{number} envelope.
// The daemon ships labels as a sibling slice (one IssueLabel per row);
// showIssueLabel keeps decode tight to the fields the TUI needs.
type showIssueBody struct {
	Issue    Issue            `json:"issue"`
	Comments []CommentEntry   `json:"comments"`
	Links    []LinkEntry      `json:"links"`
	Labels   []showIssueLabel `json:"labels"`
	Parent   *IssueRef        `json:"parent,omitempty"`
	Children []Issue          `json:"children,omitempty"`
}

type IssueDetail struct {
	Issue    *Issue
	Parent   *IssueRef
	Children []Issue
}

// showIssueLabel is the per-label projection from showIssue's labels
// slice. The wire shape is db.IssueLabel (issue_id, label, author,
// created_at) — only label is used for rendering.
type showIssueLabel struct {
	Label string `json:"label"`
}
