package db

import (
	"context"
	"database/sql"
	"fmt"
	"strings"
)

// MaxEventID returns the highest events.id, or 0 when the table is empty. The
// SSE handler uses this as the high-water mark snapshot after Subscribe.
func (d *DB) MaxEventID(ctx context.Context) (int64, error) {
	var n sql.NullInt64
	err := d.QueryRowContext(ctx, `SELECT MAX(id) FROM events`).Scan(&n)
	if err != nil {
		return 0, fmt.Errorf("max event id: %w", err)
	}
	if !n.Valid {
		return 0, nil
	}
	return n.Int64, nil
}

// EventsAfterParams selects events with id strictly greater than AfterID,
// optionally bounded above by ThroughID and filtered by ProjectID. Limit is
// applied verbatim; callers are responsible for clamping (the polling
// endpoint clamps to [1, 1000]; the SSE drain passes 10001).
type EventsAfterParams struct {
	AfterID   int64
	ProjectID int64 // 0 = cross-project; nonzero adds AND project_id = ?
	ThroughID int64 // 0 = no upper bound; nonzero adds AND id <= ?
	Limit     int
}

// EventsAfter returns up to Limit events ordered by id ASC.
func (d *DB) EventsAfter(ctx context.Context, p EventsAfterParams) ([]Event, error) {
	var (
		conds []string
		args  []any
	)
	conds = append(conds, "id > ?")
	args = append(args, p.AfterID)
	if p.ProjectID != 0 {
		conds = append(conds, "project_id = ?")
		args = append(args, p.ProjectID)
	}
	if p.ThroughID != 0 {
		conds = append(conds, "id <= ?")
		args = append(args, p.ThroughID)
	}
	q := `SELECT id, project_id, project_identity, issue_id, issue_number, related_issue_id,
	             type, actor, payload, created_at
	      FROM events WHERE ` + strings.Join(conds, " AND ") + ` ORDER BY id ASC LIMIT ?`
	args = append(args, p.Limit)
	rows, err := d.QueryContext(ctx, q, args...)
	if err != nil {
		return nil, fmt.Errorf("events after: %w", err)
	}
	defer func() { _ = rows.Close() }()
	var out []Event
	for rows.Next() {
		var e Event
		if err := rows.Scan(&e.ID, &e.ProjectID, &e.ProjectIdentity, &e.IssueID,
			&e.IssueNumber, &e.RelatedIssueID, &e.Type, &e.Actor, &e.Payload, &e.CreatedAt); err != nil {
			return nil, fmt.Errorf("scan event: %w", err)
		}
		out = append(out, e)
	}
	return out, rows.Err()
}

// PurgeResetCheck returns the maximum purge_reset_after_event_id strictly
// greater than afterID, optionally constrained to a project. Returns 0 when
// no matching purge_log row exists. The strict > semantics align with the
// spec §2.6 reservation: every reserved cursor is greater than every real
// events.id at the moment of the purge, so cursor == reservedID means the
// client is already past it and does not need a reset.
//
// projectID == 0 = cross-project (no filter).
func (d *DB) PurgeResetCheck(ctx context.Context, afterID, projectID int64) (int64, error) {
	q := `SELECT MAX(purge_reset_after_event_id) FROM purge_log
	      WHERE purge_reset_after_event_id IS NOT NULL AND purge_reset_after_event_id > ?`
	args := []any{afterID}
	if projectID != 0 {
		q += ` AND project_id = ?`
		args = append(args, projectID)
	}
	var n sql.NullInt64
	if err := d.QueryRowContext(ctx, q, args...).Scan(&n); err != nil {
		return 0, fmt.Errorf("purge reset check: %w", err)
	}
	if !n.Valid {
		return 0, nil
	}
	return n.Int64, nil
}
