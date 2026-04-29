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
		if _, err := cfg.DB.ProjectByID(ctx, in.ProjectID); err != nil {
			if errors.Is(err, db.ErrNotFound) {
				return nil, api.NewError(404, "project_not_found", "project not found", "", nil)
			}
			return nil, api.NewError(500, "internal", err.Error(), "", nil)
		}
		issue, evt, err := cfg.DB.CreateIssue(ctx, db.CreateIssueParams{
			ProjectID: in.ProjectID,
			Title:     in.Body.Title,
			Body:      in.Body.Body,
			Author:    in.Body.Actor,
		})
		if err != nil {
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
		out := &api.ShowIssueResponse{}
		out.Body.Issue = issue
		out.Body.Comments = comments
		return out, nil
	})

	huma.Register(humaAPI, huma.Operation{
		OperationID: "editIssue",
		Method:      "PATCH",
		Path:        "/api/v1/projects/{project_id}/issues/{number}",
	}, func(ctx context.Context, in *api.EditIssueRequest) (*api.MutationResponse, error) {
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
