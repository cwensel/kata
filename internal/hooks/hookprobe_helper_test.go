package hooks

import (
	"bytes"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
)

var (
	hookprobeOnce sync.Once
	hookprobeBin  string
	hookprobeErr  error
)

// hookprobePath returns the absolute path to the built hookprobe binary,
// building it on first call. Subsequent calls reuse the same binary.
func hookprobePath(t testing.TB) string {
	t.Helper()
	hookprobeOnce.Do(func() {
		// Not t.TempDir(): cleanup must outlive whichever test wins the sync.Once
		// race; TestMain handles removal explicitly below.
		dir, err := os.MkdirTemp("", "hookprobe-")
		if err != nil {
			hookprobeErr = err
			return
		}
		out := filepath.Join(dir, "hookprobe")
		// Test-only build of an in-tree helper; args are constants.
		cmd := exec.Command("go", "build", "-o", out, "./hookprobe") //nolint:gosec // test build
		var stderr bytes.Buffer
		cmd.Stderr = &stderr
		if err := cmd.Run(); err != nil {
			hookprobeErr = fmt.Errorf("go build ./hookprobe: %w: %s", err, stderr.String())
			return
		}
		hookprobeBin = out
	})
	if hookprobeErr != nil {
		t.Fatalf("build hookprobe: %v", hookprobeErr)
	}
	return hookprobeBin
}

// TestMain cleans up the cached binary directory.
func TestMain(m *testing.M) {
	code := m.Run()
	if hookprobeBin != "" {
		_ = os.RemoveAll(filepath.Dir(hookprobeBin))
	}
	os.Exit(code)
}

func TestHookprobe_StdinEcho(t *testing.T) {
	bin := hookprobePath(t)
	cmd := exec.Command(bin, "stdin") //nolint:gosec // bin is the test-built helper
	cmd.Stdin = strings.NewReader("hello\n")
	var out bytes.Buffer
	cmd.Stdout = &out
	if err := cmd.Run(); err != nil {
		t.Fatal(err)
	}
	if out.String() != "hello\n" {
		t.Fatalf("stdin echo = %q, want %q", out.String(), "hello\n")
	}
}

func TestHookprobe_ExitCode(t *testing.T) {
	bin := hookprobePath(t)
	cmd := exec.Command(bin, "exit", "7") //nolint:gosec // bin is the test-built helper
	err := cmd.Run()
	exit := exitCode(t, err)
	if exit != 7 {
		t.Fatalf("exit code = %d, want 7", exit)
	}
}

func TestHookprobe_EnvKey(t *testing.T) {
	bin := hookprobePath(t)
	cmd := exec.Command(bin, "env", "KATA_TEST_X") //nolint:gosec // bin is the test-built helper
	cmd.Env = append(os.Environ(), "KATA_TEST_X=hello-world")
	var out bytes.Buffer
	cmd.Stdout = &out
	if err := cmd.Run(); err != nil {
		t.Fatal(err)
	}
	if strings.TrimSpace(out.String()) != "hello-world" {
		t.Fatalf("env value = %q, want %q", out.String(), "hello-world")
	}
}

func TestHookprobe_Both(t *testing.T) {
	bin := hookprobePath(t)
	cmd := exec.Command(bin, "both", "outline", "errline") //nolint:gosec // bin is the test-built helper
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(stdout.String(), "outline") {
		t.Fatalf("stdout = %q, want outline", stdout.String())
	}
	if !strings.Contains(stderr.String(), "errline") {
		t.Fatalf("stderr = %q, want errline", stderr.String())
	}
}

func exitCode(t testing.TB, err error) int {
	t.Helper()
	if err == nil {
		return 0
	}
	var ee *exec.ExitError
	if errors.As(err, &ee) {
		return ee.ExitCode()
	}
	t.Fatalf("not an exit error: %v", err)
	return -1
}
