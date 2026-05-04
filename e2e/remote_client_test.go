//go:build !windows

package e2e_test

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestRemoteClient_DaemonOnTCPClientViaKATA_SERVER spins up a daemon
// bound to TCP loopback under one KATA_HOME, then runs a client
// subprocess in a separate KATA_HOME and workspace pointed at that
// daemon via KATA_SERVER. Exercises init/create/list/close end-to-end
// and asserts the client never auto-started a local daemon.
func TestRemoteClient_DaemonOnTCPClientViaKATA_SERVER(t *testing.T) {
	if testing.Short() {
		t.Skip("e2e")
	}
	bin := buildKataBinary(t)

	// Pre-allocate a free port. We can't use --listen 127.0.0.1:0
	// because the runtime file is written before the daemon actually
	// binds — endpoint.Address() returns the literal "127.0.0.1:0".
	port := freeTCPPort(t)
	addr := fmt.Sprintf("127.0.0.1:%d", port)

	// Server-side state: its own KATA_HOME, runs the daemon.
	serverHome := t.TempDir()

	daemonStderr := &safeBuffer{}
	daemon := exec.Command(bin, "daemon", "start", "--listen", addr) //nolint:gosec
	daemon.Env = append(os.Environ(),
		"KATA_HOME="+serverHome,
		"KATA_DB="+filepath.Join(serverHome, "kata.db"),
	)
	daemon.Stdout = io.Discard
	daemon.Stderr = daemonStderr
	daemon.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	require.NoError(t, daemon.Start())
	t.Cleanup(func() {
		if t.Failed() {
			t.Logf("daemon stderr:\n%s", daemonStderr.String())
		}
	})
	t.Cleanup(func() { stopDaemon(daemon) })

	// Wait for /api/v1/ping to answer on the bound address.
	waitForPing(t, "http://"+addr, 5*time.Second)

	// Client-side state: separate KATA_HOME, separate workspace.
	clientHome := t.TempDir()
	clientWS := initRepo(t, "https://github.com/wesm/system.git")

	clientEnv := append(os.Environ(),
		"KATA_HOME="+clientHome,
		"KATA_DB="+filepath.Join(clientHome, "kata.db"),
		"KATA_SERVER=http://"+addr,
		"KATA_AUTHOR=e2e-bot",
	)

	// init through the remote.
	runRemoteCmd(t, bin, clientWS, clientEnv, "init")

	// create an issue.
	runRemoteCmd(t, bin, clientWS, clientEnv,
		"create", "hello from remote", "--body", "via KATA_SERVER")

	// list and confirm the issue is in the SERVER's DB.
	out := runRemoteCmdOutput(t, bin, clientWS, clientEnv, "list", "--json")
	assert.Contains(t, out, "hello from remote")

	// close it.
	runRemoteCmd(t, bin, clientWS, clientEnv, "close", "1", "--reason", "done")

	// Critical assertion: the client KATA_HOME has no runtime files.
	// If a local daemon had been auto-started on the client side,
	// we'd find a daemon.<pid>.json under <clientHome>/runtime/<dbhash>/.
	clientRuntime := filepath.Join(clientHome, "runtime")
	assertNoDaemonRuntimeFiles(t, clientRuntime)
}

// freeTCPPort binds 127.0.0.1:0, captures the bound port, and closes.
// There is a small race window before the caller binds again; accept it.
func freeTCPPort(t *testing.T) int {
	t.Helper()
	l, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	port := l.Addr().(*net.TCPAddr).Port
	require.NoError(t, l.Close())
	return port
}

// waitForPing polls /api/v1/ping until it answers 200 with {"ok": true}
// or the deadline expires.
func waitForPing(t *testing.T, base string, timeout time.Duration) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	client := &http.Client{Timeout: 250 * time.Millisecond}
	for time.Now().Before(deadline) {
		resp, err := client.Get(base + "/api/v1/ping") //nolint:noctx
		if err == nil {
			body, _ := io.ReadAll(resp.Body)
			_ = resp.Body.Close()
			if resp.StatusCode == http.StatusOK {
				var info struct {
					OK bool `json:"ok"`
				}
				if json.Unmarshal(body, &info) == nil && info.OK {
					return
				}
			}
		}
		time.Sleep(50 * time.Millisecond)
	}
	t.Fatalf("daemon at %s did not answer /api/v1/ping within %s", base, timeout)
}

// runRemoteCmd runs a kata subcommand and asserts success, dumping
// combined output on failure for debugging.
func runRemoteCmd(t *testing.T, bin, workdir string, env []string, args ...string) {
	t.Helper()
	cmd := exec.Command(bin, args...) //nolint:gosec
	cmd.Dir = workdir
	cmd.Env = env
	out, err := cmd.CombinedOutput()
	require.NoErrorf(t, err, "kata %s: %s", strings.Join(args, " "), out)
}

// runRemoteCmdOutput runs a kata subcommand, returns combined output,
// and asserts success.
func runRemoteCmdOutput(t *testing.T, bin, workdir string, env []string, args ...string) string {
	t.Helper()
	cmd := exec.Command(bin, args...) //nolint:gosec
	cmd.Dir = workdir
	cmd.Env = env
	out, err := cmd.CombinedOutput()
	require.NoErrorf(t, err, "kata %s: %s", strings.Join(args, " "), out)
	return string(out)
}

// assertNoDaemonRuntimeFiles walks <dir> and fails if any daemon.<pid>.json
// is present. Missing dir is a pass (no daemon was started).
func assertNoDaemonRuntimeFiles(t *testing.T, dir string) {
	t.Helper()
	entries, err := os.ReadDir(dir)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return
		}
		t.Fatalf("reading client runtime dir %s: %v", dir, err)
	}
	for _, e := range entries {
		sub := filepath.Join(dir, e.Name())
		files, err := os.ReadDir(sub)
		if err != nil {
			continue
		}
		for _, f := range files {
			n := f.Name()
			if strings.HasPrefix(n, "daemon.") && strings.HasSuffix(n, ".json") {
				t.Errorf("client unexpectedly wrote runtime file %s/%s", sub, n)
			}
		}
	}
}
