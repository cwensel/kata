// Package api defines the request/response DTOs for the kata daemon HTTP API.
package api //nolint:revive // package name "api" is fixed by Plan 1 §4 wire-types layout.

import (
	"time"

	"github.com/wesm/kata/internal/db"
)

// PingResponse mirrors the cheapest liveness response.
type PingResponse struct {
	Body struct {
		OK bool `json:"ok"`
	}
}

// HealthResponse mirrors /api/v1/health.
type HealthResponse struct {
	Body struct {
		OK            bool      `json:"ok"`
		DBPath        string    `json:"db_path"`
		SchemaVersion int       `json:"schema_version"`
		Uptime        string    `json:"uptime"`
		StartedAt     time.Time `json:"started_at"`
	}
}

// ResolveProjectRequest is POST /api/v1/projects/resolve.
type ResolveProjectRequest struct {
	Body struct {
		StartPath string `json:"start_path" doc:"absolute path to resolve from" required:"true"`
	}
}

// ProjectResolveBody is the JSON body field of a successful resolve response.
type ProjectResolveBody struct {
	Project       db.Project      `json:"project"`
	Alias         db.ProjectAlias `json:"alias"`
	WorkspaceRoot string          `json:"workspace_root,omitempty"`
}

// ResolveProjectResponse wraps ProjectResolveBody.
type ResolveProjectResponse struct {
	Body ProjectResolveBody
}

// InitProjectRequest is POST /api/v1/projects (used by `kata init`).
type InitProjectRequest struct {
	Body struct {
		StartPath       string `json:"start_path" required:"true"`
		ProjectIdentity string `json:"project_identity,omitempty"`
		Name            string `json:"name,omitempty"`
		Replace         bool   `json:"replace,omitempty"`
		Reassign        bool   `json:"reassign,omitempty"`
	}
}

// InitProjectResponse uses ProjectResolveBody plus a "created" flag.
type InitProjectResponse struct {
	Body struct {
		ProjectResolveBody
		Created bool `json:"created"`
	}
}

// ListProjectsResponse is GET /api/v1/projects.
type ListProjectsResponse struct {
	Body struct {
		Projects []db.Project `json:"projects"`
	}
}

// ShowProjectResponse is GET /api/v1/projects/{id}.
type ShowProjectResponse struct {
	Body struct {
		Project db.Project        `json:"project"`
		Aliases []db.ProjectAlias `json:"aliases"`
	}
}

// CreateIssueRequest is POST /api/v1/projects/{id}/issues.
type CreateIssueRequest struct {
	ProjectID int64 `path:"project_id" required:"true"`
	Body      struct {
		Actor  string                  `json:"actor" required:"true"`
		Title  string                  `json:"title" required:"true"`
		Body   string                  `json:"body,omitempty"`
		Owner  *string                 `json:"owner,omitempty"`
		Labels []string                `json:"labels,omitempty"`
		Links  []CreateInitialLinkBody `json:"links,omitempty"`
	}
}

// CreateInitialLinkBody is one entry in CreateIssueRequest.Body.Links.
type CreateInitialLinkBody struct {
	Type     string `json:"type" enum:"parent,blocks,related"`
	ToNumber int64  `json:"to_number"`
}

// MutationResponse is the standard mutation envelope (§4.5).
type MutationResponse struct {
	Body struct {
		Issue   db.Issue  `json:"issue"`
		Event   *db.Event `json:"event"`
		Changed bool      `json:"changed"`
		Reused  bool      `json:"reused,omitempty"`
	}
}

// ListIssuesRequest is GET /api/v1/projects/{id}/issues.
type ListIssuesRequest struct {
	ProjectID int64  `path:"project_id" required:"true"`
	Status    string `query:"status,omitempty" enum:"open,closed,"`
	Limit     int    `query:"limit,omitempty"`
}

// ListIssuesResponse is the list payload.
type ListIssuesResponse struct {
	Body struct {
		Issues []db.Issue `json:"issues"`
	}
}

// ShowIssueRequest is GET /api/v1/projects/{id}/issues/{number}.
type ShowIssueRequest struct {
	ProjectID int64 `path:"project_id" required:"true"`
	Number    int64 `path:"number" required:"true"`
}

// ShowIssueResponse is the per-issue read payload (Plan 2: + links, + labels).
type ShowIssueResponse struct {
	Body struct {
		Issue    db.Issue        `json:"issue"`
		Comments []db.Comment    `json:"comments"`
		Links    []LinkOut       `json:"links"`
		Labels   []db.IssueLabel `json:"labels"`
	}
}

// EditIssueRequest is PATCH /api/v1/projects/{id}/issues/{number}.
type EditIssueRequest struct {
	ProjectID int64 `path:"project_id" required:"true"`
	Number    int64 `path:"number" required:"true"`
	Body      struct {
		Actor string  `json:"actor" required:"true"`
		Title *string `json:"title,omitempty"`
		Body  *string `json:"body,omitempty"`
		Owner *string `json:"owner,omitempty"`
	}
}

// CommentRequest is POST /api/v1/projects/{id}/issues/{number}/comments.
type CommentRequest struct {
	ProjectID int64 `path:"project_id" required:"true"`
	Number    int64 `path:"number" required:"true"`
	Body      struct {
		Actor string `json:"actor" required:"true"`
		Body  string `json:"body" required:"true"`
	}
}

// CommentResponse mirrors MutationResponse but adds the new comment row.
type CommentResponse struct {
	Body struct {
		Issue   db.Issue   `json:"issue"`
		Comment db.Comment `json:"comment"`
		Event   *db.Event  `json:"event"`
		Changed bool       `json:"changed"`
	}
}

// ActionRequest is POST /api/v1/projects/{id}/issues/{number}/actions/close|reopen.
// Reason is enforced to the schema's CHECK list so unsupported values surface
// as 400 validation rather than a SQLite constraint failure (500 internal).
type ActionRequest struct {
	ProjectID int64 `path:"project_id" required:"true"`
	Number    int64 `path:"number" required:"true"`
	Body      struct {
		Actor  string `json:"actor" required:"true"`
		Reason string `json:"reason,omitempty" enum:"done,wontfix,duplicate,"` // close only
	}
}

// CreateLinkRequest is POST /api/v1/projects/{id}/issues/{number}/links.
type CreateLinkRequest struct {
	ProjectID int64 `path:"project_id" required:"true"`
	Number    int64 `path:"number" required:"true"`
	Body      struct {
		Actor    string `json:"actor" required:"true"`
		Type     string `json:"type" required:"true" enum:"parent,blocks,related"`
		ToNumber int64  `json:"to_number" required:"true"`
		Replace  bool   `json:"replace,omitempty"` // type=parent only
	}
}

// LinkOut is the wire projection of a link with both endpoint *numbers* (not
// internal issue ids) so clients can correlate without an extra lookup.
type LinkOut struct {
	ID         int64     `json:"id"`
	ProjectID  int64     `json:"project_id"`
	FromNumber int64     `json:"from_number"`
	ToNumber   int64     `json:"to_number"`
	Type       string    `json:"type"`
	Author     string    `json:"author"`
	CreatedAt  time.Time `json:"created_at"`
}

// CreateLinkResponse extends MutationResponse with the new link's wire
// projection (handlers populate `Link` for both new and no-op cases).
type CreateLinkResponse struct {
	Body struct {
		Issue   db.Issue  `json:"issue"`
		Link    LinkOut   `json:"link"`
		Event   *db.Event `json:"event"`
		Changed bool      `json:"changed"`
	}
}

// DeleteLinkRequest is DELETE /api/v1/projects/{id}/issues/{number}/links/{link_id}.
// Actor is in the query string because DELETE bodies are non-portable.
type DeleteLinkRequest struct {
	ProjectID int64  `path:"project_id" required:"true"`
	Number    int64  `path:"number" required:"true"`
	LinkID    int64  `path:"link_id" required:"true"`
	Actor     string `query:"actor" required:"true"`
}

// AddLabelRequest is POST /api/v1/projects/{id}/issues/{number}/labels.
type AddLabelRequest struct {
	ProjectID int64 `path:"project_id" required:"true"`
	Number    int64 `path:"number" required:"true"`
	Body      struct {
		Actor string `json:"actor" required:"true"`
		Label string `json:"label" required:"true"`
	}
}

// AddLabelResponse extends the standard envelope with the new label row.
type AddLabelResponse struct {
	Body struct {
		Issue   db.Issue      `json:"issue"`
		Label   db.IssueLabel `json:"label"`
		Event   *db.Event     `json:"event"`
		Changed bool          `json:"changed"`
	}
}

// RemoveLabelRequest is DELETE /api/v1/projects/{id}/issues/{number}/labels/{label}.
type RemoveLabelRequest struct {
	ProjectID int64  `path:"project_id" required:"true"`
	Number    int64  `path:"number" required:"true"`
	Label     string `path:"label" required:"true"`
	Actor     string `query:"actor" required:"true"`
}

// AssignRequest is POST /api/v1/projects/{id}/issues/{number}/actions/assign.
type AssignRequest struct {
	ProjectID int64 `path:"project_id" required:"true"`
	Number    int64 `path:"number" required:"true"`
	Body      struct {
		Actor string `json:"actor" required:"true"`
		Owner string `json:"owner" required:"true"`
	}
}

// UnassignRequest is POST /api/v1/projects/{id}/issues/{number}/actions/unassign.
// Same shape as AssignRequest minus owner.
type UnassignRequest struct {
	ProjectID int64 `path:"project_id" required:"true"`
	Number    int64 `path:"number" required:"true"`
	Body      struct {
		Actor string `json:"actor" required:"true"`
	}
}

// ReadyRequest is GET /api/v1/projects/{id}/ready.
type ReadyRequest struct {
	ProjectID int64 `path:"project_id" required:"true"`
	Limit     int   `query:"limit,omitempty"`
}

// ReadyResponse is the ready-issue list.
type ReadyResponse struct {
	Body struct {
		Issues []db.Issue `json:"issues"`
	}
}

// LabelsListRequest is GET /api/v1/projects/{id}/labels (counts).
type LabelsListRequest struct {
	ProjectID int64 `path:"project_id" required:"true"`
}

// LabelsListResponse is the per-label aggregate.
type LabelsListResponse struct {
	Body struct {
		Labels []db.LabelCount `json:"labels"`
	}
}
