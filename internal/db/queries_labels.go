package db

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
)

// ErrLabelExists is returned when (issue_id, label) already exists.
// Caller treats this as a no-op success on duplicate labels.
var ErrLabelExists = errors.New("label already attached")

// ErrLabelInvalid is returned when the label fails the schema's charset/length
// CHECK constraint.
var ErrLabelInvalid = errors.New("invalid label")

// AddLabel attaches a label to an issue.
func (d *DB) AddLabel(ctx context.Context, issueID int64, label, author string) (IssueLabel, error) {
	res, err := d.ExecContext(ctx,
		`INSERT INTO issue_labels(issue_id, label, author) VALUES(?, ?, ?)`,
		issueID, label, author)
	if err != nil {
		return IssueLabel{}, classifyLabelInsertError(err)
	}
	_ = res
	row := d.QueryRowContext(ctx,
		`SELECT issue_id, label, author, created_at FROM issue_labels
		 WHERE issue_id = ? AND label = ?`, issueID, label)
	var out IssueLabel
	if err := row.Scan(&out.IssueID, &out.Label, &out.Author, &out.CreatedAt); err != nil {
		return IssueLabel{}, fmt.Errorf("re-fetch label: %w", err)
	}
	return out, nil
}

func classifyLabelInsertError(err error) error {
	msg := err.Error()
	switch {
	case strings.Contains(msg, "UNIQUE constraint failed: issue_labels.issue_id, issue_labels.label"):
		return ErrLabelExists
	case strings.Contains(msg, "CHECK constraint failed"):
		// Either the GLOB charset or the length BETWEEN check.
		return ErrLabelInvalid
	}
	return fmt.Errorf("insert label: %w", err)
}

// RemoveLabel detaches a label from an issue. Returns ErrNotFound when the row
// doesn't exist (idempotent unlink semantics live in the handler).
func (d *DB) RemoveLabel(ctx context.Context, issueID int64, label string) error {
	res, err := d.ExecContext(ctx,
		`DELETE FROM issue_labels WHERE issue_id = ? AND label = ?`,
		issueID, label)
	if err != nil {
		return fmt.Errorf("delete label: %w", err)
	}
	n, err := res.RowsAffected()
	if err != nil {
		return fmt.Errorf("delete label rows affected: %w", err)
	}
	if n == 0 {
		return ErrNotFound
	}
	return nil
}

// HasLabel reports whether (issueID, label) exists.
func (d *DB) HasLabel(ctx context.Context, issueID int64, label string) (bool, error) {
	var n int
	err := d.QueryRowContext(ctx,
		`SELECT 1 FROM issue_labels WHERE issue_id = ? AND label = ?`,
		issueID, label).Scan(&n)
	if errors.Is(err, sql.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, fmt.Errorf("has label: %w", err)
	}
	return n == 1, nil
}

// LabelsByIssue returns every label attached to issueID, ordered alphabetically.
func (d *DB) LabelsByIssue(ctx context.Context, issueID int64) ([]IssueLabel, error) {
	rows, err := d.QueryContext(ctx,
		`SELECT issue_id, label, author, created_at FROM issue_labels
		 WHERE issue_id = ? ORDER BY label ASC`, issueID)
	if err != nil {
		return nil, fmt.Errorf("list labels: %w", err)
	}
	defer func() { _ = rows.Close() }()
	var out []IssueLabel
	for rows.Next() {
		var l IssueLabel
		if err := rows.Scan(&l.IssueID, &l.Label, &l.Author, &l.CreatedAt); err != nil {
			return nil, err
		}
		out = append(out, l)
	}
	return out, rows.Err()
}

// LabelCounts returns the per-label aggregate for projectID, excluding
// soft-deleted issues.
func (d *DB) LabelCounts(ctx context.Context, projectID int64) ([]LabelCount, error) {
	rows, err := d.QueryContext(ctx,
		`SELECT il.label, COUNT(*) AS n
		 FROM issue_labels il
		 JOIN issues i ON i.id = il.issue_id
		 WHERE i.project_id = ? AND i.deleted_at IS NULL
		 GROUP BY il.label
		 ORDER BY n DESC, il.label ASC`, projectID)
	if err != nil {
		return nil, fmt.Errorf("label counts: %w", err)
	}
	defer func() { _ = rows.Close() }()
	var out []LabelCount
	for rows.Next() {
		var c LabelCount
		if err := rows.Scan(&c.Label, &c.Count); err != nil {
			return nil, err
		}
		out = append(out, c)
	}
	return out, rows.Err()
}
