package db_test

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestAllSchemaTablesExist guards against a future migration accidentally
// dropping a table that Plan 1 doesn't actively exercise. Plan 1 reads/writes
// projects, project_aliases, issues, comments, events, and meta. The other
// names below (links, issue_labels, purge_log, issues_fts) are scaffolded by
// 0001_init.sql for later plans; this test is the only thing that catches a
// silent removal.
func TestAllSchemaTablesExist(t *testing.T) {
	d := openTestDB(t)
	wanted := []string{
		"projects", "project_aliases", "issues", "comments",
		"links", "issue_labels", "events", "purge_log",
		"meta", "issues_fts",
	}
	for _, name := range wanted {
		var n int
		err := d.QueryRowContext(context.Background(),
			`SELECT 1 FROM sqlite_master WHERE name = ?`, name).Scan(&n)
		require.NoErrorf(t, err, "table %q missing from schema", name)
		assert.Equal(t, 1, n, name)
	}
}
