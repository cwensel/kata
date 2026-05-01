package db

import (
	"context"
	"database/sql"
	"errors"
)

// CommentBodyByID returns the body of the comment with the given id.
// Used by the hooks dispatcher to resolve comment_body for
// issue.commented events at fire time.
func (d *DB) CommentBodyByID(ctx context.Context, id int64) (string, error) {
	var body string
	err := d.QueryRowContext(ctx, `SELECT body FROM comments WHERE id = ?`, id).Scan(&body)
	return body, err
}

// AliasRow is the slim view of a project alias the hook resolver needs.
// Field naming is normalized vs. ProjectAlias (which uses Alias-prefixed
// fields) so resolver call sites read cleanly.
type AliasRow struct {
	Identity string
	Kind     string
	RootPath string
}

// LatestAliasForProject returns the most-recently-seen alias for the
// project, if any. ok=false means the project has no aliases (the hook
// payload omits the alias block in that case).
func (d *DB) LatestAliasForProject(ctx context.Context, projectID int64) (AliasRow, bool, error) {
	var a AliasRow
	err := d.QueryRowContext(ctx,
		`SELECT alias_identity, alias_kind, root_path
		 FROM project_aliases WHERE project_id = ?
		 ORDER BY last_seen_at DESC LIMIT 1`, projectID).
		Scan(&a.Identity, &a.Kind, &a.RootPath)
	if errors.Is(err, sql.ErrNoRows) {
		return AliasRow{}, false, nil
	}
	if err != nil {
		return AliasRow{}, false, err
	}
	return a, true, nil
}

// LabelsForIssue returns sorted label values for the issue (alphabetical).
// Sorting is done in SQL so the result matches what the issue.created
// payload normalizes at insert time.
func (d *DB) LabelsForIssue(ctx context.Context, issueID int64) ([]string, error) {
	rows, err := d.QueryContext(ctx,
		`SELECT label FROM issue_labels WHERE issue_id = ? ORDER BY label ASC`, issueID)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	var out []string
	for rows.Next() {
		var v string
		if err := rows.Scan(&v); err != nil {
			return nil, err
		}
		out = append(out, v)
	}
	return out, rows.Err()
}
