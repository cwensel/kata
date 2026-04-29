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
	done := make(chan struct{})
	go func() {
		defer close(done)
		_ = srv.Run(ctx)
	}()
	// Cleanup must wait for Run to return (Shutdown drained) before the DB is
	// closed, otherwise in-flight handlers can race against d.Close. t.Cleanup
	// is LIFO, so this fires before the d.Close cleanup registered above.
	t.Cleanup(func() {
		cancel()
		<-done
	})

	// Wait for /ping to answer; if the daemon never becomes ready, fail loudly
	// at New rather than letting the test report a confusing connection-refused
	// on its first real request.
	url := "http://" + addr
	deadline := time.Now().Add(2 * time.Second)
	client := &http.Client{Timeout: 2 * time.Second}
	var lastErr error
	ready := false
	for time.Now().Before(deadline) {
		resp, err := client.Get(url + "/api/v1/ping") //nolint:noctx // polling loop; context would add noise without benefit
		if err == nil {
			_ = resp.Body.Close()
			ready = true
			break
		}
		lastErr = err
		time.Sleep(20 * time.Millisecond)
	}
	require.Truef(t, ready, "daemon did not become ready within 2s: %v", lastErr)

	return &Env{URL: url, HTTP: client, DB: d, Home: home}
}
