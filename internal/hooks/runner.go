package hooks

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io/fs"
	"log"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"time"

	"github.com/wesm/kata/internal/db"
)

// runRecord is the JSONL line shape for runs.jsonl. The dispatcher's
// runs appender (Task 6) marshals these.
type runRecord struct {
	Version          int    `json:"kata_hook_runs_version"`
	EventID          int64  `json:"event_id"`
	EventType        string `json:"event_type"`
	HookIndex        int    `json:"hook_index"`
	HookCommand      string `json:"hook_command"`
	StartedAt        string `json:"started_at"`
	EndedAt          string `json:"ended_at"`
	DurationMS       int64  `json:"duration_ms"`
	ExitCode         int    `json:"exit_code"`
	Result           string `json:"result"`
	StdoutPath       string `json:"stdout_path"`
	StderrPath       string `json:"stderr_path"`
	StdoutBytes      int64  `json:"stdout_bytes"`
	StderrBytes      int64  `json:"stderr_bytes"`
	SpawnError       string `json:"spawn_error"`
	PayloadTruncated bool   `json:"payload_truncated"`
}

// runDeps is what runJob needs from its caller. The dispatcher (Task 8)
// fills these from DispatcherDeps + its own per-instance state.
type runDeps struct {
	OutputDir   string
	KataHome    string
	DBHash      string
	DaemonLog   *log.Logger
	Now         func() time.Time
	GraceWindow time.Duration
	Project     projectResolver
	Issue       issueResolver
	Comment     commentResolver
	Alias       aliasResolver
	AppendRun   func(runRecord)
}

func runJob(ctx context.Context, shutdown <-chan struct{}, job HookJob, deps runDeps) {
	startedAt := deps.Now()
	outPath := filepath.Join(deps.OutputDir, fmt.Sprintf("%d.%d.out", job.Event.ID, job.Hook.Index))
	errPath := filepath.Join(deps.OutputDir, fmt.Sprintf("%d.%d.err", job.Event.ID, job.Hook.Index))

	outFile, oErr := os.OpenFile(outPath, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o600) //nolint:gosec // G304: path is OutputDir + int64.int filename, daemon-controlled
	errFile, eErr := os.OpenFile(errPath, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o600) //nolint:gosec // G304: path is OutputDir + int64.int filename, daemon-controlled
	if oErr != nil || eErr != nil {
		if outFile != nil {
			_ = outFile.Close()
		}
		if errFile != nil {
			_ = errFile.Close()
		}
		deps.AppendRun(runRecord{
			Version:     1,
			EventID:     job.Event.ID,
			EventType:   job.Event.Type,
			HookIndex:   job.Hook.Index,
			HookCommand: job.Hook.Command,
			StartedAt:   startedAt.UTC().Format(time.RFC3339Nano),
			EndedAt:     deps.Now().UTC().Format(time.RFC3339Nano),
			ExitCode:    -1,
			Result:      "spawn_failed",
			SpawnError:  fmt.Sprintf("open output files: out=%v err=%v", oErr, eErr),
		})
		return
	}
	defer func() { _ = outFile.Close() }()
	defer func() { _ = errFile.Close() }()

	recordRunWithFiles := func(result, spawnErr string, exitCode int, payloadTruncated bool) {
		_ = outFile.Close()
		_ = errFile.Close()
		var outBytes, errBytes int64
		if st, err := os.Stat(outPath); err == nil {
			outBytes = st.Size()
		}
		if st, err := os.Stat(errPath); err == nil {
			errBytes = st.Size()
		}
		ended := deps.Now()
		deps.AppendRun(runRecord{
			Version:          1,
			EventID:          job.Event.ID,
			EventType:        job.Event.Type,
			HookIndex:        job.Hook.Index,
			HookCommand:      job.Hook.Command,
			StartedAt:        startedAt.UTC().Format(time.RFC3339Nano),
			EndedAt:          ended.UTC().Format(time.RFC3339Nano),
			DurationMS:       ended.Sub(startedAt).Milliseconds(),
			ExitCode:         exitCode,
			Result:           result,
			StdoutPath:       outPath,
			StderrPath:       errPath,
			StdoutBytes:      outBytes,
			StderrBytes:      errBytes,
			SpawnError:       spawnErr,
			PayloadTruncated: payloadTruncated,
		})
	}

	if st, e := os.Stat(job.Hook.WorkingDir); e != nil {
		if errors.Is(e, fs.ErrNotExist) {
			recordRunWithFiles("working_dir_missing", e.Error(), -1, false)
			return
		}
		recordRunWithFiles("spawn_failed", e.Error(), -1, false)
		return
	} else if !st.IsDir() {
		recordRunWithFiles("spawn_failed", "working_dir is not a directory", -1, false)
		return
	}

	logf := func(format string, args ...any) { deps.DaemonLog.Printf(format, args...) }
	stdinPayload, payloadTruncated := buildStdinJSON(ctx, job.Event, deps.Project, deps.Issue, deps.Comment, deps.Alias, logf)

	cmd := exec.Command(job.Hook.Command, job.Hook.Args...) //nolint:gosec // G204: command validated at config load
	cmd.Dir = job.Hook.WorkingDir
	cmd.Env = buildEnv(job.Hook.UserEnv, job.Event, deps)
	cmd.Stdin = bytes.NewReader(stdinPayload)
	cmd.Stdout = outFile
	cmd.Stderr = errFile
	applyProcessGroupAttrs(cmd)

	if e := cmd.Start(); e != nil {
		recordRunWithFiles("spawn_failed", e.Error(), -1, payloadTruncated)
		return
	}

	doneCh := make(chan error, 1)
	go func() { doneCh <- cmd.Wait() }()

	timer := time.NewTimer(job.Hook.Timeout)
	defer timer.Stop()
	var (
		result   string
		exitCode int
	)
	select {
	case e := <-doneCh:
		result = "ok"
		exitCode = exitCodeOf(e)
	case <-timer.C:
		result = "timed_out"
		killTreeWithGrace(cmd, deps.GraceWindow, deps.DaemonLog)
		w := <-doneCh
		exitCode = exitCodeOf(w)
	case <-shutdown:
		result = "daemon_shutdown"
		killTreeWithGrace(cmd, deps.GraceWindow, deps.DaemonLog)
		w := <-doneCh
		exitCode = exitCodeOf(w)
	}

	recordRunWithFiles(result, "", exitCode, payloadTruncated)
}

func exitCodeOf(err error) int {
	if err == nil {
		return 0
	}
	var ee *exec.ExitError
	if errors.As(err, &ee) {
		return ee.ExitCode()
	}
	return -1
}

func buildEnv(userEnv []string, evt db.Event, deps runDeps) []string {
	env := append([]string{}, os.Environ()...)
	env = append(env, userEnv...)
	env = append(env,
		"KATA_HOOK_VERSION=1",
		"KATA_EVENT_ID="+strconv.FormatInt(evt.ID, 10),
		"KATA_EVENT_TYPE="+evt.Type,
		"KATA_ACTOR="+evt.Actor,
		"KATA_CREATED_AT="+evt.CreatedAt.UTC().Format(time.RFC3339Nano),
		"KATA_PROJECT_ID="+strconv.FormatInt(evt.ProjectID, 10),
		"KATA_PROJECT_IDENTITY="+evt.ProjectIdentity,
	)
	if evt.IssueNumber != nil {
		env = append(env, "KATA_ISSUE_NUMBER="+strconv.FormatInt(*evt.IssueNumber, 10))
	}
	if asnap, has, err := deps.Alias(evt); err == nil && has {
		env = append(env,
			"KATA_ALIAS_IDENTITY="+asnap.Identity,
			"KATA_ROOT_PATH="+asnap.RootPath,
		)
	}
	return env
}

func killTreeWithGrace(cmd *exec.Cmd, grace time.Duration, daemonLog *log.Logger) {
	if cmd.Process == nil {
		return
	}
	if err := signalGroup(cmd, syscallSIGTERM()); err != nil {
		daemonLog.Printf("hooks: SIGTERM: %v", err)
	}
	timer := time.NewTimer(grace)
	defer timer.Stop()
	exited := make(chan struct{})
	go func() {
		for {
			if !processAlive(cmd.Process.Pid) {
				close(exited)
				return
			}
			time.Sleep(10 * time.Millisecond)
		}
	}()
	select {
	case <-timer.C:
		if err := signalGroup(cmd, syscallSIGKILL()); err != nil {
			daemonLog.Printf("hooks: SIGKILL: %v", err)
		}
	case <-exited:
	}
}

func processAlive(pid int) bool {
	p, err := os.FindProcess(pid)
	if err != nil {
		return false
	}
	return p.Signal(syscallSignal0()) == nil
}
