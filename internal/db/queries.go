package db

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
)

// ErrNotFound is returned when a single-row lookup matches zero rows.
var ErrNotFound = errors.New("not found")

// CreateProject inserts a new projects row with default next_issue_number=1.
func (d *DB) CreateProject(ctx context.Context, identity, name string) (Project, error) {
	res, err := d.ExecContext(ctx,
		`INSERT INTO projects(identity, name) VALUES(?, ?)`, identity, name)
	if err != nil {
		return Project{}, fmt.Errorf("insert project: %w", err)
	}
	id, err := res.LastInsertId()
	if err != nil {
		return Project{}, fmt.Errorf("last id: %w", err)
	}
	return d.ProjectByID(ctx, id)
}

// ProjectByID fetches one project by its rowid.
func (d *DB) ProjectByID(ctx context.Context, id int64) (Project, error) {
	row := d.QueryRowContext(ctx, projectSelect+` WHERE id = ?`, id)
	return scanProject(row)
}

// ProjectByIdentity fetches one project by its UNIQUE identity.
func (d *DB) ProjectByIdentity(ctx context.Context, identity string) (Project, error) {
	row := d.QueryRowContext(ctx, projectSelect+` WHERE identity = ?`, identity)
	return scanProject(row)
}

// ListProjects returns every project ordered by id ASC.
func (d *DB) ListProjects(ctx context.Context) ([]Project, error) {
	rows, err := d.QueryContext(ctx, projectSelect+` ORDER BY id ASC`)
	if err != nil {
		return nil, fmt.Errorf("list projects: %w", err)
	}
	defer func() { _ = rows.Close() }()
	var out []Project
	for rows.Next() {
		p, err := scanProject(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, p)
	}
	return out, rows.Err()
}

// AttachAlias inserts a project_aliases row.
func (d *DB) AttachAlias(ctx context.Context, projectID int64, identity, kind, rootPath string) (ProjectAlias, error) {
	res, err := d.ExecContext(ctx,
		`INSERT INTO project_aliases(project_id, alias_identity, alias_kind, root_path)
		 VALUES(?, ?, ?, ?)`, projectID, identity, kind, rootPath)
	if err != nil {
		return ProjectAlias{}, fmt.Errorf("insert alias: %w", err)
	}
	id, err := res.LastInsertId()
	if err != nil {
		return ProjectAlias{}, err
	}
	return d.aliasByID(ctx, id)
}

// AliasByIdentity returns the alias for a given alias_identity.
func (d *DB) AliasByIdentity(ctx context.Context, identity string) (ProjectAlias, error) {
	row := d.QueryRowContext(ctx, aliasSelect+` WHERE alias_identity = ?`, identity)
	return scanAlias(row)
}

func (d *DB) aliasByID(ctx context.Context, id int64) (ProjectAlias, error) {
	row := d.QueryRowContext(ctx, aliasSelect+` WHERE id = ?`, id)
	return scanAlias(row)
}

// TouchAlias updates last_seen_at to now and rewrites root_path.
func (d *DB) TouchAlias(ctx context.Context, aliasID int64, rootPath string) error {
	_, err := d.ExecContext(ctx,
		`UPDATE project_aliases
		 SET last_seen_at = strftime('%Y-%m-%dT%H:%M:%fZ','now'),
		     root_path    = ?
		 WHERE id = ?`, rootPath, aliasID)
	if err != nil {
		return fmt.Errorf("touch alias: %w", err)
	}
	return nil
}

// ProjectAliases returns every alias attached to a project ordered by id ASC.
func (d *DB) ProjectAliases(ctx context.Context, projectID int64) ([]ProjectAlias, error) {
	rows, err := d.QueryContext(ctx, aliasSelect+` WHERE project_id = ? ORDER BY id ASC`, projectID)
	if err != nil {
		return nil, fmt.Errorf("list aliases: %w", err)
	}
	defer func() { _ = rows.Close() }()
	var out []ProjectAlias
	for rows.Next() {
		a, err := scanAlias(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, a)
	}
	return out, rows.Err()
}

// projectSelect is the canonical SELECT list for projects rows.
const projectSelect = `SELECT id, identity, name, created_at, next_issue_number FROM projects`

// rowScanner is the subset of *sql.Row / *sql.Rows used by scan helpers.
type rowScanner interface {
	Scan(...any) error
}

func scanProject(r rowScanner) (Project, error) {
	var p Project
	err := r.Scan(&p.ID, &p.Identity, &p.Name, &p.CreatedAt, &p.NextIssueNumber)
	if errors.Is(err, sql.ErrNoRows) {
		return Project{}, ErrNotFound
	}
	if err != nil {
		return Project{}, fmt.Errorf("scan project: %w", err)
	}
	return p, nil
}

// aliasSelect is the canonical SELECT list for project_aliases rows.
const aliasSelect = `SELECT id, project_id, alias_identity, alias_kind, root_path, created_at, last_seen_at FROM project_aliases`

func scanAlias(r rowScanner) (ProjectAlias, error) {
	var a ProjectAlias
	err := r.Scan(&a.ID, &a.ProjectID, &a.AliasIdentity, &a.AliasKind, &a.RootPath, &a.CreatedAt, &a.LastSeenAt)
	if errors.Is(err, sql.ErrNoRows) {
		return ProjectAlias{}, ErrNotFound
	}
	if err != nil {
		return ProjectAlias{}, fmt.Errorf("scan alias: %w", err)
	}
	return a, nil
}

// ErrNoFields is returned by EditIssue when no field is set.
var ErrNoFields = errors.New("no fields to update")

// CreateIssueParams carries inputs for CreateIssue.
type CreateIssueParams struct {
	ProjectID int64
	Title     string
	Body      string
	Author    string
}

// CreateIssue inserts an issue, allocates the next number atomically, and
// appends an issue.created event in the same transaction.
func (d *DB) CreateIssue(ctx context.Context, p CreateIssueParams) (Issue, Event, error) {
	tx, err := d.BeginTx(ctx, &sql.TxOptions{})
	if err != nil {
		return Issue{}, Event{}, fmt.Errorf("begin: %w", err)
	}
	defer func() { _ = tx.Rollback() }()

	// Atomic number allocation: bump next_issue_number first and capture the
	// pre-bump value via RETURNING. Concurrent CreateIssue calls serialize on
	// this UPDATE, so each one gets a distinct number.
	var (
		identity string
		nextNum  int64
	)
	if err := tx.QueryRowContext(ctx,
		`UPDATE projects
		 SET next_issue_number = next_issue_number + 1
		 WHERE id = ?
		 RETURNING next_issue_number - 1, identity`, p.ProjectID).
		Scan(&nextNum, &identity); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return Issue{}, Event{}, ErrNotFound
		}
		return Issue{}, Event{}, fmt.Errorf("allocate issue number: %w", err)
	}

	res, err := tx.ExecContext(ctx,
		`INSERT INTO issues(project_id, number, title, body, author)
		 VALUES(?, ?, ?, ?, ?)`,
		p.ProjectID, nextNum, p.Title, p.Body, p.Author)
	if err != nil {
		return Issue{}, Event{}, fmt.Errorf("insert issue: %w", err)
	}
	issueID, err := res.LastInsertId()
	if err != nil {
		return Issue{}, Event{}, err
	}

	evt, err := insertEventTx(ctx, tx, eventInsert{
		ProjectID:       p.ProjectID,
		ProjectIdentity: identity,
		IssueID:         &issueID,
		IssueNumber:     &nextNum,
		Type:            "issue.created",
		Actor:           p.Author,
		Payload:         "{}",
	})
	if err != nil {
		return Issue{}, Event{}, err
	}

	if err := tx.Commit(); err != nil {
		return Issue{}, Event{}, fmt.Errorf("commit: %w", err)
	}

	issue, err := d.IssueByID(ctx, issueID)
	if err != nil {
		return Issue{}, Event{}, err
	}
	return issue, evt, nil
}

// IssueByID fetches an issue by rowid.
func (d *DB) IssueByID(ctx context.Context, id int64) (Issue, error) {
	row := d.QueryRowContext(ctx, issueSelect+` WHERE id = ?`, id)
	return scanIssue(row)
}

// IssueByNumber fetches an issue by (project_id, number).
func (d *DB) IssueByNumber(ctx context.Context, projectID, number int64) (Issue, error) {
	row := d.QueryRowContext(ctx, issueSelect+` WHERE project_id = ? AND number = ?`, projectID, number)
	return scanIssue(row)
}

// ListIssuesParams filters list output. Status="" → all. Empty struct → all.
type ListIssuesParams struct {
	ProjectID int64
	Status    string // "open" | "closed" | "" (any)
	Limit     int    // 0 = no limit
}

// ListIssues returns issues in the given project, excluding soft-deleted rows.
func (d *DB) ListIssues(ctx context.Context, p ListIssuesParams) ([]Issue, error) {
	q := issueSelect + ` WHERE project_id = ? AND deleted_at IS NULL`
	args := []any{p.ProjectID}
	if p.Status != "" {
		q += ` AND status = ?`
		args = append(args, p.Status)
	}
	q += ` ORDER BY updated_at DESC, id DESC`
	if p.Limit > 0 {
		q += fmt.Sprintf(` LIMIT %d`, p.Limit)
	}
	rows, err := d.QueryContext(ctx, q, args...)
	if err != nil {
		return nil, fmt.Errorf("list issues: %w", err)
	}
	defer func() { _ = rows.Close() }()
	var out []Issue
	for rows.Next() {
		i, err := scanIssue(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, i)
	}
	return out, rows.Err()
}

// CreateCommentParams carries inputs for CreateComment.
type CreateCommentParams struct {
	IssueID int64
	Author  string
	Body    string
}

// CreateComment appends a comment + issue.commented event in one tx, bumping
// issues.updated_at.
func (d *DB) CreateComment(ctx context.Context, p CreateCommentParams) (Comment, Event, error) {
	tx, err := d.BeginTx(ctx, nil)
	if err != nil {
		return Comment{}, Event{}, err
	}
	defer func() { _ = tx.Rollback() }()

	issue, projectIdentity, err := lookupIssueForEvent(ctx, tx, p.IssueID)
	if err != nil {
		return Comment{}, Event{}, err
	}

	res, err := tx.ExecContext(ctx,
		`INSERT INTO comments(issue_id, author, body) VALUES(?, ?, ?)`,
		p.IssueID, p.Author, p.Body)
	if err != nil {
		return Comment{}, Event{}, fmt.Errorf("insert comment: %w", err)
	}
	commentID, err := res.LastInsertId()
	if err != nil {
		return Comment{}, Event{}, err
	}

	if _, err := tx.ExecContext(ctx,
		`UPDATE issues SET updated_at = strftime('%Y-%m-%dT%H:%M:%fZ','now') WHERE id = ?`,
		p.IssueID); err != nil {
		return Comment{}, Event{}, fmt.Errorf("touch issue: %w", err)
	}

	payload := fmt.Sprintf(`{"comment_id":%d}`, commentID)
	evt, err := insertEventTx(ctx, tx, eventInsert{
		ProjectID:       issue.ProjectID,
		ProjectIdentity: projectIdentity,
		IssueID:         &issue.ID,
		IssueNumber:     &issue.Number,
		Type:            "issue.commented",
		Actor:           p.Author,
		Payload:         payload,
	})
	if err != nil {
		return Comment{}, Event{}, err
	}

	if err := tx.Commit(); err != nil {
		return Comment{}, Event{}, err
	}

	var c Comment
	if err := d.QueryRowContext(ctx,
		`SELECT id, issue_id, author, body, created_at FROM comments WHERE id = ?`,
		commentID).Scan(&c.ID, &c.IssueID, &c.Author, &c.Body, &c.CreatedAt); err != nil {
		return Comment{}, Event{}, fmt.Errorf("read comment: %w", err)
	}
	return c, evt, nil
}

// CloseIssue sets status=closed unless already closed.
func (d *DB) CloseIssue(ctx context.Context, issueID int64, reason, actor string) (Issue, *Event, bool, error) {
	if reason == "" {
		reason = "done"
	}
	tx, err := d.BeginTx(ctx, nil)
	if err != nil {
		return Issue{}, nil, false, err
	}
	defer func() { _ = tx.Rollback() }()

	issue, projectIdentity, err := lookupIssueForEvent(ctx, tx, issueID)
	if err != nil {
		return Issue{}, nil, false, err
	}
	if issue.Status == "closed" {
		return issue, nil, false, tx.Commit()
	}
	if _, err := tx.ExecContext(ctx,
		`UPDATE issues
		 SET status        = 'closed',
		     closed_reason = ?,
		     closed_at     = strftime('%Y-%m-%dT%H:%M:%fZ','now'),
		     updated_at    = strftime('%Y-%m-%dT%H:%M:%fZ','now')
		 WHERE id = ?`, reason, issueID); err != nil {
		return Issue{}, nil, false, fmt.Errorf("close: %w", err)
	}
	payload := fmt.Sprintf(`{"reason":%q}`, reason)
	evt, err := insertEventTx(ctx, tx, eventInsert{
		ProjectID:       issue.ProjectID,
		ProjectIdentity: projectIdentity,
		IssueID:         &issue.ID,
		IssueNumber:     &issue.Number,
		Type:            "issue.closed",
		Actor:           actor,
		Payload:         payload,
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

// ReopenIssue clears status=closed unless already open.
func (d *DB) ReopenIssue(ctx context.Context, issueID int64, actor string) (Issue, *Event, bool, error) {
	tx, err := d.BeginTx(ctx, nil)
	if err != nil {
		return Issue{}, nil, false, err
	}
	defer func() { _ = tx.Rollback() }()

	issue, projectIdentity, err := lookupIssueForEvent(ctx, tx, issueID)
	if err != nil {
		return Issue{}, nil, false, err
	}
	if issue.Status == "open" {
		return issue, nil, false, tx.Commit()
	}
	if _, err := tx.ExecContext(ctx,
		`UPDATE issues
		 SET status        = 'open',
		     closed_reason = NULL,
		     closed_at     = NULL,
		     updated_at    = strftime('%Y-%m-%dT%H:%M:%fZ','now')
		 WHERE id = ?`, issueID); err != nil {
		return Issue{}, nil, false, fmt.Errorf("reopen: %w", err)
	}
	evt, err := insertEventTx(ctx, tx, eventInsert{
		ProjectID:       issue.ProjectID,
		ProjectIdentity: projectIdentity,
		IssueID:         &issue.ID,
		IssueNumber:     &issue.Number,
		Type:            "issue.reopened",
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

// EditIssueParams carries the optional fields for an edit. Nil = leave alone.
type EditIssueParams struct {
	IssueID int64
	Title   *string
	Body    *string
	Owner   *string
	Actor   string
}

// EditIssue mutates title/body/owner. ErrNoFields if none are set.
func (d *DB) EditIssue(ctx context.Context, p EditIssueParams) (Issue, *Event, bool, error) {
	if p.Title == nil && p.Body == nil && p.Owner == nil {
		return Issue{}, nil, false, ErrNoFields
	}
	tx, err := d.BeginTx(ctx, nil)
	if err != nil {
		return Issue{}, nil, false, err
	}
	defer func() { _ = tx.Rollback() }()

	issue, projectIdentity, err := lookupIssueForEvent(ctx, tx, p.IssueID)
	if err != nil {
		return Issue{}, nil, false, err
	}

	sets := []string{`updated_at = strftime('%Y-%m-%dT%H:%M:%fZ','now')`}
	args := []any{}
	if p.Title != nil {
		sets = append(sets, `title = ?`)
		args = append(args, *p.Title)
	}
	if p.Body != nil {
		sets = append(sets, `body = ?`)
		args = append(args, *p.Body)
	}
	if p.Owner != nil {
		sets = append(sets, `owner = ?`)
		args = append(args, *p.Owner)
	}
	args = append(args, p.IssueID)
	// `sets` only contains string literals chosen above; user-provided values
	// are parameterized via `args`. Safe to concatenate.
	q := `UPDATE issues SET ` + joinComma(sets) + ` WHERE id = ?` // #nosec G202
	if _, err := tx.ExecContext(ctx, q, args...); err != nil {
		return Issue{}, nil, false, fmt.Errorf("update issue: %w", err)
	}
	evt, err := insertEventTx(ctx, tx, eventInsert{
		ProjectID:       issue.ProjectID,
		ProjectIdentity: projectIdentity,
		IssueID:         &issue.ID,
		IssueNumber:     &issue.Number,
		Type:            "issue.updated",
		Actor:           p.Actor,
		Payload:         "{}",
	})
	if err != nil {
		return Issue{}, nil, false, err
	}
	if err := tx.Commit(); err != nil {
		return Issue{}, nil, false, err
	}
	updated, err := d.IssueByID(ctx, p.IssueID)
	if err != nil {
		return Issue{}, nil, false, err
	}
	return updated, &evt, true, nil
}

func joinComma(parts []string) string {
	out := ""
	for i, p := range parts {
		if i > 0 {
			out += ", "
		}
		out += p
	}
	return out
}

// lookupIssueForEvent fetches the issue + its project's identity for event
// snapshotting. Used inside transactions. The query is written standalone
// rather than concatenating issueSelect so the FROM/JOIN clause appears once.
func lookupIssueForEvent(ctx context.Context, tx *sql.Tx, issueID int64) (Issue, string, error) {
	const q = `
		SELECT i.id, i.project_id, i.number, i.title, i.body, i.status,
		       i.closed_reason, i.owner, i.author, i.created_at, i.updated_at,
		       i.closed_at, i.deleted_at, p.identity
		FROM issues i
		JOIN projects p ON p.id = i.project_id
		WHERE i.id = ?`
	var i Issue
	var identity string
	err := tx.QueryRowContext(ctx, q, issueID).
		Scan(&i.ID, &i.ProjectID, &i.Number, &i.Title, &i.Body, &i.Status, &i.ClosedReason, &i.Owner, &i.Author, &i.CreatedAt, &i.UpdatedAt, &i.ClosedAt, &i.DeletedAt, &identity)
	if errors.Is(err, sql.ErrNoRows) {
		return Issue{}, "", ErrNotFound
	}
	if err != nil {
		return Issue{}, "", fmt.Errorf("lookup issue: %w", err)
	}
	return i, identity, nil
}

const issueSelect = `SELECT i.id, i.project_id, i.number, i.title, i.body, i.status, i.closed_reason, i.owner, i.author, i.created_at, i.updated_at, i.closed_at, i.deleted_at FROM issues i`

func scanIssue(r rowScanner) (Issue, error) {
	var i Issue
	err := r.Scan(&i.ID, &i.ProjectID, &i.Number, &i.Title, &i.Body, &i.Status, &i.ClosedReason, &i.Owner, &i.Author, &i.CreatedAt, &i.UpdatedAt, &i.ClosedAt, &i.DeletedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return Issue{}, ErrNotFound
	}
	if err != nil {
		return Issue{}, fmt.Errorf("scan issue: %w", err)
	}
	return i, nil
}

// eventInsert is the tx-internal payload used by insertEventTx.
type eventInsert struct {
	ProjectID       int64
	ProjectIdentity string
	IssueID         *int64
	IssueNumber     *int64
	RelatedIssueID  *int64
	Type            string
	Actor           string
	Payload         string
}

func insertEventTx(ctx context.Context, tx *sql.Tx, in eventInsert) (Event, error) {
	res, err := tx.ExecContext(ctx,
		`INSERT INTO events(project_id, project_identity, issue_id, issue_number, related_issue_id, type, actor, payload)
		 VALUES(?, ?, ?, ?, ?, ?, ?, ?)`,
		in.ProjectID, in.ProjectIdentity, in.IssueID, in.IssueNumber, in.RelatedIssueID, in.Type, in.Actor, in.Payload)
	if err != nil {
		return Event{}, fmt.Errorf("insert event: %w", err)
	}
	id, err := res.LastInsertId()
	if err != nil {
		return Event{}, err
	}
	var e Event
	err = tx.QueryRowContext(ctx,
		`SELECT id, project_id, project_identity, issue_id, issue_number, related_issue_id, type, actor, payload, created_at FROM events WHERE id = ?`, id).
		Scan(&e.ID, &e.ProjectID, &e.ProjectIdentity, &e.IssueID, &e.IssueNumber, &e.RelatedIssueID, &e.Type, &e.Actor, &e.Payload, &e.CreatedAt)
	if err != nil {
		return Event{}, fmt.Errorf("read event: %w", err)
	}
	return e, nil
}
