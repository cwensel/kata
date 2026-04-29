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
		Actor string `json:"actor" required:"true"`
		Title string `json:"title" required:"true"`
		Body  string `json:"body,omitempty"`
	}
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

// ShowIssueResponse is the per-issue read payload (Plan 1: issue + comments).
type ShowIssueResponse struct {
	Body struct {
		Issue    db.Issue     `json:"issue"`
		Comments []db.Comment `json:"comments"`
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
