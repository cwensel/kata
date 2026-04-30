package db

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
)

// ErrLinkExists is returned when a (from, to, type) triple already has a row.
// Caller treats this as a no-op success on duplicate links.
var ErrLinkExists = errors.New("link already exists")

// ErrParentAlreadySet is returned when a child issue already has a parent and
// CreateLink is called with type=parent.
var ErrParentAlreadySet = errors.New("parent already set")

// ErrSelfLink is returned when from_issue_id == to_issue_id.
var ErrSelfLink = errors.New("self-link not allowed")

// ErrCrossProjectLink is returned when the same-project trigger fires.
var ErrCrossProjectLink = errors.New("cross-project link not allowed")

// CreateLinkParams carries inputs for CreateLink. The caller is responsible
// for canonical ordering of `related` links (from < to) before calling.
type CreateLinkParams struct {
	ProjectID   int64
	FromIssueID int64
	ToIssueID   int64
	Type        string // "parent" | "blocks" | "related"
	Author      string
}

// CreateLink inserts a links row. Distinct error types let the caller emit
// the right wire status without parsing SQLite messages.
func (d *DB) CreateLink(ctx context.Context, p CreateLinkParams) (Link, error) {
	res, err := d.ExecContext(ctx,
		`INSERT INTO links(project_id, from_issue_id, to_issue_id, type, author)
		 VALUES(?, ?, ?, ?, ?)`,
		p.ProjectID, p.FromIssueID, p.ToIssueID, p.Type, p.Author)
	if err != nil {
		classified := classifyLinkInsertError(err)
		// SQLite may report the partial-parent index violation as a bare
		// `links.from_issue_id` UNIQUE failure, which classifies to
		// ErrParentAlreadySet. For an exact-duplicate parent link the
		// caller-facing semantic is "already linked" (200 no-op), not
		// "different parent set" (409 conflict). Disambiguate by re-querying.
		if errors.Is(classified, ErrParentAlreadySet) && p.Type == "parent" {
			if _, lookupErr := d.LinkByEndpoints(ctx, p.FromIssueID, p.ToIssueID, "parent"); lookupErr == nil {
				return Link{}, ErrLinkExists
			}
		}
		return Link{}, classified
	}
	id, err := res.LastInsertId()
	if err != nil {
		return Link{}, fmt.Errorf("last insert id: %w", err)
	}
	return d.LinkByID(ctx, id)
}

// classifyLinkInsertError maps SQLite constraint failures to typed errors so
// the handler can choose the right HTTP status without string-matching.
//
// Order matters: the triple-UNIQUE check must run before the partial-parent
// check because both messages start with "links.from_issue_id". The triple is
// distinguishable by the trailing column list; once that case is rejected,
// any remaining "links.from_issue_id" UNIQUE error must be the partial index
// on (from_issue_id) WHERE type='parent'. modernc.org/sqlite's error text for
// partial-index violations names only the indexed column, not the WHERE
// clause — see TestCreateLink_SecondParentIsErrParentAlreadySet.
func classifyLinkInsertError(err error) error {
	msg := err.Error()
	switch {
	case strings.Contains(msg, "UNIQUE constraint failed: links.from_issue_id, links.to_issue_id, links.type"):
		return ErrLinkExists
	case strings.Contains(msg, "UNIQUE constraint failed: links.from_issue_id"):
		return ErrParentAlreadySet
	case strings.Contains(msg, "CHECK constraint failed") &&
		strings.Contains(msg, "from_issue_id <> to_issue_id"):
		return ErrSelfLink
	case strings.Contains(msg, "cross-project links are not allowed"):
		return ErrCrossProjectLink
	}
	return fmt.Errorf("insert link: %w", err)
}

// LinkByID fetches a link by rowid.
func (d *DB) LinkByID(ctx context.Context, id int64) (Link, error) {
	row := d.QueryRowContext(ctx, linkSelect+` WHERE id = ?`, id)
	return scanLink(row)
}

// LinkByEndpoints fetches the link for a (from, to, type) triple.
func (d *DB) LinkByEndpoints(ctx context.Context, fromIssueID, toIssueID int64, linkType string) (Link, error) {
	row := d.QueryRowContext(ctx,
		linkSelect+` WHERE from_issue_id = ? AND to_issue_id = ? AND type = ?`,
		fromIssueID, toIssueID, linkType)
	return scanLink(row)
}

// ParentOf returns the parent link for childIssueID (one-parent invariant).
// Returns ErrNotFound when no parent is set.
func (d *DB) ParentOf(ctx context.Context, childIssueID int64) (Link, error) {
	row := d.QueryRowContext(ctx,
		linkSelect+` WHERE from_issue_id = ? AND type = 'parent'`,
		childIssueID)
	return scanLink(row)
}

// LinksByIssue returns every link involving issueID (either endpoint), ordered
// by id ASC. Used to build the show-issue response and to back `kata unlink`'s
// list-then-delete flow.
func (d *DB) LinksByIssue(ctx context.Context, issueID int64) ([]Link, error) {
	rows, err := d.QueryContext(ctx,
		linkSelect+` WHERE from_issue_id = ? OR to_issue_id = ? ORDER BY id ASC`,
		issueID, issueID)
	if err != nil {
		return nil, fmt.Errorf("list links: %w", err)
	}
	defer func() { _ = rows.Close() }()
	var out []Link
	for rows.Next() {
		l, err := scanLink(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, l)
	}
	return out, rows.Err()
}

// DeleteLinkByID removes a links row. Returns ErrNotFound when no row exists.
func (d *DB) DeleteLinkByID(ctx context.Context, linkID int64) error {
	res, err := d.ExecContext(ctx, `DELETE FROM links WHERE id = ?`, linkID)
	if err != nil {
		return fmt.Errorf("delete link: %w", err)
	}
	n, err := res.RowsAffected()
	if err != nil {
		return fmt.Errorf("delete link rows affected: %w", err)
	}
	if n == 0 {
		return ErrNotFound
	}
	return nil
}

const linkSelect = `SELECT id, project_id, from_issue_id, to_issue_id, type, author, created_at FROM links`

func scanLink(r rowScanner) (Link, error) {
	var l Link
	err := r.Scan(&l.ID, &l.ProjectID, &l.FromIssueID, &l.ToIssueID, &l.Type, &l.Author, &l.CreatedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return Link{}, ErrNotFound
	}
	if err != nil {
		return Link{}, fmt.Errorf("scan link: %w", err)
	}
	return l, nil
}
