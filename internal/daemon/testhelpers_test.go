package daemon_test

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"github.com/wesm/kata/internal/db"
)

// testDBHandle bundles a fresh *db.DB and a started-at timestamp for daemon
// server tests.
type testDBHandle struct {
	db  *db.DB
	now time.Time
}

// openTestDB opens a fresh sqlite DB rooted in t.TempDir() and registers a
// cleanup to close it. The returned handle is suitable for daemon.ServerConfig.
func openTestDB(t *testing.T) testDBHandle {
	t.Helper()
	d, err := db.Open(context.Background(), filepath.Join(t.TempDir(), "kata.db"))
	require.NoError(t, err)
	t.Cleanup(func() { _ = d.Close() })
	return testDBHandle{db: d, now: time.Now().UTC()}
}
