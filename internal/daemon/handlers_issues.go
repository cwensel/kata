package daemon

import (
	"context"
	"errors"

	"github.com/danielgtaylor/huma/v2"

	"github.com/wesm/kata/internal/api"
	"github.com/wesm/kata/internal/db"
)

// registerIssuesHandlers installs the four issue routes (create/list/show/edit)
// on humaAPI. CreateIssue writes both the issue row and the matching
// issue.created event in one tx (see db.CreateIssue) so the response always
// carries an event for the CLI to render.
func registerIssuesHandlers(humaAPI huma.API, cfg ServerConfig) {
	huma.Register(humaAPI, huma.Operation{
		OperationID: "createIssue",
		Method:      "POST",
		Path:        "/api/v1/projects/{project_id}/issues",
	}, func(ctx context.Context, in *api.CreateIssueRequest) (*api.MutationResponse, error) {
		if err := validateActor(in.Body.Actor); err != nil {
			return nil, err
		}
		if _, err := cfg.DB.ProjectByID(ctx, in.ProjectID); err != nil {
			if errors.Is(err, db.ErrNotFound) {
				return nil, api.NewError(404, "project_not_found", "project not found", "", nil)
			}
			return nil, api.NewError(500, "internal", err.Error(), "", nil)
		}

		links := make([]db.InitialLink, 0, len(in.Body.Links))
		for _, l := range in.Body.Links {
			links = append(links, db.InitialLink{Type: l.Type, ToNumber: l.ToNumber})
		}

		issue, evt, err := cfg.DB.CreateIssue(ctx, db.CreateIssueParams{
			ProjectID: in.ProjectID,
			Title:     in.Body.Title,
			Body:      in.Body.Body,
			Author:    in.Body.Actor,
			Owner:     in.Body.Owner,
			Labels:    in.Body.Labels,
			Links:     links,
		})
		switch {
		case errors.Is(err, db.ErrInitialLinkInvalidType):
			return nil, api.NewError(400, "validation",
				"link.type must be parent|blocks|related", "", nil)
		case errors.Is(err, db.ErrInitialLinkTargetNotFound):
			return nil, api.NewError(404, "issue_not_found",
				"initial link target not found in this project", "", nil)
		case errors.Is(err, db.ErrLabelInvalid):
			return nil, api.NewError(400, "validation",
				"label must match charset [a-z0-9._:-] and length 1..64", "", nil)
		case errors.Is(err, db.ErrParentAlreadySet):
			return nil, api.NewError(409, "parent_already_set",
				"duplicate parent in initial links", "pass at most one parent link", nil)
		case err != nil:
			return nil, api.NewError(500, "internal", err.Error(), "", nil)
		}
		out := &api.MutationResponse{}
		out.Body.Issue = issue
		out.Body.Event = &evt
		out.Body.Changed = true
		return out, nil
	})

	huma.Register(humaAPI, huma.Operation{
		OperationID: "listIssues",
		Method:      "GET",
		Path:        "/api/v1/projects/{project_id}/issues",
	}, func(ctx context.Context, in *api.ListIssuesRequest) (*api.ListIssuesResponse, error) {
		if _, err := cfg.DB.ProjectByID(ctx, in.ProjectID); err != nil {
			if errors.Is(err, db.ErrNotFound) {
				return nil, api.NewError(404, "project_not_found", "project not found", "", nil)
			}
			return nil, api.NewError(500, "internal", err.Error(), "", nil)
		}
		issues, err := cfg.DB.ListIssues(ctx, db.ListIssuesParams{
			ProjectID: in.ProjectID,
			Status:    in.Status,
			Limit:     in.Limit,
		})
		if err != nil {
			return nil, api.NewError(500, "internal", err.Error(), "", nil)
		}
		out := &api.ListIssuesResponse{}
		out.Body.Issues = issues
		return out, nil
	})

	huma.Register(humaAPI, huma.Operation{
		OperationID: "showIssue",
		Method:      "GET",
		Path:        "/api/v1/projects/{project_id}/issues/{number}",
	}, func(ctx context.Context, in *api.ShowIssueRequest) (*api.ShowIssueResponse, error) {
		issue, err := cfg.DB.IssueByNumber(ctx, in.ProjectID, in.Number)
		if errors.Is(err, db.ErrNotFound) {
			return nil, api.NewError(404, "issue_not_found", "issue not found", "", nil)
		}
		if err != nil {
			return nil, api.NewError(500, "internal", err.Error(), "", nil)
		}
		comments, err := listComments(ctx, cfg.DB, issue.ID)
		if err != nil {
			return nil, api.NewError(500, "internal", err.Error(), "", nil)
		}
		links, err := loadLinkOuts(ctx, cfg.DB, issue.ID)
		if err != nil {
			return nil, api.NewError(500, "internal", err.Error(), "", nil)
		}
		labels, err := cfg.DB.LabelsByIssue(ctx, issue.ID)
		if err != nil {
			return nil, api.NewError(500, "internal", err.Error(), "", nil)
		}
		out := &api.ShowIssueResponse{}
		out.Body.Issue = issue
		out.Body.Comments = comments
		out.Body.Links = links
		out.Body.Labels = labels
		return out, nil
	})

	huma.Register(humaAPI, huma.Operation{
		OperationID: "editIssue",
		Method:      "PATCH",
		Path:        "/api/v1/projects/{project_id}/issues/{number}",
	}, func(ctx context.Context, in *api.EditIssueRequest) (*api.MutationResponse, error) {
		if err := validateActor(in.Body.Actor); err != nil {
			return nil, err
		}
		issue, err := cfg.DB.IssueByNumber(ctx, in.ProjectID, in.Number)
		if errors.Is(err, db.ErrNotFound) {
			return nil, api.NewError(404, "issue_not_found", "issue not found", "", nil)
		}
		if err != nil {
			return nil, api.NewError(500, "internal", err.Error(), "", nil)
		}
		updated, evt, changed, err := cfg.DB.EditIssue(ctx, db.EditIssueParams{
			IssueID: issue.ID,
			Title:   in.Body.Title,
			Body:    in.Body.Body,
			Owner:   in.Body.Owner,
			Actor:   in.Body.Actor,
		})
		if errors.Is(err, db.ErrNoFields) {
			return nil, api.NewError(400, "validation", "no fields to update", "pass at least one of title, body, owner", nil)
		}
		if err != nil {
			return nil, api.NewError(500, "internal", err.Error(), "", nil)
		}
		out := &api.MutationResponse{}
		out.Body.Issue = updated
		out.Body.Event = evt
		out.Body.Changed = changed
		return out, nil
	})
}

// loadLinkOuts fetches every link involving issueID, resolving both endpoint
// numbers so the wire response speaks the agent-facing surface (numbers, not
// internal ids). One IssueByID call per endpoint is fine for show; pagination
// is a Plan 4 concern.
func loadLinkOuts(ctx context.Context, store *db.DB, issueID int64) ([]api.LinkOut, error) {
	rows, err := store.LinksByIssue(ctx, issueID)
	if err != nil {
		return nil, err
	}
	out := make([]api.LinkOut, 0, len(rows))
	for _, l := range rows {
		from, err := store.IssueByID(ctx, l.FromIssueID)
		if err != nil {
			return nil, err
		}
		to, err := store.IssueByID(ctx, l.ToIssueID)
		if err != nil {
			return nil, err
		}
		out = append(out, api.LinkOut{
			ID:         l.ID,
			ProjectID:  l.ProjectID,
			FromNumber: from.Number,
			ToNumber:   to.Number,
			Type:       l.Type,
			Author:     l.Author,
			CreatedAt:  l.CreatedAt,
		})
	}
	return out, nil
}

// listComments fetches every comment attached to issueID in chronological
// order. Plan 1 ships no pagination; the show handler embeds the full slice.
func listComments(ctx context.Context, store *db.DB, issueID int64) ([]db.Comment, error) {
	rows, err := store.QueryContext(ctx,
		`SELECT id, issue_id, author, body, created_at FROM comments WHERE issue_id = ? ORDER BY created_at ASC, id ASC`, issueID)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	var out []db.Comment
	for rows.Next() {
		var c db.Comment
		if err := rows.Scan(&c.ID, &c.IssueID, &c.Author, &c.Body, &c.CreatedAt); err != nil {
			return nil, err
		}
		out = append(out, c)
	}
	return out, rows.Err()
}
