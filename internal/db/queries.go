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
