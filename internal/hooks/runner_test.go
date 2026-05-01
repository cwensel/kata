package hooks

import (
	"context"
	"errors"
	"log"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

type runnerSetup struct {
	t      *testing.T
	deps   runDeps
	dir    string
	dbHash string
	logBuf *strings.Builder
}

func newRunnerSetup(t *testing.T) *runnerSetup {
	t.Helper()
	root := t.TempDir()
	t.Setenv("KATA_HOME", root)
	dbHash := "testdbhash01"
	outDir := filepath.Join(root, "hooks", dbHash, "output")
	if err := os.MkdirAll(outDir, 0o700); err != nil {
		t.Fatal(err)
	}
	logBuf := &strings.Builder{}
	logger := log.New(logBuf, "", 0)
	deps := runDeps{
		OutputDir:   outDir,
		KataHome:    root,
		DBHash:      dbHash,
		DaemonLog:   logger,
		Now:         func() time.Time { return time.Date(2026, 4, 30, 14, 22, 11, 0, time.UTC) },
		GraceWindow: 100 * time.Millisecond,
		Project:     okProject,
		Issue:       okIssue,
		Comment:     okComment,
		Alias:       okAlias,
		AppendRun:   func(_ runRecord) {},
	}
	return &runnerSetup{t: t, deps: deps, dir: root, dbHash: dbHash, logBuf: logBuf}
}

func TestRunner_OK_HookprobeStdin(t *testing.T) {
	rs := newRunnerSetup(t)
	bin := hookprobePath(t)
	var got runRecord
	rs.deps.AppendRun = func(r runRecord) { got = r }
	job := HookJob{
		Event:      sampleEvent("issue.created"),
		Hook:       ResolvedHook{Index: 0, Command: bin, Args: []string{"stdin"}, Timeout: 2 * time.Second, WorkingDir: rs.dir},
		EnqueuedAt: rs.deps.Now(),
	}
	runJob(context.Background(), make(chan struct{}), job, rs.deps)
	if got.Result != "ok" {
		t.Fatalf("result = %q, want ok (log=%s)", got.Result, rs.logBuf.String())
	}
	if got.ExitCode != 0 {
		t.Fatalf("exit_code = %d, want 0", got.ExitCode)
	}
	stdoutBytes, err := os.ReadFile(got.StdoutPath)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(stdoutBytes), `"event_id":81237`) {
		t.Fatalf("stdout missing event_id: %q", stdoutBytes)
	}
}

func TestRunner_NonzeroExit(t *testing.T) {
	rs := newRunnerSetup(t)
	bin := hookprobePath(t)
	var got runRecord
	rs.deps.AppendRun = func(r runRecord) { got = r }
	job := HookJob{
		Event: sampleEvent("issue.created"),
		Hook:  ResolvedHook{Index: 1, Command: bin, Args: []string{"exit", "7"}, Timeout: 2 * time.Second, WorkingDir: rs.dir},
	}
	runJob(context.Background(), make(chan struct{}), job, rs.deps)
	if got.Result != "ok" || got.ExitCode != 7 {
		t.Fatalf("got %+v, want result=ok exit_code=7", got)
	}
}

func TestRunner_SpawnFailed_NonexistentCommand(t *testing.T) {
	rs := newRunnerSetup(t)
	var got runRecord
	rs.deps.AppendRun = func(r runRecord) { got = r }
	job := HookJob{
		Event: sampleEvent("issue.created"),
		Hook:  ResolvedHook{Index: 2, Command: "/nonexistent/no-such-binary", Timeout: 2 * time.Second, WorkingDir: rs.dir},
	}
	runJob(context.Background(), make(chan struct{}), job, rs.deps)
	if got.Result != "spawn_failed" {
		t.Fatalf("result = %q, want spawn_failed", got.Result)
	}
	if got.StdoutPath == "" || got.StderrPath == "" {
		t.Fatalf("paths should still be recorded: %+v", got)
	}
	if got.StdoutBytes != 0 || got.StderrBytes != 0 {
		t.Fatalf("byte counts should be 0 on spawn_failed: %+v", got)
	}
}

func TestRunner_WorkingDirMissing(t *testing.T) {
	rs := newRunnerSetup(t)
	bin := hookprobePath(t)
	var got runRecord
	rs.deps.AppendRun = func(r runRecord) { got = r }
	job := HookJob{
		Event: sampleEvent("issue.created"),
		Hook:  ResolvedHook{Index: 3, Command: bin, Args: []string{"exit", "0"}, Timeout: 2 * time.Second, WorkingDir: filepath.Join(rs.dir, "nope")},
	}
	runJob(context.Background(), make(chan struct{}), job, rs.deps)
	if got.Result != "working_dir_missing" {
		t.Fatalf("result = %q, want working_dir_missing", got.Result)
	}
}

func TestRunner_WorkingDirIsFile(t *testing.T) {
	rs := newRunnerSetup(t)
	bin := hookprobePath(t)
	wd := filepath.Join(rs.dir, "wd-as-file")
	if err := os.WriteFile(wd, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	var got runRecord
	rs.deps.AppendRun = func(r runRecord) { got = r }
	job := HookJob{
		Event: sampleEvent("issue.created"),
		Hook:  ResolvedHook{Index: 4, Command: bin, Args: []string{"exit", "0"}, Timeout: 2 * time.Second, WorkingDir: wd},
	}
	runJob(context.Background(), make(chan struct{}), job, rs.deps)
	if got.Result != "spawn_failed" {
		t.Fatalf("working_dir = file: result = %q, want spawn_failed", got.Result)
	}
}

func TestRunner_TimedOut_TermDelay(t *testing.T) {
	rs := newRunnerSetup(t)
	bin := hookprobePath(t)
	var got runRecord
	rs.deps.AppendRun = func(r runRecord) { got = r }
	job := HookJob{
		Event: sampleEvent("issue.created"),
		Hook:  ResolvedHook{Index: 5, Command: bin, Args: []string{"term-delay", "10ms"}, Timeout: 50 * time.Millisecond, WorkingDir: rs.dir},
	}
	rs.deps.GraceWindow = 200 * time.Millisecond
	runJob(context.Background(), make(chan struct{}), job, rs.deps)
	if got.Result != "timed_out" {
		t.Fatalf("result = %q, want timed_out", got.Result)
	}
}

func TestRunner_TimedOut_TermIgnore_Killed(t *testing.T) {
	rs := newRunnerSetup(t)
	bin := hookprobePath(t)
	var got runRecord
	rs.deps.AppendRun = func(r runRecord) { got = r }
	job := HookJob{
		Event: sampleEvent("issue.created"),
		Hook:  ResolvedHook{Index: 6, Command: bin, Args: []string{"term-ignore", "10s"}, Timeout: 50 * time.Millisecond, WorkingDir: rs.dir},
	}
	rs.deps.GraceWindow = 50 * time.Millisecond
	runJob(context.Background(), make(chan struct{}), job, rs.deps)
	if got.Result != "timed_out" {
		t.Fatalf("result = %q, want timed_out (SIGKILL fallback)", got.Result)
	}
}

func TestRunner_DaemonShutdown_BeforeWait(t *testing.T) {
	rs := newRunnerSetup(t)
	bin := hookprobePath(t)
	var got runRecord
	rs.deps.AppendRun = func(r runRecord) { got = r }
	job := HookJob{
		Event: sampleEvent("issue.created"),
		Hook:  ResolvedHook{Index: 7, Command: bin, Args: []string{"sleep", "1s"}, Timeout: 5 * time.Second, WorkingDir: rs.dir},
	}
	done := make(chan struct{})
	go func() {
		time.Sleep(50 * time.Millisecond)
		close(done)
	}()
	runJob(context.Background(), done, job, rs.deps)
	if got.Result != "daemon_shutdown" {
		t.Fatalf("result = %q, want daemon_shutdown", got.Result)
	}
}

func TestRunner_OutputCapture_BothStreams(t *testing.T) {
	rs := newRunnerSetup(t)
	bin := hookprobePath(t)
	var got runRecord
	rs.deps.AppendRun = func(r runRecord) { got = r }
	job := HookJob{
		Event: sampleEvent("issue.created"),
		Hook:  ResolvedHook{Index: 8, Command: bin, Args: []string{"both", "OUT", "ERR"}, Timeout: 2 * time.Second, WorkingDir: rs.dir},
	}
	runJob(context.Background(), make(chan struct{}), job, rs.deps)
	out, _ := os.ReadFile(got.StdoutPath)
	er, _ := os.ReadFile(got.StderrPath)
	if !strings.Contains(string(out), "OUT") {
		t.Fatalf(".out missing OUT: %q", out)
	}
	if !strings.Contains(string(er), "ERR") {
		t.Fatalf(".err missing ERR: %q", er)
	}
	if got.StdoutBytes != int64(len(out)) || got.StderrBytes != int64(len(er)) {
		t.Fatalf("recorded sizes don't match disk: %+v vs %d/%d", got, len(out), len(er))
	}
}

func TestRunner_EnvKataVars(t *testing.T) {
	rs := newRunnerSetup(t)
	bin := hookprobePath(t)
	var got runRecord
	rs.deps.AppendRun = func(r runRecord) { got = r }
	job := HookJob{
		Event: sampleEvent("issue.created"),
		Hook:  ResolvedHook{Index: 9, Command: bin, Args: []string{"env", "KATA_EVENT_ID"}, Timeout: 2 * time.Second, WorkingDir: rs.dir},
	}
	runJob(context.Background(), make(chan struct{}), job, rs.deps)
	out, _ := os.ReadFile(got.StdoutPath)
	if strings.TrimSpace(string(out)) != "81237" {
		t.Fatalf("KATA_EVENT_ID = %q, want 81237", out)
	}
}

func TestRunner_EnvUserOverridable_NotForKata(t *testing.T) {
	rs := newRunnerSetup(t)
	bin := hookprobePath(t)
	var got runRecord
	rs.deps.AppendRun = func(r runRecord) { got = r }
	job := HookJob{
		Event: sampleEvent("issue.created"),
		Hook: ResolvedHook{
			Index: 10, Command: bin, Args: []string{"env", "EXTRA"},
			Timeout: 2 * time.Second, WorkingDir: rs.dir,
			UserEnv: []string{"EXTRA=visible"},
		},
	}
	runJob(context.Background(), make(chan struct{}), job, rs.deps)
	out, _ := os.ReadFile(got.StdoutPath)
	if strings.TrimSpace(string(out)) != "visible" {
		t.Fatalf("user env not visible: %q", out)
	}
}

var _ = errors.New
