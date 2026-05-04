// Package db opens the kata SQLite database and applies embedded migrations.
package db

import (
	"context"
	"database/sql"
	"embed"
	"errors"
	"fmt"
	"sort"
	"strconv"
	"strings"

	_ "modernc.org/sqlite" // pure-Go SQLite driver registered as "sqlite"
)

//go:embed migrations/*.sql
var migrationsFS embed.FS

const currentSchemaVersion = 2

// CurrentSchemaVersion returns the schema version expected by this binary.
func CurrentSchemaVersion() int { return currentSchemaVersion }

// ErrSchemaCutoverRequired is returned by Open when an existing database is
// older than the binary's schema and must be upgraded through JSONL cutover.
var ErrSchemaCutoverRequired = errors.New("schema cutover required")

// DB wraps *sql.DB. Use Open to construct one with PRAGMAs applied.
type DB struct {
	*sql.DB
	path string
}

// Open opens (and if needed initializes) the kata SQLite database at path.
// PRAGMAs are applied for every connection (via the connection string and
// post-open exec) and pending migrations are run inside a transaction.
func Open(ctx context.Context, path string) (*DB, error) {
	dsn := fmt.Sprintf(
		"file:%s?_pragma=foreign_keys(1)&_pragma=journal_mode(WAL)&_pragma=synchronous(NORMAL)&_pragma=busy_timeout(5000)",
		path,
	)
	sdb, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, fmt.Errorf("open %s: %w", path, err)
	}
	// Single writer is fine for v1; SetMaxOpenConns left at default for reads.
	if err := sdb.PingContext(ctx); err != nil {
		_ = sdb.Close()
		return nil, fmt.Errorf("ping %s: %w", path, err)
	}
	d := &DB{DB: sdb, path: path}
	if err := d.migrate(ctx); err != nil {
		_ = sdb.Close()
		return nil, err
	}
	return d, nil
}

// OpenReadOnly opens an existing kata database without applying migrations.
// It is used by JSONL cutover so the old source DB can be exported without
// the normal Open path mutating meta.schema_version first.
func OpenReadOnly(ctx context.Context, path string) (*DB, error) {
	dsn := fmt.Sprintf(
		"file:%s?mode=ro&_pragma=foreign_keys(1)&_pragma=busy_timeout(5000)",
		path,
	)
	sdb, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, fmt.Errorf("open read-only %s: %w", path, err)
	}
	if err := sdb.PingContext(ctx); err != nil {
		_ = sdb.Close()
		return nil, fmt.Errorf("ping read-only %s: %w", path, err)
	}
	return &DB{DB: sdb, path: path}, nil
}

// Path returns the resolved database path.
func (d *DB) Path() string { return d.path }

// PeekSchemaVersion reads meta.schema_version without applying migrations.
// It returns 0 when the database exists but has no meta table or schema_version
// row.
func PeekSchemaVersion(ctx context.Context, path string) (int, error) {
	d, err := OpenReadOnly(ctx, path)
	if err != nil {
		return 0, err
	}
	defer func() { _ = d.Close() }()
	return d.currentVersion(ctx)
}

func (d *DB) migrate(ctx context.Context) error {
	current, err := d.currentVersion(ctx)
	if err != nil {
		return err
	}
	if current > 0 && current < currentSchemaVersion {
		return fmt.Errorf("%w: database schema_version %d is older than binary schema %d; run JSONL cutover before opening",
			ErrSchemaCutoverRequired, current, currentSchemaVersion)
	}
	files, err := migrationsFS.ReadDir("migrations")
	if err != nil {
		return fmt.Errorf("read embed: %w", err)
	}
	sort.Slice(files, func(i, j int) bool { return files[i].Name() < files[j].Name() })
	for _, f := range files {
		ver, err := parseMigrationVersion(f.Name())
		if err != nil {
			return err
		}
		if ver <= current {
			continue
		}
		body, err := migrationsFS.ReadFile("migrations/" + f.Name())
		if err != nil {
			return fmt.Errorf("read %s: %w", f.Name(), err)
		}
		tx, err := d.BeginTx(ctx, nil)
		if err != nil {
			return fmt.Errorf("begin tx for %s: %w", f.Name(), err)
		}
		if _, err := tx.ExecContext(ctx, string(body)); err != nil {
			_ = tx.Rollback()
			return fmt.Errorf("apply %s: %w", f.Name(), err)
		}
		if _, err := tx.ExecContext(ctx,
			`INSERT INTO meta(key,value) VALUES('schema_version', ?)
			 ON CONFLICT(key) DO UPDATE SET value=excluded.value`, strconv.Itoa(currentSchemaVersion)); err != nil {
			_ = tx.Rollback()
			return fmt.Errorf("record schema version %d: %w", currentSchemaVersion, err)
		}
		if err := tx.Commit(); err != nil {
			return fmt.Errorf("commit %s: %w", f.Name(), err)
		}
	}
	return nil
}

// currentVersion returns 0 when the meta table doesn't exist yet (fresh DB).
func (d *DB) currentVersion(ctx context.Context) (int, error) {
	exists, err := d.tableExists(ctx, "meta")
	if err != nil {
		return 0, err
	}
	if !exists {
		return 0, nil
	}
	var v string
	err = d.QueryRowContext(ctx, `SELECT value FROM meta WHERE key='schema_version'`).Scan(&v)
	if errors.Is(err, sql.ErrNoRows) {
		return 0, nil
	}
	if err != nil {
		return 0, fmt.Errorf("read schema_version: %w", err)
	}
	n, err := strconv.Atoi(v)
	if err != nil {
		return 0, fmt.Errorf("parse schema_version %q: %w", v, err)
	}
	return n, nil
}

func (d *DB) tableExists(ctx context.Context, name string) (bool, error) {
	var n int
	err := d.QueryRowContext(ctx,
		`SELECT 1 FROM sqlite_master WHERE type='table' AND name=?`, name).Scan(&n)
	if errors.Is(err, sql.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	return true, nil
}

// parseMigrationVersion extracts the leading integer from filenames like
// "0001_init.sql" → 1.
func parseMigrationVersion(name string) (int, error) {
	parts := strings.SplitN(name, "_", 2)
	if len(parts) < 2 {
		return 0, fmt.Errorf("invalid migration filename: %s", name)
	}
	n, err := strconv.Atoi(parts[0])
	if err != nil {
		return 0, fmt.Errorf("parse version in %s: %w", name, err)
	}
	return n, nil
}
