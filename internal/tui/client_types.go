package tui

import (
	"net/url"
	"strings"
)

// Issue is a strict subset of the daemon's wire shape: Labels and the
// deleted bool live elsewhere on the wire (labels come from a separate
// fetch; deleted is derived from DeletedAt being non-nil).
type Issue struct {
	ID           int64   `json:"id"`
	ProjectID    int64   `json:"project_id"`
	Number       int64   `json:"number"`
	Title        string  `json:"title"`
	Body         string  `json:"body"`
	Status       string  `json:"status"`
	ClosedReason *string `json:"closed_reason,omitempty"`
	Owner        *string `json:"owner,omitempty"`
	Author       string  `json:"author"`
	CreatedAt    string  `json:"created_at"`
	UpdatedAt    string  `json:"updated_at"`
	ClosedAt     *string `json:"closed_at,omitempty"`
	DeletedAt    *string `json:"deleted_at,omitempty"`
}

// ListFilter is the union of query params used by list views. status maps
// to ?status=open|closed; the rest are reserved for client-side filtering
// pending wider daemon support.
type ListFilter struct {
	Status, Owner, Author, Search string
	Labels                        []string
	IncludeDeleted                bool
}

func (f ListFilter) values() url.Values {
	v := url.Values{}
	if f.Status != "" {
		v.Set("status", f.Status)
	}
	if f.Owner != "" {
		v.Set("owner", f.Owner)
	}
	if f.Author != "" {
		v.Set("author", f.Author)
	}
	for _, l := range f.Labels {
		v.Add("label", l)
	}
	if f.Search != "" {
		v.Set("q", f.Search)
	}
	if f.IncludeDeleted {
		v.Set("include_deleted", "true")
	}
	return v
}

// CreateIssueBody is the input to CreateIssue. IdempotencyKey rides the
// Idempotency-Key header per spec §4.4 instead of the JSON body.
type CreateIssueBody struct {
	Title          string `json:"title"`
	Body           string `json:"body,omitempty"`
	Actor          string `json:"actor"`
	ForceNew       bool   `json:"force_new,omitempty"`
	IdempotencyKey string `json:"-"`
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
	ID        int64  `json:"id"`
	Type      string `json:"type"`
	CreatedAt string `json:"created_at"`
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
	ID        int64  `json:"id"`
	Author    string `json:"author"`
	Body      string `json:"body"`
	CreatedAt string `json:"created_at"`
}

// EventLogEntry is the per-event projection used by the events tab.
type EventLogEntry struct {
	ID          int64          `json:"event_id"`
	Type        string         `json:"type"`
	Actor       string         `json:"actor"`
	IssueNumber *int64         `json:"issue_number,omitempty"`
	CreatedAt   string         `json:"created_at"`
	Payload     map[string]any `json:"payload,omitempty"`
}

// LinkEntry mirrors the daemon's LinkOut wire shape.
type LinkEntry struct {
	ID         int64  `json:"id"`
	Type       string `json:"type"`
	FromNumber int64  `json:"from_number"`
	ToNumber   int64  `json:"to_number"`
	Author     string `json:"author"`
	CreatedAt  string `json:"created_at"`
}

// APIError is the structured form of the §4.6 error envelope.
type APIError struct {
	Method, Path string
	Status       int
	Code         string
	Message      string
	Hint         string
}

// Error returns "code: message[: hint: ...]".
func (e *APIError) Error() string {
	parts := []string{e.Code, e.Message}
	if e.Hint != "" {
		parts = append(parts, "hint: "+e.Hint)
	}
	return strings.Join(parts, ": ")
}

// showIssueBody mirrors the daemon's GET /issues/{number} envelope.
type showIssueBody struct {
	Issue    Issue          `json:"issue"`
	Comments []CommentEntry `json:"comments"`
	Links    []LinkEntry    `json:"links"`
}
