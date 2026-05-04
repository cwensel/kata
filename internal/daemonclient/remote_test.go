package daemonclient

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func pingingServer(t *testing.T) *httptest.Server {
	t.Helper()
	s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/v1/ping" {
			http.NotFound(w, r)
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]any{
			"ok":      true,
			"service": "kata",
			"version": "test",
		})
	}))
	t.Cleanup(s.Close)
	return s
}

func TestResolveRemote_NoEnvNoFile(t *testing.T) {
	t.Setenv("KATA_SERVER", "")
	dir := t.TempDir()
	t.Chdir(dir)

	url, ok, err := resolveRemote(context.Background())
	require.NoError(t, err)
	assert.False(t, ok)
	assert.Empty(t, url)
}

func TestResolveRemote_EnvWinsAndProbes(t *testing.T) {
	srv := pingingServer(t)
	t.Setenv("KATA_SERVER", srv.URL)

	url, ok, err := resolveRemote(context.Background())
	require.NoError(t, err)
	assert.True(t, ok)
	assert.Equal(t, srv.URL, url)
}

func TestResolveRemote_EnvUnreachableErrors(t *testing.T) {
	t.Setenv("KATA_SERVER", "http://127.0.0.1:1") // closed port

	_, _, err := resolveRemote(context.Background())
	require.Error(t, err)
	assert.Contains(t, err.Error(), "KATA_SERVER")
	assert.Contains(t, err.Error(), "http://127.0.0.1:1")
	assert.ErrorIs(t, err, ErrRemoteUnavailable)
}

func TestResolveRemote_EnvMalformedErrors(t *testing.T) {
	t.Setenv("KATA_SERVER", "::not-a-url::")

	_, _, err := resolveRemote(context.Background())
	require.Error(t, err)
	assert.Contains(t, err.Error(), "KATA_SERVER")
}

func TestResolveRemote_FileWhenNoEnv(t *testing.T) {
	srv := pingingServer(t)
	t.Setenv("KATA_SERVER", "")
	dir := t.TempDir()
	t.Chdir(dir)
	require.NoError(t, os.WriteFile(filepath.Join(dir, ".kata.local.toml"),
		[]byte(`version = 1
[server]
url = "`+srv.URL+`"
`), 0o600))

	url, ok, err := resolveRemote(context.Background())
	require.NoError(t, err)
	assert.True(t, ok)
	assert.Equal(t, srv.URL, url)
}

func TestResolveRemote_EnvWinsOverFile(t *testing.T) {
	srv := pingingServer(t)
	t.Setenv("KATA_SERVER", srv.URL)
	dir := t.TempDir()
	t.Chdir(dir)
	require.NoError(t, os.WriteFile(filepath.Join(dir, ".kata.local.toml"),
		[]byte(`version = 1
[server]
url = "http://10.255.255.1:9"
`), 0o600))

	url, ok, err := resolveRemote(context.Background())
	require.NoError(t, err)
	assert.True(t, ok)
	assert.Equal(t, srv.URL, url, "env URL must win over file URL")
}

func TestResolveRemote_FileEmptyURLFallsThrough(t *testing.T) {
	t.Setenv("KATA_SERVER", "")
	dir := t.TempDir()
	t.Chdir(dir)
	require.NoError(t, os.WriteFile(filepath.Join(dir, ".kata.local.toml"),
		[]byte(`version = 1
[server]
url = ""
`), 0o600))

	url, ok, err := resolveRemote(context.Background())
	require.NoError(t, err)
	assert.False(t, ok, "empty server URL must be treated as no remote configured")
	assert.Empty(t, url)
}

func TestResolveRemote_FileUnreachableErrors(t *testing.T) {
	t.Setenv("KATA_SERVER", "")
	dir := t.TempDir()
	t.Chdir(dir)
	require.NoError(t, os.WriteFile(filepath.Join(dir, ".kata.local.toml"),
		[]byte(`version = 1
[server]
url = "http://127.0.0.1:1"
`), 0o600))

	_, _, err := resolveRemote(context.Background())
	require.Error(t, err)
	assert.Contains(t, err.Error(), ".kata.local.toml")
	assert.ErrorIs(t, err, ErrRemoteUnavailable)
}
