package db

import "time"

// Project mirrors a row in projects.
type Project struct {
	ID              int64     `json:"id"`
	Identity        string    `json:"identity"`
	Name            string    `json:"name"`
	CreatedAt       time.Time `json:"created_at"`
	NextIssueNumber int64     `json:"next_issue_number"`
}

// ProjectAlias mirrors a row in project_aliases.
type ProjectAlias struct {
	ID            int64     `json:"id"`
	ProjectID     int64     `json:"project_id"`
	AliasIdentity string    `json:"alias_identity"`
	AliasKind     string    `json:"alias_kind"`
	RootPath      string    `json:"root_path"`
	CreatedAt     time.Time `json:"created_at"`
	LastSeenAt    time.Time `json:"last_seen_at"`
}

// Issue mirrors a row in issues.
type Issue struct {
	ID           int64      `json:"id"`
	ProjectID    int64      `json:"project_id"`
	Number       int64      `json:"number"`
	Title        string     `json:"title"`
	Body         string     `json:"body"`
	Status       string     `json:"status"`
	ClosedReason *string    `json:"closed_reason,omitempty"`
	Owner        *string    `json:"owner,omitempty"`
	Author       string     `json:"author"`
	CreatedAt    time.Time  `json:"created_at"`
	UpdatedAt    time.Time  `json:"updated_at"`
	ClosedAt     *time.Time `json:"closed_at,omitempty"`
	DeletedAt    *time.Time `json:"deleted_at,omitempty"`
}

// Comment mirrors a row in comments.
type Comment struct {
	ID        int64     `json:"id"`
	IssueID   int64     `json:"issue_id"`
	Author    string    `json:"author"`
	Body      string    `json:"body"`
	CreatedAt time.Time `json:"created_at"`
}

// Event mirrors a row in events.
type Event struct {
	ID              int64     `json:"id"`
	ProjectID       int64     `json:"project_id"`
	ProjectIdentity string    `json:"project_identity"`
	IssueID         *int64    `json:"issue_id,omitempty"`
	IssueNumber     *int64    `json:"issue_number,omitempty"`
	RelatedIssueID  *int64    `json:"related_issue_id,omitempty"`
	Type            string    `json:"type"`
	Actor           string    `json:"actor"`
	Payload         string    `json:"payload"`
	CreatedAt       time.Time `json:"created_at"`
}
