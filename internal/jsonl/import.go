package jsonl

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"io"
	"strconv"
	"strings"

	"github.com/wesm/kata/internal/db"
)

// Import reads JSONL records from r and inserts them into target.
func Import(ctx context.Context, r io.Reader, target *db.DB) error {
	envs, err := NewDecoder(r).ReadAll(ctx)
	if err != nil {
		return err
	}
	if err := validateExportVersion(envs); err != nil {
		return err
	}
	tx, err := target.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin import: %w", err)
	}
	defer func() { _ = tx.Rollback() }()
	if _, err := tx.ExecContext(ctx, `PRAGMA defer_foreign_keys=ON`); err != nil {
		return fmt.Errorf("defer foreign keys: %w", err)
	}

	for _, env := range envs {
		if err := importEnvelope(ctx, tx, env); err != nil {
			return err
		}
	}
	if err := reconcileSequences(ctx, tx); err != nil {
		return err
	}
	if err := validateBeforeCommit(ctx, tx); err != nil {
		return err
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit import: %w", err)
	}
	return nil
}

func validateExportVersion(envs []Envelope) error {
	var rec metaRecord
	if err := decodeData(envs[0], &rec); err != nil {
		return err
	}
	version, err := strconv.Atoi(rec.Value)
	if err != nil {
		return fmt.Errorf("invalid export_version %q: %w", rec.Value, err)
	}
	if version > db.CurrentSchemaVersion() {
		return fmt.Errorf("unsupported export_version %d for current schema version %d", version, db.CurrentSchemaVersion())
	}
	if version < 1 {
		return fmt.Errorf("invalid export_version %d", version)
	}
	return nil
}

func importEnvelope(ctx context.Context, tx *sql.Tx, env Envelope) error {
	switch env.Kind {
	case KindMeta:
		var rec metaRecord
		if err := decodeData(env, &rec); err != nil {
			return err
		}
		if rec.Key == "export_version" {
			return nil
		}
		_, err := tx.ExecContext(ctx,
			`INSERT INTO meta(key, value) VALUES(?, ?)
			 ON CONFLICT(key) DO UPDATE SET value=excluded.value`,
			rec.Key, rec.Value)
		return wrapImportErr(env.Kind, err)
	case KindProject:
		var rec projectRecord
		if err := decodeData(env, &rec); err != nil {
			return err
		}
		_, err := tx.ExecContext(ctx,
			`INSERT INTO projects(id, identity, name, created_at, next_issue_number)
			 VALUES(?, ?, ?, ?, ?)`,
			rec.ID, rec.Identity, rec.Name, rec.CreatedAt, rec.NextIssueNumber)
		return wrapImportErr(env.Kind, err)
	case KindProjectAlias:
		var rec projectAliasRecord
		if err := decodeData(env, &rec); err != nil {
			return err
		}
		_, err := tx.ExecContext(ctx,
			`INSERT INTO project_aliases(id, project_id, alias_identity, alias_kind, root_path, created_at, last_seen_at)
			 VALUES(?, ?, ?, ?, ?, ?, ?)`,
			rec.ID, rec.ProjectID, rec.AliasIdentity, rec.AliasKind, rec.RootPath, rec.CreatedAt, rec.LastSeenAt)
		return wrapImportErr(env.Kind, err)
	case KindIssue:
		var rec issueRecord
		if err := decodeData(env, &rec); err != nil {
			return err
		}
		_, err := tx.ExecContext(ctx,
			`INSERT INTO issues(id, project_id, number, title, body, status, closed_reason, owner, author,
			                    created_at, updated_at, closed_at, deleted_at)
			 VALUES(?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
			rec.ID, rec.ProjectID, rec.Number, rec.Title, rec.Body, rec.Status, rec.ClosedReason,
			rec.Owner, rec.Author, rec.CreatedAt, rec.UpdatedAt, rec.ClosedAt, rec.DeletedAt)
		return wrapImportErr(env.Kind, err)
	case KindComment:
		var rec commentRecord
		if err := decodeData(env, &rec); err != nil {
			return err
		}
		_, err := tx.ExecContext(ctx,
			`INSERT INTO comments(id, issue_id, author, body, created_at) VALUES(?, ?, ?, ?, ?)`,
			rec.ID, rec.IssueID, rec.Author, rec.Body, rec.CreatedAt)
		return wrapImportErr(env.Kind, err)
	case KindIssueLabel:
		var rec issueLabelRecord
		if err := decodeData(env, &rec); err != nil {
			return err
		}
		_, err := tx.ExecContext(ctx,
			`INSERT INTO issue_labels(issue_id, label, author, created_at) VALUES(?, ?, ?, ?)`,
			rec.IssueID, rec.Label, rec.Author, rec.CreatedAt)
		return wrapImportErr(env.Kind, err)
	case KindLink:
		var rec linkRecord
		if err := decodeData(env, &rec); err != nil {
			return err
		}
		_, err := tx.ExecContext(ctx,
			`INSERT INTO links(id, project_id, from_issue_id, to_issue_id, type, author, created_at)
			 VALUES(?, ?, ?, ?, ?, ?, ?)`,
			rec.ID, rec.ProjectID, rec.FromIssueID, rec.ToIssueID, rec.Type, rec.Author, rec.CreatedAt)
		return wrapImportErr(env.Kind, err)
	case KindEvent:
		var rec eventRecord
		if err := decodeData(env, &rec); err != nil {
			return err
		}
		_, err := tx.ExecContext(ctx,
			`INSERT INTO events(id, project_id, project_identity, issue_id, issue_number, related_issue_id,
			                    type, actor, payload, created_at)
			 VALUES(?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
			rec.ID, rec.ProjectID, rec.ProjectIdentity, rec.IssueID, rec.IssueNumber,
			rec.RelatedIssueID, rec.Type, rec.Actor, string(rec.Payload), rec.CreatedAt)
		return wrapImportErr(env.Kind, err)
	case KindPurgeLog:
		var rec purgeLogRecord
		if err := decodeData(env, &rec); err != nil {
			return err
		}
		_, err := tx.ExecContext(ctx,
			`INSERT INTO purge_log(id, project_id, purged_issue_id, project_identity, issue_number, issue_title,
			                       issue_author, comment_count, link_count, label_count, event_count,
			                       events_deleted_min_id, events_deleted_max_id, purge_reset_after_event_id,
			                       actor, reason, purged_at)
			 VALUES(?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
			rec.ID, rec.ProjectID, rec.PurgedIssueID, rec.ProjectIdentity, rec.IssueNumber,
			rec.IssueTitle, rec.IssueAuthor, rec.CommentCount, rec.LinkCount, rec.LabelCount,
			rec.EventCount, rec.EventsDeletedMinID, rec.EventsDeletedMaxID, rec.PurgeResetAfterEventID,
			rec.Actor, rec.Reason, rec.PurgedAt)
		return wrapImportErr(env.Kind, err)
	case KindSQLiteSequence:
		var rec sqliteSequenceRecord
		if err := decodeData(env, &rec); err != nil {
			return err
		}
		return upsertSequence(ctx, tx, rec.Name, rec.Seq)
	default:
		return fmt.Errorf("import %s: unsupported kind", env.Kind)
	}
}

func decodeData(env Envelope, dst any) error {
	if err := json.Unmarshal(env.Data, dst); err != nil {
		return fmt.Errorf("decode %s data: %w", env.Kind, err)
	}
	return nil
}

func wrapImportErr(kind Kind, err error) error {
	if err != nil {
		return fmt.Errorf("import %s: %w", kind, err)
	}
	return nil
}

type projectRecord struct {
	ID              int64  `json:"id"`
	Identity        string `json:"identity"`
	Name            string `json:"name"`
	CreatedAt       string `json:"created_at"`
	NextIssueNumber int64  `json:"next_issue_number"`
}

type projectAliasRecord struct {
	ID            int64  `json:"id"`
	ProjectID     int64  `json:"project_id"`
	AliasIdentity string `json:"alias_identity"`
	AliasKind     string `json:"alias_kind"`
	RootPath      string `json:"root_path"`
	CreatedAt     string `json:"created_at"`
	LastSeenAt    string `json:"last_seen_at"`
}

type issueRecord struct {
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

type commentRecord struct {
	ID        int64  `json:"id"`
	IssueID   int64  `json:"issue_id"`
	Author    string `json:"author"`
	Body      string `json:"body"`
	CreatedAt string `json:"created_at"`
}

type issueLabelRecord struct {
	IssueID   int64  `json:"issue_id"`
	Label     string `json:"label"`
	Author    string `json:"author"`
	CreatedAt string `json:"created_at"`
}

type linkRecord struct {
	ID          int64  `json:"id"`
	ProjectID   int64  `json:"project_id"`
	FromIssueID int64  `json:"from_issue_id"`
	ToIssueID   int64  `json:"to_issue_id"`
	Type        string `json:"type"`
	Author      string `json:"author"`
	CreatedAt   string `json:"created_at"`
}

type eventRecord struct {
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

type purgeLogRecord struct {
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

type sqliteSequenceRecord struct {
	Name string `json:"name"`
	Seq  int64  `json:"seq"`
}

func upsertSequence(ctx context.Context, tx *sql.Tx, name string, seq int64) error {
	res, err := tx.ExecContext(ctx, `UPDATE sqlite_sequence SET seq = ? WHERE name = ?`, seq, name)
	if err != nil {
		return fmt.Errorf("update sqlite_sequence %s: %w", name, err)
	}
	n, err := res.RowsAffected()
	if err != nil {
		return fmt.Errorf("sqlite_sequence rows affected: %w", err)
	}
	if n == 0 {
		if _, err := tx.ExecContext(ctx,
			`INSERT INTO sqlite_sequence(name, seq) VALUES(?, ?)`, name, seq); err != nil {
			return fmt.Errorf("insert sqlite_sequence %s: %w", name, err)
		}
	}
	return nil
}

func reconcileSequences(ctx context.Context, tx *sql.Tx) error {
	for _, table := range []string{"projects", "project_aliases", "issues", "comments", "links", "events", "purge_log"} {
		var maxID int64
		if err := tx.QueryRowContext(ctx,
			`SELECT COALESCE(MAX(id), 0) FROM `+table).Scan(&maxID); err != nil {
			return fmt.Errorf("max id for %s: %w", table, err)
		}
		var stored sql.NullInt64
		if err := tx.QueryRowContext(ctx,
			`SELECT MAX(seq) FROM sqlite_sequence WHERE name = ?`, table).Scan(&stored); err != nil {
			return fmt.Errorf("read sqlite_sequence %s: %w", table, err)
		}
		seq := maxID
		if stored.Valid && stored.Int64 > seq {
			seq = stored.Int64
		}
		if seq > 0 {
			if err := upsertSequence(ctx, tx, table, seq); err != nil {
				return err
			}
		}
	}
	return nil
}

func validateBeforeCommit(ctx context.Context, tx *sql.Tx) error {
	fkRows, err := tx.QueryContext(ctx, `PRAGMA foreign_key_check`)
	if err != nil {
		return fmt.Errorf("foreign_key_check: %w", err)
	}
	defer func() { _ = fkRows.Close() }()
	if fkRows.Next() {
		return fmt.Errorf("foreign_key_check: violations found")
	}
	if err := fkRows.Err(); err != nil {
		return fmt.Errorf("foreign_key_check rows: %w", err)
	}

	rows, err := tx.QueryContext(ctx, `PRAGMA integrity_check`)
	if err != nil {
		return fmt.Errorf("integrity_check: %w", err)
	}
	defer func() { _ = rows.Close() }()
	for rows.Next() {
		var msg string
		if err := rows.Scan(&msg); err != nil {
			return fmt.Errorf("integrity_check scan: %w", err)
		}
		if !strings.EqualFold(msg, "ok") {
			return fmt.Errorf("integrity_check: %s", msg)
		}
	}
	return rows.Err()
}
