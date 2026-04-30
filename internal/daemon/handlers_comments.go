package daemon

import (
	"context"
	"errors"

	"github.com/danielgtaylor/huma/v2"

	"github.com/wesm/kata/internal/api"
	"github.com/wesm/kata/internal/db"
)

// registerCommentsHandlers installs POST /comments. CreateComment writes the
// comment row and an issue.commented event in one tx; we re-read the issue via
// IssueByID to surface the freshly-bumped updated_at in the response envelope.
func registerCommentsHandlers(humaAPI huma.API, cfg ServerConfig) {
	huma.Register(humaAPI, huma.Operation{
		OperationID: "createComment",
		Method:      "POST",
		Path:        "/api/v1/projects/{project_id}/issues/{number}/comments",
	}, func(ctx context.Context, in *api.CommentRequest) (*api.CommentResponse, error) {
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
		c, evt, err := cfg.DB.CreateComment(ctx, db.CreateCommentParams{
			IssueID: issue.ID,
			Author:  in.Body.Actor,
			Body:    in.Body.Body,
		})
		if err != nil {
			return nil, api.NewError(500, "internal", err.Error(), "", nil)
		}
		cfg.Broadcaster.Broadcast(StreamMsg{Kind: "event", Event: &evt, ProjectID: in.ProjectID})
		updated, err := cfg.DB.IssueByID(ctx, issue.ID)
		if err != nil {
			return nil, api.NewError(500, "internal", err.Error(), "", nil)
		}
		out := &api.CommentResponse{}
		out.Body.Issue = updated
		out.Body.Comment = c
		out.Body.Event = &evt
		out.Body.Changed = true
		return out, nil
	})
}
