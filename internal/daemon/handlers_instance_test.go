package daemon_test

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/wesm/kata/internal/daemon"
	"github.com/wesm/kata/internal/db"
	"github.com/wesm/kata/internal/uid"
)

// TestInstance_ReturnsLocalUID covers spec §8.8: GET /api/v1/instance returns
// the value db.Open seeded into meta.instance_uid.
func TestInstance_ReturnsLocalUID(t *testing.T) {
	d := openTestDB(t)
	srv := daemon.NewServer(daemon.ServerConfig{DB: d.db, StartedAt: d.now})
	t.Cleanup(func() { _ = srv.Close() })

	ts := httptest.NewServer(srv.Handler())
	t.Cleanup(ts.Close)

	resp, err := http.Get(ts.URL + "/api/v1/instance")
	require.NoError(t, err)
	defer func() { _ = resp.Body.Close() }()
	assert.Equal(t, http.StatusOK, resp.StatusCode)

	bs, err := io.ReadAll(resp.Body)
	require.NoError(t, err)
	var body struct {
		InstanceUID string `json:"instance_uid"`
	}
	require.NoError(t, json.Unmarshal(bs, &body))
	assert.Equal(t, d.db.InstanceUID(), body.InstanceUID)
	assert.True(t, uid.Valid(body.InstanceUID), "instance_uid %q invalid", body.InstanceUID)
}

// TestInstance_503WhenUIDUnset covers spec §8.8 second bullet: the handler
// returns 503 instance_uid_unset when the *db.DB's cached InstanceUID() is
// empty. In production this is theoretical (db.Open always seeds the row);
// the test reaches it by routing the server through OpenReadOnly, which
// skips the seed step and yields a *DB with empty cached value.
func TestInstance_503WhenUIDUnset(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	path := filepath.Join(dir, "kata.db")

	// Materialize a real DB file so OpenReadOnly has something to attach to.
	primary, err := db.Open(ctx, path)
	require.NoError(t, err)
	require.NoError(t, primary.Close())

	// Read-only handle bypasses ensureInstanceUID; cached InstanceUID() is "".
	ro, err := db.OpenReadOnly(ctx, path)
	require.NoError(t, err)
	t.Cleanup(func() { _ = ro.Close() })
	require.Empty(t, ro.InstanceUID(), "OpenReadOnly must yield empty cached InstanceUID")

	srv := daemon.NewServer(daemon.ServerConfig{DB: ro, StartedAt: time.Now().UTC()})
	t.Cleanup(func() { _ = srv.Close() })
	ts := httptest.NewServer(srv.Handler())
	t.Cleanup(ts.Close)

	resp, err := http.Get(ts.URL + "/api/v1/instance") //nolint:gosec // test loopback
	require.NoError(t, err)
	defer func() { _ = resp.Body.Close() }()
	assert.Equal(t, http.StatusServiceUnavailable, resp.StatusCode)

	bs, err := io.ReadAll(resp.Body)
	require.NoError(t, err)
	var env struct {
		Error struct {
			Code string `json:"code"`
		} `json:"error"`
	}
	require.NoError(t, json.Unmarshal(bs, &env), string(bs))
	assert.Equal(t, "instance_uid_unset", env.Error.Code)
}
