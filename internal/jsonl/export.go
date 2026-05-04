package jsonl

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"io"

	"github.com/wesm/kata/internal/db"
)

// ExportOptions controls which rows are exported.
type ExportOptions struct {
	ProjectID      int64
	IncludeDeleted bool
}

// Export writes a deterministic JSONL export of d to w.
func Export(ctx context.Context, d *db.DB, w io.Writer, _ ExportOptions) error {
	enc := NewEncoder(w)

	version, err := schemaVersion(ctx, d)
	if err != nil {
		return err
	}
	if err := writeRecord(enc, KindMeta, metaRecord{Key: "export_version", Value: version}); err != nil {
		return err
	}
	if err := exportMeta(ctx, d, enc); err != nil {
		return err
	}
	if err := exportProjects(ctx, d, enc); err != nil {
		return err
	}
	if err := exportProjectAliases(ctx, d, enc); err != nil {
		return err
	}
	if err := exportIssues(ctx, d, enc); err != nil {
		return err
	}
	if err := exportComments(ctx, d, enc); err != nil {
		return err
	}
	if err := exportIssueLabels(ctx, d, enc); err != nil {
		return err
	}
	if err := exportLinks(ctx, d, enc); err != nil {
		return err
	}
	if err := exportEvents(ctx, d, enc); err != nil {
		return err
	}
	if err := exportPurgeLog(ctx, d, enc); err != nil {
		return err
	}
	if err := exportSQLiteSequence(ctx, d, enc); err != nil {
		return err
	}
	return nil
}

type metaRecord struct {
	Key   string `json:"key"`
	Value string `json:"value"`
}

func schemaVersion(ctx context.Context, d *db.DB) (string, error) {
	var version string
	if err := d.QueryRowContext(ctx, `SELECT value FROM meta WHERE key='schema_version'`).Scan(&version); err != nil {
		return "", fmt.Errorf("read schema_version: %w", err)
	}
	return version, nil
}

func exportMeta(ctx context.Context, d *db.DB, enc *Encoder) error {
	rows, err := d.QueryContext(ctx, `SELECT key, value FROM meta ORDER BY key ASC`)
	if err != nil {
		return fmt.Errorf("export meta: %w", err)
	}
	defer func() { _ = rows.Close() }()
	for rows.Next() {
		var rec metaRecord
		if err := rows.Scan(&rec.Key, &rec.Value); err != nil {
			return fmt.Errorf("scan meta: %w", err)
		}
		if err := writeRecord(enc, KindMeta, rec); err != nil {
			return err
		}
	}
	return rows.Err()
}

func exportProjects(ctx context.Context, d *db.DB, enc *Encoder) error {
	type record struct {
		ID              int64  `json:"id"`
		Identity        string `json:"identity"`
		Name            string `json:"name"`
		CreatedAt       string `json:"created_at"`
		NextIssueNumber int64  `json:"next_issue_number"`
	}
	rows, err := d.QueryContext(ctx,
		`SELECT id, identity, name, CAST(created_at AS TEXT), next_issue_number FROM projects ORDER BY id ASC`)
	if err != nil {
		return fmt.Errorf("export projects: %w", err)
	}
	return scanRecords(rows, KindProject, enc, func(rows *sql.Rows) (record, error) {
		var rec record
		err := rows.Scan(&rec.ID, &rec.Identity, &rec.Name, &rec.CreatedAt, &rec.NextIssueNumber)
		return rec, err
	})
}

func exportProjectAliases(ctx context.Context, d *db.DB, enc *Encoder) error {
	type record struct {
		ID            int64  `json:"id"`
		ProjectID     int64  `json:"project_id"`
		AliasIdentity string `json:"alias_identity"`
		AliasKind     string `json:"alias_kind"`
		RootPath      string `json:"root_path"`
		CreatedAt     string `json:"created_at"`
		LastSeenAt    string `json:"last_seen_at"`
	}
	rows, err := d.QueryContext(ctx,
		`SELECT id, project_id, alias_identity, alias_kind, root_path,
		        CAST(created_at AS TEXT), CAST(last_seen_at AS TEXT)
		 FROM project_aliases ORDER BY id ASC`)
	if err != nil {
		return fmt.Errorf("export project_aliases: %w", err)
	}
	return scanRecords(rows, KindProjectAlias, enc, func(rows *sql.Rows) (record, error) {
		var rec record
		err := rows.Scan(&rec.ID, &rec.ProjectID, &rec.AliasIdentity, &rec.AliasKind,
			&rec.RootPath, &rec.CreatedAt, &rec.LastSeenAt)
		return rec, err
	})
}

func exportIssues(ctx context.Context, d *db.DB, enc *Encoder) error {
	type record struct {
		ID           int64   `json:"id"`
		ProjectID    int64   `json:"project_id"`
		Number       int64   `json:"number"`
		Title        string  `json:"title"`
		Body         string  `json:"body"`
		Status       string  `json:"status"`
		ClosedReason *string `json:"closed_reason"`
		Owner        *string `json:"owner"`
		Author       string  `json:"author"`
		CreatedAt    string  `json:"created_at"`
		UpdatedAt    string  `json:"updated_at"`
		ClosedAt     *string `json:"closed_at"`
		DeletedAt    *string `json:"deleted_at"`
	}
	rows, err := d.QueryContext(ctx,
		`SELECT id, project_id, number, title, body, status, closed_reason, owner, author,
		        CAST(created_at AS TEXT), CAST(updated_at AS TEXT),
		        CAST(closed_at AS TEXT), CAST(deleted_at AS TEXT)
		 FROM issues ORDER BY id ASC`)
	if err != nil {
		return fmt.Errorf("export issues: %w", err)
	}
	return scanRecords(rows, KindIssue, enc, func(rows *sql.Rows) (record, error) {
		var rec record
		err := rows.Scan(&rec.ID, &rec.ProjectID, &rec.Number, &rec.Title, &rec.Body,
			&rec.Status, &rec.ClosedReason, &rec.Owner, &rec.Author, &rec.CreatedAt,
			&rec.UpdatedAt, &rec.ClosedAt, &rec.DeletedAt)
		return rec, err
	})
}

func exportComments(ctx context.Context, d *db.DB, enc *Encoder) error {
	type record struct {
		ID        int64  `json:"id"`
		IssueID   int64  `json:"issue_id"`
		Author    string `json:"author"`
		Body      string `json:"body"`
		CreatedAt string `json:"created_at"`
	}
	rows, err := d.QueryContext(ctx,
		`SELECT id, issue_id, author, body, CAST(created_at AS TEXT) FROM comments ORDER BY id ASC`)
	if err != nil {
		return fmt.Errorf("export comments: %w", err)
	}
	return scanRecords(rows, KindComment, enc, func(rows *sql.Rows) (record, error) {
		var rec record
		err := rows.Scan(&rec.ID, &rec.IssueID, &rec.Author, &rec.Body, &rec.CreatedAt)
		return rec, err
	})
}

func exportIssueLabels(ctx context.Context, d *db.DB, enc *Encoder) error {
	type record struct {
		IssueID   int64  `json:"issue_id"`
		Label     string `json:"label"`
		Author    string `json:"author"`
		CreatedAt string `json:"created_at"`
	}
	rows, err := d.QueryContext(ctx,
		`SELECT issue_id, label, author, CAST(created_at AS TEXT)
		 FROM issue_labels ORDER BY issue_id ASC, label ASC`)
	if err != nil {
		return fmt.Errorf("export issue_labels: %w", err)
	}
	return scanRecords(rows, KindIssueLabel, enc, func(rows *sql.Rows) (record, error) {
		var rec record
		err := rows.Scan(&rec.IssueID, &rec.Label, &rec.Author, &rec.CreatedAt)
		return rec, err
	})
}

func exportLinks(ctx context.Context, d *db.DB, enc *Encoder) error {
	type record struct {
		ID          int64  `json:"id"`
		ProjectID   int64  `json:"project_id"`
		FromIssueID int64  `json:"from_issue_id"`
		ToIssueID   int64  `json:"to_issue_id"`
		Type        string `json:"type"`
		Author      string `json:"author"`
		CreatedAt   string `json:"created_at"`
	}
	rows, err := d.QueryContext(ctx,
		`SELECT id, project_id, from_issue_id, to_issue_id, type, author, CAST(created_at AS TEXT)
		 FROM links ORDER BY id ASC`)
	if err != nil {
		return fmt.Errorf("export links: %w", err)
	}
	return scanRecords(rows, KindLink, enc, func(rows *sql.Rows) (record, error) {
		var rec record
		err := rows.Scan(&rec.ID, &rec.ProjectID, &rec.FromIssueID, &rec.ToIssueID,
			&rec.Type, &rec.Author, &rec.CreatedAt)
		return rec, err
	})
}

func exportEvents(ctx context.Context, d *db.DB, enc *Encoder) error {
	type record struct {
		ID              int64           `json:"id"`
		ProjectID       int64           `json:"project_id"`
		ProjectIdentity string          `json:"project_identity"`
		IssueID         *int64          `json:"issue_id"`
		IssueNumber     *int64          `json:"issue_number"`
		RelatedIssueID  *int64          `json:"related_issue_id"`
		Type            string          `json:"type"`
		Actor           string          `json:"actor"`
		Payload         json.RawMessage `json:"payload"`
		CreatedAt       string          `json:"created_at"`
	}
	rows, err := d.QueryContext(ctx,
		`SELECT id, project_id, project_identity, issue_id, issue_number, related_issue_id,
		        type, actor, payload, CAST(created_at AS TEXT)
		 FROM events ORDER BY id ASC`)
	if err != nil {
		return fmt.Errorf("export events: %w", err)
	}
	return scanRecords(rows, KindEvent, enc, func(rows *sql.Rows) (record, error) {
		var rec record
		var payload string
		err := rows.Scan(&rec.ID, &rec.ProjectID, &rec.ProjectIdentity, &rec.IssueID,
			&rec.IssueNumber, &rec.RelatedIssueID, &rec.Type, &rec.Actor, &payload, &rec.CreatedAt)
		if err != nil {
			return rec, err
		}
		if !json.Valid([]byte(payload)) {
			return rec, fmt.Errorf("event %d payload is invalid JSON", rec.ID)
		}
		rec.Payload = json.RawMessage(payload)
		return rec, nil
	})
}

func exportPurgeLog(ctx context.Context, d *db.DB, enc *Encoder) error {
	type record struct {
		ID                     int64   `json:"id"`
		ProjectID              int64   `json:"project_id"`
		PurgedIssueID          int64   `json:"purged_issue_id"`
		ProjectIdentity        string  `json:"project_identity"`
		IssueNumber            int64   `json:"issue_number"`
		IssueTitle             string  `json:"issue_title"`
		IssueAuthor            string  `json:"issue_author"`
		CommentCount           int64   `json:"comment_count"`
		LinkCount              int64   `json:"link_count"`
		LabelCount             int64   `json:"label_count"`
		EventCount             int64   `json:"event_count"`
		EventsDeletedMinID     *int64  `json:"events_deleted_min_id"`
		EventsDeletedMaxID     *int64  `json:"events_deleted_max_id"`
		PurgeResetAfterEventID *int64  `json:"purge_reset_after_event_id"`
		Actor                  string  `json:"actor"`
		Reason                 *string `json:"reason"`
		PurgedAt               string  `json:"purged_at"`
	}
	rows, err := d.QueryContext(ctx,
		`SELECT id, project_id, purged_issue_id, project_identity, issue_number, issue_title,
		        issue_author, comment_count, link_count, label_count, event_count,
		        events_deleted_min_id, events_deleted_max_id, purge_reset_after_event_id,
		        actor, reason, CAST(purged_at AS TEXT)
		 FROM purge_log ORDER BY id ASC`)
	if err != nil {
		return fmt.Errorf("export purge_log: %w", err)
	}
	return scanRecords(rows, KindPurgeLog, enc, func(rows *sql.Rows) (record, error) {
		var rec record
		err := rows.Scan(&rec.ID, &rec.ProjectID, &rec.PurgedIssueID, &rec.ProjectIdentity,
			&rec.IssueNumber, &rec.IssueTitle, &rec.IssueAuthor, &rec.CommentCount,
			&rec.LinkCount, &rec.LabelCount, &rec.EventCount, &rec.EventsDeletedMinID,
			&rec.EventsDeletedMaxID, &rec.PurgeResetAfterEventID, &rec.Actor, &rec.Reason,
			&rec.PurgedAt)
		return rec, err
	})
}

func exportSQLiteSequence(ctx context.Context, d *db.DB, enc *Encoder) error {
	type record struct {
		Name string `json:"name"`
		Seq  int64  `json:"seq"`
	}
	rows, err := d.QueryContext(ctx, `SELECT name, seq FROM sqlite_sequence ORDER BY name ASC`)
	if err != nil {
		return fmt.Errorf("export sqlite_sequence: %w", err)
	}
	return scanRecords(rows, KindSQLiteSequence, enc, func(rows *sql.Rows) (record, error) {
		var rec record
		err := rows.Scan(&rec.Name, &rec.Seq)
		return rec, err
	})
}

func scanRecords[T any](rows *sql.Rows, kind Kind, enc *Encoder, scan func(*sql.Rows) (T, error)) error {
	defer func() { _ = rows.Close() }()
	for rows.Next() {
		rec, err := scan(rows)
		if err != nil {
			return fmt.Errorf("scan %s: %w", kind, err)
		}
		if err := writeRecord(enc, kind, rec); err != nil {
			return err
		}
	}
	return rows.Err()
}

func writeRecord(enc *Encoder, kind Kind, data any) error {
	bs, err := json.Marshal(data)
	if err != nil {
		return fmt.Errorf("marshal %s: %w", kind, err)
	}
	if err := enc.Write(Envelope{Kind: kind, Data: bs}); err != nil {
		return fmt.Errorf("write %s: %w", kind, err)
	}
	return nil
}
