package db

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
)

// SoftDeleteIssue sets deleted_at on the issue and emits issue.soft_deleted.
// Already-deleted issues are returned as a no-op envelope (nil event,
// changed=false). Unknown issues return ErrNotFound.
func (d *DB) SoftDeleteIssue(ctx context.Context, issueID int64, actor string) (Issue, *Event, bool, error) {
	tx, err := d.BeginTx(ctx, nil)
	if err != nil {
		return Issue{}, nil, false, err
	}
	defer func() { _ = tx.Rollback() }()

	issue, projectIdentity, err := lookupIssueIncludingDeleted(ctx, tx, issueID)
	if err != nil {
		return Issue{}, nil, false, err
	}
	if issue.DeletedAt != nil {
		// Already soft-deleted; commit so the read-side state is consistent
		// (no-op tx is harmless) and return the no-op envelope.
		if err := tx.Commit(); err != nil {
			return Issue{}, nil, false, err
		}
		return issue, nil, false, nil
	}
	// Conditional UPDATE — gated on deleted_at IS NULL — closes the
	// read-then-write race: a concurrent SoftDeleteIssue between our lookup
	// and our UPDATE would otherwise let both transactions emit events.
	res, err := tx.ExecContext(ctx,
		`UPDATE issues
		 SET deleted_at = strftime('%Y-%m-%dT%H:%M:%fZ','now'),
		     updated_at = strftime('%Y-%m-%dT%H:%M:%fZ','now')
		 WHERE id = ? AND deleted_at IS NULL`, issueID)
	if err != nil {
		return Issue{}, nil, false, fmt.Errorf("soft delete issue: %w", err)
	}
	n, err := res.RowsAffected()
	if err != nil {
		return Issue{}, nil, false, fmt.Errorf("soft delete rows affected: %w", err)
	}
	if n == 0 {
		// Lost the race — another tx soft-deleted this issue. No event.
		if err := tx.Commit(); err != nil {
			return Issue{}, nil, false, err
		}
		updated, err := d.IssueByID(ctx, issueID)
		if err != nil {
			return Issue{}, nil, false, err
		}
		return updated, nil, false, nil
	}
	evt, err := insertEventTx(ctx, tx, eventInsert{
		ProjectID:       issue.ProjectID,
		ProjectIdentity: projectIdentity,
		IssueID:         &issue.ID,
		IssueNumber:     &issue.Number,
		Type:            "issue.soft_deleted",
		Actor:           actor,
		Payload:         "{}",
	})
	if err != nil {
		return Issue{}, nil, false, err
	}
	if err := tx.Commit(); err != nil {
		return Issue{}, nil, false, err
	}
	updated, err := d.IssueByID(ctx, issueID)
	if err != nil {
		return Issue{}, nil, false, err
	}
	return updated, &evt, true, nil
}

// RestoreIssue clears deleted_at and emits issue.restored. Not-deleted issues
// are returned as a no-op envelope. Unknown issues return ErrNotFound.
func (d *DB) RestoreIssue(ctx context.Context, issueID int64, actor string) (Issue, *Event, bool, error) {
	tx, err := d.BeginTx(ctx, nil)
	if err != nil {
		return Issue{}, nil, false, err
	}
	defer func() { _ = tx.Rollback() }()

	issue, projectIdentity, err := lookupIssueIncludingDeleted(ctx, tx, issueID)
	if err != nil {
		return Issue{}, nil, false, err
	}
	if issue.DeletedAt == nil {
		if err := tx.Commit(); err != nil {
			return Issue{}, nil, false, err
		}
		return issue, nil, false, nil
	}
	// Conditional UPDATE — gated on deleted_at IS NOT NULL — closes the
	// read-then-write race symmetric to SoftDeleteIssue.
	res, err := tx.ExecContext(ctx,
		`UPDATE issues
		 SET deleted_at = NULL,
		     updated_at = strftime('%Y-%m-%dT%H:%M:%fZ','now')
		 WHERE id = ? AND deleted_at IS NOT NULL`, issueID)
	if err != nil {
		return Issue{}, nil, false, fmt.Errorf("restore issue: %w", err)
	}
	n, err := res.RowsAffected()
	if err != nil {
		return Issue{}, nil, false, fmt.Errorf("restore rows affected: %w", err)
	}
	if n == 0 {
		// Lost the race — another tx restored this issue. No event.
		if err := tx.Commit(); err != nil {
			return Issue{}, nil, false, err
		}
		updated, err := d.IssueByID(ctx, issueID)
		if err != nil {
			return Issue{}, nil, false, err
		}
		return updated, nil, false, nil
	}
	evt, err := insertEventTx(ctx, tx, eventInsert{
		ProjectID:       issue.ProjectID,
		ProjectIdentity: projectIdentity,
		IssueID:         &issue.ID,
		IssueNumber:     &issue.Number,
		Type:            "issue.restored",
		Actor:           actor,
		Payload:         "{}",
	})
	if err != nil {
		return Issue{}, nil, false, err
	}
	if err := tx.Commit(); err != nil {
		return Issue{}, nil, false, err
	}
	updated, err := d.IssueByID(ctx, issueID)
	if err != nil {
		return Issue{}, nil, false, err
	}
	return updated, &evt, true, nil
}

// lookupIssueIncludingDeleted fetches an issue + its project's identity for
// event snapshotting. Unlike lookupIssueForEvent (queries.go), this version
// does NOT filter out soft-deleted rows — it's the right primitive for the
// destructive ladder verbs that need to operate on deleted issues.
func lookupIssueIncludingDeleted(ctx context.Context, tx *sql.Tx, issueID int64) (Issue, string, error) {
	const q = `
		SELECT i.id, i.project_id, i.number, i.title, i.body, i.status,
		       i.closed_reason, i.owner, i.author, i.created_at, i.updated_at,
		       i.closed_at, i.deleted_at, p.identity
		FROM issues i
		JOIN projects p ON p.id = i.project_id
		WHERE i.id = ?`
	var (
		i        Issue
		identity string
	)
	err := tx.QueryRowContext(ctx, q, issueID).
		Scan(&i.ID, &i.ProjectID, &i.Number, &i.Title, &i.Body, &i.Status,
			&i.ClosedReason, &i.Owner, &i.Author, &i.CreatedAt, &i.UpdatedAt,
			&i.ClosedAt, &i.DeletedAt, &identity)
	if errors.Is(err, sql.ErrNoRows) {
		return Issue{}, "", ErrNotFound
	}
	if err != nil {
		return Issue{}, "", fmt.Errorf("lookup issue including deleted: %w", err)
	}
	return i, identity, nil
}
