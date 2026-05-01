package hooks

import (
	"context"
	"encoding/json"
	"strings"

	"github.com/wesm/kata/internal/db"
)

// Per-field byte caps from spec §7.2.
const (
	maxStdinBytes       = 256 * 1024
	maxIssueTitleBytes  = 1 * 1024
	maxIssueBodyBytes   = 8 * 1024
	maxCommentBodyBytes = 8 * 1024
)

// Resolver function types match DispatcherDeps in dispatcher.go (Task 8).
// They live here so payload.go has no compile-time dependency on the
// dispatcher package surface.
type (
	projectResolver func(context.Context, int64) (ProjectSnapshot, error)
	issueResolver   func(context.Context, int64) (IssueSnapshot, error)
	commentResolver func(context.Context, int64) (CommentSnapshot, error)
	aliasResolver   func(db.Event) (AliasSnapshot, bool, error)
	logfn           func(format string, args ...any)
)

// buildStdinJSON assembles the §7 envelope. Resolver errors degrade the
// payload (omit the affected block) but are non-fatal — every error is
// logged and the hook still runs. Returns (bytes, truncated) where
// truncated is true if total >256KB after per-field caps and we dropped
// optional fields to fit.
func buildStdinJSON(
	ctx context.Context,
	evt db.Event,
	rp projectResolver,
	ri issueResolver,
	rc commentResolver,
	ra aliasResolver,
	log logfn,
) ([]byte, bool) {
	env := map[string]any{
		"kata_hook_version": 1,
		"event_id":          evt.ID,
		"type":              evt.Type,
		"actor":             evt.Actor,
		"created_at":        evt.CreatedAt.UTC().Format("2006-01-02T15:04:05.000000000Z07:00"),
	}

	proj := map[string]any{"id": evt.ProjectID, "identity": evt.ProjectIdentity}
	if ps, err := rp(ctx, evt.ProjectID); err != nil {
		log("hooks: project resolver: %v", err)
	} else {
		proj["name"] = ps.Name
	}
	env["project"] = proj

	if asnap, has, err := ra(evt); err != nil {
		log("hooks: alias resolver: %v", err)
	} else if has {
		env["alias"] = map[string]any{
			"alias_identity": asnap.Identity,
			"alias_kind":     asnap.Kind,
			"root_path":      asnap.RootPath,
		}
	}

	if evt.IssueID != nil {
		if isnap, err := ri(ctx, *evt.IssueID); err != nil {
			log("hooks: issue resolver: %v", err)
		} else {
			block := map[string]any{
				"number": isnap.Number,
				"status": isnap.Status,
				"owner":  isnap.Owner,
				"author": isnap.Author,
				"labels": isnap.Labels,
			}
			truncStringField(block, "title", isnap.Title, maxIssueTitleBytes)
			env["issue"] = block
		}
	}

	payload := map[string]any{}
	if len(evt.Payload) > 0 {
		var raw map[string]any
		if err := json.Unmarshal([]byte(evt.Payload), &raw); err == nil {
			for k, v := range raw {
				payload[k] = v
			}
		}
	}
	if evt.Type == "issue.commented" {
		if cidF, ok := payload["comment_id"].(float64); ok {
			cid := int64(cidF)
			if csnap, err := rc(ctx, cid); err != nil {
				log("hooks: comment resolver: %v", err)
			} else {
				truncStringField(payload, "comment_body", csnap.Body, maxCommentBodyBytes)
			}
		}
	}
	if len(payload) > 0 {
		env["payload"] = payload
	}

	out, _ := json.Marshal(env)
	if len(out) <= maxStdinBytes {
		return out, false
	}

	env["payload_truncated"] = true
	for _, key := range []string{"payload", "issue.body", "issue.title"} {
		if dropOptional(env, key) {
			out, _ = json.Marshal(env)
			if len(out) <= maxStdinBytes {
				return out, true
			}
		}
	}
	return out, true
}

// truncStringField puts (possibly truncated) value at key, marking the
// parent block with _truncated/_full_size when the byte length exceeds
// limit.
func truncStringField(parent map[string]any, key, value string, limit int) {
	if len(value) <= limit {
		parent[key] = value
		return
	}
	parent[key] = value[:limit]
	parent["_truncated"] = true
	parent["_full_size"] = len(value)
}

// dropOptional removes the addressed field. Supports "payload" (top-level
// removal) and dotted keys like "issue.body" (sub-field removal). Returns
// true when the key existed and was removed.
func dropOptional(env map[string]any, addr string) bool {
	parts := strings.SplitN(addr, ".", 2)
	if len(parts) == 1 {
		if _, ok := env[parts[0]]; !ok {
			return false
		}
		delete(env, parts[0])
		return true
	}
	parent, ok := env[parts[0]].(map[string]any)
	if !ok {
		return false
	}
	if _, ok := parent[parts[1]]; !ok {
		return false
	}
	delete(parent, parts[1])
	return true
}
