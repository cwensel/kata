package main

import (
	"bytes"
	"context"
	"encoding/json"
	"net"
	"net/http"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/wesm/kata/internal/daemon"
	"github.com/wesm/kata/internal/db"
	"github.com/wesm/kata/internal/testenv"
)

func TestSmoke_ExportImport(t *testing.T) {
	env := testenv.New(t)
	dir := initBoundWorkspace(t, env.URL, "https://github.com/wesm/kata.git")

	runSmokeCLI(t, env.URL, []string{"--workspace", dir, "create", "export smoke one", "--body", "orchid body", "--label", "bug"})
	runSmokeCLI(t, env.URL, []string{"--workspace", dir, "create", "export smoke two", "--body", "blocks first", "--blocks", "1"})
	runSmokeCLI(t, env.URL, []string{"--workspace", dir, "comment", "1", "--body", "watermelon note"})
	runSmokeCLI(t, env.URL, []string{"--workspace", dir, "label", "add", "2", "backend"})

	before := runSmokeCLI(t, env.URL, []string{"--workspace", dir, "--json", "list"})
	beforeShow := runSmokeCLI(t, env.URL, []string{"--workspace", dir, "--json", "show", "1"})
	assert.Contains(t, beforeShow, "watermelon note")
	exportPath := filepath.Join(env.Home, "smoke.jsonl")
	runSmokeCLI(t, env.URL, []string{"export", "--output", exportPath})
	targetPath := filepath.Join(t.TempDir(), "imported.db")
	runSmokeCLI(t, env.URL, []string{"import", "--input", exportPath, "--target", targetPath})

	importedURL := startSmokeDaemonForDB(t, targetPath)
	after := runSmokeCLI(t, importedURL, []string{"--workspace", dir, "--json", "list"})
	afterShow := runSmokeCLI(t, importedURL, []string{"--workspace", dir, "--json", "show", "1"})

	assert.JSONEq(t, before, after)
	assert.JSONEq(t, beforeShow, afterShow)
}

func runSmokeCLI(t *testing.T, baseURL string, args []string) string {
	t.Helper()
	resetFlags(t)
	cmd := newRootCmd()
	var out bytes.Buffer
	cmd.SetOut(&out)
	cmd.SetArgs(args)
	cmd.SetContext(contextWithBaseURL(context.Background(), baseURL))
	require.NoError(t, cmd.Execute())
	return out.String()
}

func startSmokeDaemonForDB(t *testing.T, path string) string {
	t.Helper()
	d, err := db.Open(context.Background(), path)
	require.NoError(t, err)
	t.Cleanup(func() { _ = d.Close() })
	l, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
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
	t.Cleanup(func() {
		cancel()
		<-done
	})
	url := "http://" + l.Addr().String()
	waitSmokeDaemon(t, url)
	return url
}

func waitSmokeDaemon(t *testing.T, url string) {
	t.Helper()
	client := &http.Client{Timeout: 2 * time.Second}
	deadline := time.Now().Add(2 * time.Second)
	var lastErr error
	for time.Now().Before(deadline) {
		resp, err := client.Get(url + "/api/v1/ping") //nolint:noctx // short test readiness poll
		if err == nil {
			var body struct {
				OK bool `json:"ok"`
			}
			lastErr = json.NewDecoder(resp.Body).Decode(&body)
			_ = resp.Body.Close()
			if resp.StatusCode == http.StatusOK && body.OK && lastErr == nil {
				return
			}
		} else {
			lastErr = err
		}
		time.Sleep(20 * time.Millisecond)
	}
	require.NoError(t, lastErr)
}
