// Package testenv provides a per-test harness that boots a real daemon over TCP
// loopback, suitable for integration tests.
package testenv

import (
	"context"
	"net"
	"net/http"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"github.com/wesm/kata/internal/daemon"
	"github.com/wesm/kata/internal/db"
)

// Env is a per-test daemon + DB + HTTP client bundle.
type Env struct {
	URL  string
	HTTP *http.Client
	DB   *db.DB
	Home string
}

// New launches a daemon listening on a free loopback port. The DB lives under
// a temp KATA_HOME. Cleanup is wired via t.Cleanup.
func New(t *testing.T) *Env {
	t.Helper()
	home := t.TempDir()
	t.Setenv("KATA_HOME", home)
	t.Setenv("KATA_DB", filepath.Join(home, "kata.db"))

	d, err := db.Open(context.Background(), filepath.Join(home, "kata.db"))
	require.NoError(t, err)
	t.Cleanup(func() { _ = d.Close() })

	// Pick a free port up front so we have a stable URL.
	l, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	addr := l.Addr().(*net.TCPAddr).String() //nolint:forcetypeassert // net.Listen("tcp",...) always returns *net.TCPAddr
	require.NoError(t, l.Close())

	srv := daemon.NewServer(daemon.ServerConfig{
		DB:        d,
		StartedAt: time.Now().UTC(),
		Endpoint:  daemon.TCPEndpoint(addr),
	})

	ctx, cancel := context.WithCancel(context.Background())
	go func() { _ = srv.Run(ctx) }()
	t.Cleanup(cancel)

	// Wait briefly for /ping to answer.
	url := "http://" + addr
	deadline := time.Now().Add(2 * time.Second)
	client := &http.Client{Timeout: 2 * time.Second}
	for time.Now().Before(deadline) {
		resp, err := client.Get(url + "/api/v1/ping") //nolint:noctx // polling loop; context would add noise without benefit
		if err == nil {
			_ = resp.Body.Close()
			break
		}
		time.Sleep(20 * time.Millisecond)
	}

	return &Env{URL: url, HTTP: client, DB: d, Home: home}
}
