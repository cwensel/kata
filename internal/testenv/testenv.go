// Package testenv provides a per-test harness that boots a real daemon over TCP
// loopback, suitable for integration tests.
package testenv

import (
	"context"
	"fmt"
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
	URL         string
	HTTP        *http.Client
	DB          *db.DB
	Home        string
	Broadcaster *daemon.EventBroadcaster
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

	// Bind the listener once and hand it directly to Server.Serve so no other
	// process can grab the port between bind and serve (the close-then-reopen
	// pattern has a TOCTOU race).
	l, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	addr := l.Addr().(*net.TCPAddr).String() //nolint:forcetypeassert // net.Listen("tcp",...) always returns *net.TCPAddr

	bcast := daemon.NewEventBroadcaster()
	srv := daemon.NewServer(daemon.ServerConfig{
		DB:          d,
		StartedAt:   time.Now().UTC(),
		Broadcaster: bcast,
	})

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		defer close(done)
		_ = srv.Serve(ctx, l)
	}()
	// Cleanup must wait for Serve to return (Shutdown drained) before the DB is
	// closed, otherwise in-flight handlers can race against d.Close. t.Cleanup
	// is LIFO, so this fires before the d.Close cleanup registered above.
	t.Cleanup(func() {
		cancel()
		<-done
	})

	// Wait for /ping to answer with 200; if the daemon never becomes ready, or
	// if some other service won the port and answered with a non-200, fail
	// loudly at New rather than letting the test report a confusing failure on
	// its first real request.
	url := "http://" + addr
	deadline := time.Now().Add(2 * time.Second)
	client := &http.Client{Timeout: 2 * time.Second}
	var lastErr error
	ready := false
	for time.Now().Before(deadline) {
		resp, err := client.Get(url + "/api/v1/ping") //nolint:noctx // polling loop; context would add noise without benefit
		if err == nil {
			status := resp.StatusCode
			_ = resp.Body.Close()
			if status == http.StatusOK {
				ready = true
				break
			}
			lastErr = fmt.Errorf("unexpected /ping status %d", status)
		} else {
			lastErr = err
		}
		time.Sleep(20 * time.Millisecond)
	}
	require.Truef(t, ready, "daemon did not become ready within 2s: %v", lastErr)

	return &Env{URL: url, HTTP: client, DB: d, Home: home, Broadcaster: bcast}
}
