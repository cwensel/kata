package daemon

import (
	"context"
	"errors"
	"fmt"

	"github.com/danielgtaylor/huma/v2"

	"github.com/wesm/kata/internal/api"
	"github.com/wesm/kata/internal/db"
)

// registerLinksHandlers installs POST/DELETE /links. CreateLinkAndEvent and
// DeleteLinkAndEvent wrap the link mutation, the matching issue.linked /
// issue.unlinked event, and the issues.updated_at touch in one TX so there's
// no window where the row mutation lands without its event.
//
// For type=parent --replace, the handler emits an issue.unlinked event for
// the old parent (in its own TX) before inserting the new parent link with
// its issue.linked event. The response shape carries only the linked event;
// the unlinked event still lands in the events table for SSE/poll clients.
func registerLinksHandlers(humaAPI huma.API, cfg ServerConfig) {
	huma.Register(humaAPI, huma.Operation{
		OperationID: "createLink",
		Method:      "POST",
		Path:        "/api/v1/projects/{project_id}/issues/{number}/links",
	}, createLinkHandler(cfg))

	huma.Register(humaAPI, huma.Operation{
		OperationID: "deleteLink",
		Method:      "DELETE",
		Path:        "/api/v1/projects/{project_id}/issues/{number}/links/{link_id}",
	}, deleteLinkHandler(cfg))
}

func createLinkHandler(cfg ServerConfig) func(context.Context, *api.CreateLinkRequest) (*api.CreateLinkResponse, error) {
	return func(ctx context.Context, in *api.CreateLinkRequest) (*api.CreateLinkResponse, error) {
		from, err := cfg.DB.IssueByNumber(ctx, in.ProjectID, in.Number)
		if errors.Is(err, db.ErrNotFound) {
			return nil, api.NewError(404, "issue_not_found", "issue not found", "", nil)
		}
		if err != nil {
			return nil, api.NewError(500, "internal", err.Error(), "", nil)
		}
		to, err := cfg.DB.IssueByNumber(ctx, in.ProjectID, in.Body.ToNumber)
		if errors.Is(err, db.ErrNotFound) {
			return nil, api.NewError(404, "issue_not_found",
				fmt.Sprintf("target issue #%d not found", in.Body.ToNumber), "", nil)
		}
		if err != nil {
			return nil, api.NewError(500, "internal", err.Error(), "", nil)
		}

		fromID, toID := from.ID, to.ID
		fromNum, toNum := from.Number, to.Number
		if in.Body.Type == "related" && fromID > toID {
			fromID, toID = toID, fromID
			fromNum, toNum = toNum, fromNum
		}

		// Parent --replace path: delete the existing parent link in its own TX
		// (emitting issue.unlinked) before inserting the new parent link.
		if in.Body.Type == "parent" && in.Body.Replace {
			if existing, perr := cfg.DB.ParentOf(ctx, fromID); perr == nil {
				if existing.ToIssueID == toID {
					// Replacing with the same parent is a no-op.
					return mutationLinkResponse(from, existing, fromNum, toNum, nil, false), nil
				}
				// Resolve the OLD parent's number so the issue.unlinked event
				// payload's to_number records the parent we're actually
				// removing — not the new parent we're about to insert.
				oldParentIssue, err := cfg.DB.IssueByID(ctx, existing.ToIssueID)
				if err != nil {
					return nil, api.NewError(500, "internal", err.Error(), "", nil)
				}
				if _, err := cfg.DB.DeleteLinkAndEvent(ctx, existing.ID, fromID, fromNum,
					fromNum, oldParentIssue.Number, existing.Type, existing, in.Body.Actor); err != nil {
					return nil, api.NewError(500, "internal", err.Error(), "", nil)
				}
			} else if !errors.Is(perr, db.ErrNotFound) {
				return nil, api.NewError(500, "internal", perr.Error(), "", nil)
			}
		}

		// Default path: insert link + emit issue.linked + touch updated_at, all
		// in one TX. Distinct error types map to specific responses.
		link, evt, err := cfg.DB.CreateLinkAndEvent(ctx, db.CreateLinkParams{
			ProjectID:   in.ProjectID,
			FromIssueID: fromID,
			ToIssueID:   toID,
			Type:        in.Body.Type,
			Author:      in.Body.Actor,
		}, "issue.linked", fromID, fromNum, toNum, in.Body.Actor)
		switch {
		case errors.Is(err, db.ErrLinkExists):
			// Duplicate (from, to, type) → no-op. Re-fetch and return existing.
			existing, lookupErr := cfg.DB.LinkByEndpoints(ctx, fromID, toID, in.Body.Type)
			if lookupErr != nil {
				return nil, api.NewError(500, "internal", lookupErr.Error(), "", nil)
			}
			return mutationLinkResponse(from, existing, fromNum, toNum, nil, false), nil
		case errors.Is(err, db.ErrParentAlreadySet):
			return nil, api.NewError(409, "parent_already_set",
				"this issue already has a parent", "pass replace=true to swap", nil)
		case errors.Is(err, db.ErrSelfLink):
			return nil, api.NewError(400, "validation", "cannot link an issue to itself", "", nil)
		case errors.Is(err, db.ErrCrossProjectLink):
			return nil, api.NewError(400, "validation", "cross-project links are not allowed", "", nil)
		case err != nil:
			return nil, api.NewError(500, "internal", err.Error(), "", nil)
		}

		updatedIssue, err := cfg.DB.IssueByID(ctx, from.ID)
		if err != nil {
			return nil, api.NewError(500, "internal", err.Error(), "", nil)
		}
		return mutationLinkResponse(updatedIssue, link, fromNum, toNum, &evt, true), nil
	}
}

func deleteLinkHandler(cfg ServerConfig) func(context.Context, *api.DeleteLinkRequest) (*api.MutationResponse, error) {
	return func(ctx context.Context, in *api.DeleteLinkRequest) (*api.MutationResponse, error) {
		from, err := cfg.DB.IssueByNumber(ctx, in.ProjectID, in.Number)
		if errors.Is(err, db.ErrNotFound) {
			return nil, api.NewError(404, "issue_not_found", "issue not found", "", nil)
		}
		if err != nil {
			return nil, api.NewError(500, "internal", err.Error(), "", nil)
		}

		link, err := cfg.DB.LinkByID(ctx, in.LinkID)
		if errors.Is(err, db.ErrNotFound) {
			// Idempotent: no row → no-op envelope.
			out := &api.MutationResponse{}
			out.Body.Issue = from
			out.Body.Event = nil
			out.Body.Changed = false
			return out, nil
		}
		if err != nil {
			return nil, api.NewError(500, "internal", err.Error(), "", nil)
		}
		if link.ProjectID != in.ProjectID {
			return nil, api.NewError(404, "link_not_found", "link not in this project", "", nil)
		}

		// Resolve numbers for the event payload before deleting.
		fromIssue, err := cfg.DB.IssueByID(ctx, link.FromIssueID)
		if err != nil {
			return nil, api.NewError(500, "internal", err.Error(), "", nil)
		}
		toIssue, err := cfg.DB.IssueByID(ctx, link.ToIssueID)
		if err != nil {
			return nil, api.NewError(500, "internal", err.Error(), "", nil)
		}

		evt, err := cfg.DB.DeleteLinkAndEvent(ctx, in.LinkID, from.ID, from.Number,
			fromIssue.Number, toIssue.Number, link.Type, link, in.Actor)
		if errors.Is(err, db.ErrNotFound) {
			// Lost the race against another DELETE; surface as no-op.
			out := &api.MutationResponse{}
			out.Body.Issue = from
			out.Body.Event = nil
			out.Body.Changed = false
			return out, nil
		}
		if err != nil {
			return nil, api.NewError(500, "internal", err.Error(), "", nil)
		}
		updatedIssue, err := cfg.DB.IssueByID(ctx, from.ID)
		if err != nil {
			return nil, api.NewError(500, "internal", err.Error(), "", nil)
		}
		out := &api.MutationResponse{}
		out.Body.Issue = updatedIssue
		out.Body.Event = &evt
		out.Body.Changed = true
		return out, nil
	}
}

// mutationLinkResponse assembles a CreateLinkResponse from the source issue,
// the link row, the canonical endpoint numbers, an optional event, and the
// changed flag. Used for both fresh inserts (event != nil, changed=true) and
// no-op envelopes (event == nil, changed=false).
func mutationLinkResponse(issue db.Issue, link db.Link, fromNum, toNum int64, evt *db.Event, changed bool) *api.CreateLinkResponse {
	out := &api.CreateLinkResponse{}
	out.Body.Issue = issue
	out.Body.Link = api.LinkOut{
		ID:         link.ID,
		ProjectID:  link.ProjectID,
		FromNumber: fromNum,
		ToNumber:   toNum,
		Type:       link.Type,
		Author:     link.Author,
		CreatedAt:  link.CreatedAt,
	}
	out.Body.Event = evt
	out.Body.Changed = changed
	return out
}
