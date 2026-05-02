package db

import (
	"context"
	"fmt"
	"strings"
)

// LabelsByIssues returns a map of issueID → []label for every issue in
// issueIDs that belongs to projectID. Labels per issue are sorted
// alphabetically; issues with no labels are absent from the map (callers
// should treat a missing key as "no labels"). Empty input short-circuits
// without a SQL roundtrip.
//
// Constrained by both project_id (via JOIN through issues) and id IN (...)
// for cross-project safety: a caller passing an issueID that belongs to a
// different project gets no rows for that ID rather than leaking labels
// across projects. The issue_labels table itself has no project_id
// column (see migrations/0001_init.sql) — projection has to go through
// issues.project_id.
//
// One backend round-trip per list page (vs N for per-issue LabelsByIssue
// calls) so the daemon's list handler stays cheap under high-issue-count
// workloads. The composite ORDER BY (issue_id ASC, label ASC) sorts on
// the primary key index of issue_labels and so should not require a
// filesort even on large projects; defer index-tuning to a follow-up if
// EXPLAIN QUERY PLAN reports otherwise.
func (d *DB) LabelsByIssues(
	ctx context.Context, projectID int64, issueIDs []int64,
) (map[int64][]string, error) {
	if len(issueIDs) == 0 {
		return map[int64][]string{}, nil
	}
	placeholders := make([]string, len(issueIDs))
	args := make([]interface{}, 0, len(issueIDs)+1)
	args = append(args, projectID)
	for i, id := range issueIDs {
		placeholders[i] = "?"
		args = append(args, id)
	}
	query := `SELECT il.issue_id, il.label
	          FROM issue_labels il
	          JOIN issues i ON i.id = il.issue_id
	          WHERE i.project_id = ?
	            AND il.issue_id IN (` + strings.Join(placeholders, ",") + `)
	          ORDER BY il.issue_id ASC, il.label ASC`
	rows, err := d.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, fmt.Errorf("labels by issues: %w", err)
	}
	defer func() { _ = rows.Close() }()
	out := map[int64][]string{}
	for rows.Next() {
		var (
			issueID int64
			label   string
		)
		if err := rows.Scan(&issueID, &label); err != nil {
			return nil, fmt.Errorf("scan labels by issues: %w", err)
		}
		out[issueID] = append(out[issueID], label)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate labels by issues: %w", err)
	}
	return out, nil
}
