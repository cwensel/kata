//go:build !windows

package hooks

import (
	"context"
	"os/exec"
	"testing"
	"time"
)

// TestRunner_KillTree_OrphanedChildrenDieToo pins that timeout/shutdown
// cleanup escalates to SIGKILL on the entire process group, even when
// the leader has already exited. spawn-orphan forks a child that
// ignores SIGTERM and outlives the parent's quick exit; the runner
// must still tear that child down via the group SIGKILL.
func TestRunner_KillTree_OrphanedChildrenDieToo(t *testing.T) {
	rs := newRunnerSetup(t)
	bin := hookprobePath(t)
	var got runRecord
	rs.deps.AppendRun = func(r runRecord) { got = r }
	job := HookJob{
		Event: sampleEvent("issue.created"),
		Hook: ResolvedHook{
			Index:      11,
			Command:    bin,
			Args:       []string{"spawn-orphan", "30s"},
			Timeout:    100 * time.Millisecond,
			WorkingDir: rs.dir,
		},
	}
	rs.deps.GraceWindow = 100 * time.Millisecond
	start := time.Now()
	runJob(context.Background(), make(chan struct{}), job, rs.deps)
	if got.Result != "ok" && got.Result != "timed_out" {
		// Parent exits 0 quickly so result depends on race with timer.
		t.Fatalf("result = %q, want ok or timed_out", got.Result)
	}
	// Bound: full kill sequence (parent exit + grace + SIGKILL) must
	// finish well under 5s. The orphaned 30s child would blow this if
	// we didn't reach it.
	if elapsed := time.Since(start); elapsed > 5*time.Second {
		t.Fatalf("runJob took %s, suggests orphan not killed", elapsed)
	}
}

// TestGroupAlive_NoProcess verifies that groupAlive returns false when
// Process is nil so waitGroupGone exits promptly without polling.
func TestGroupAlive_NoProcess(t *testing.T) {
	cmd := exec.Command("true")
	// Don't Start: cmd.Process is nil.
	if groupAlive(cmd) {
		t.Fatal("groupAlive should be false when Process is nil")
	}
}
