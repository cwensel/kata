package hooks

import (
	"bytes"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"sync"
	"testing"
)

func TestPrune_StartupSeed_TotalsBytesInDir(t *testing.T) {
	dir := t.TempDir()
	mustWrite(t, filepath.Join(dir, "1.0.out"), 100)
	mustWrite(t, filepath.Join(dir, "1.0.err"), 50)
	mustWrite(t, filepath.Join(dir, "2.0.out"), 200)
	p := newPruner(dir, 1024, log.New(&bytes.Buffer{}, "", 0))
	if err := p.Seed(); err != nil {
		t.Fatal(err)
	}
	if got := p.Total(); got != 350 {
		t.Fatalf("seed total = %d, want 350", got)
	}
}

func TestPrune_MaybeSweep_OldestGroupFirst(t *testing.T) {
	dir := t.TempDir()
	mustWrite(t, filepath.Join(dir, "10.0.out"), 100)
	mustWrite(t, filepath.Join(dir, "10.0.err"), 50)
	mustWrite(t, filepath.Join(dir, "20.0.out"), 100)
	mustWrite(t, filepath.Join(dir, "20.0.err"), 50)
	mustWrite(t, filepath.Join(dir, "30.0.out"), 100)
	mustWrite(t, filepath.Join(dir, "30.0.err"), 50)
	logBuf := &bytes.Buffer{}
	p := newPruner(dir, 250, log.New(logBuf, "", 0))
	if err := p.Seed(); err != nil {
		t.Fatal(err)
	}
	p.MaybeSweep()
	// 450 -> cap 250 -> must delete oldest groups (10.0 and 20.0) leaving 150.
	if _, err := os.Stat(filepath.Join(dir, "10.0.out")); err == nil {
		t.Fatal("oldest group should have been deleted")
	}
	if _, err := os.Stat(filepath.Join(dir, "30.0.out")); err != nil {
		t.Fatalf("newest group must survive: %v", err)
	}
}

func TestPrune_AtomicGroup_DeletesOutAndErrTogether(t *testing.T) {
	dir := t.TempDir()
	mustWrite(t, filepath.Join(dir, "10.0.out"), 100)
	mustWrite(t, filepath.Join(dir, "10.0.err"), 50)
	mustWrite(t, filepath.Join(dir, "20.0.out"), 100)
	mustWrite(t, filepath.Join(dir, "20.0.err"), 50)
	p := newPruner(dir, 100, log.New(&bytes.Buffer{}, "", 0))
	if err := p.Seed(); err != nil {
		t.Fatal(err)
	}
	p.MaybeSweep()
	if _, err := os.Stat(filepath.Join(dir, "10.0.out")); err == nil {
		t.Fatal("10.0.out should be gone")
	}
	if _, err := os.Stat(filepath.Join(dir, "10.0.err")); err == nil {
		t.Fatal("10.0.err should also be gone (atomic group delete)")
	}
}

func TestPrune_PartialGroup_NotFatal(t *testing.T) {
	dir := t.TempDir()
	// Only .out exists for this group; .err is missing.
	mustWrite(t, filepath.Join(dir, "10.0.out"), 100)
	mustWrite(t, filepath.Join(dir, "20.0.out"), 100)
	mustWrite(t, filepath.Join(dir, "20.0.err"), 50)
	p := newPruner(dir, 100, log.New(&bytes.Buffer{}, "", 0))
	if err := p.Seed(); err != nil {
		t.Fatal(err)
	}
	p.MaybeSweep()
	if _, err := os.Stat(filepath.Join(dir, "10.0.out")); err == nil {
		t.Fatal("partial group should still be eligible for delete")
	}
}

func TestPrune_AddAfterRun_TriggersSweep(t *testing.T) {
	dir := t.TempDir()
	p := newPruner(dir, 100, log.New(&bytes.Buffer{}, "", 0))
	if err := p.Seed(); err != nil {
		t.Fatal(err)
	}
	mustWrite(t, filepath.Join(dir, "1.0.out"), 80)
	mustWrite(t, filepath.Join(dir, "1.0.err"), 0)
	p.AddRun(1, 0, 80, 0)
	mustWrite(t, filepath.Join(dir, "2.0.out"), 80)
	mustWrite(t, filepath.Join(dir, "2.0.err"), 0)
	p.AddRun(2, 0, 80, 0)
	// Total now 160 over cap 100 -> after second AddRun, sweep should run
	// and delete oldest (1.0) leaving 80.
	if _, err := os.Stat(filepath.Join(dir, "1.0.out")); err == nil {
		t.Fatal("oldest run should have been pruned by AddRun-triggered sweep")
	}
}

// TestPrune_SkipsActiveGroup pins the contract that MaybeSweep never
// unlinks an in-flight group's .out/.err. Marking 10.0 active blocks
// the oldest group from being deleted; the next-oldest is taken
// instead even though it lives newer in the (event_id, hook_index)
// ordering.
func TestPrune_SkipsActiveGroup(t *testing.T) {
	dir := t.TempDir()
	mustWrite(t, filepath.Join(dir, "10.0.out"), 100)
	mustWrite(t, filepath.Join(dir, "10.0.err"), 50)
	mustWrite(t, filepath.Join(dir, "20.0.out"), 100)
	mustWrite(t, filepath.Join(dir, "20.0.err"), 50)
	mustWrite(t, filepath.Join(dir, "30.0.out"), 100)
	mustWrite(t, filepath.Join(dir, "30.0.err"), 50)
	p := newPruner(dir, 200, log.New(&bytes.Buffer{}, "", 0))
	if err := p.Seed(); err != nil {
		t.Fatal(err)
	}
	p.SetActiveCheck(func(k groupKey) bool {
		return k.eventID == 10
	})
	p.MaybeSweep()
	if _, err := os.Stat(filepath.Join(dir, "10.0.out")); err != nil {
		t.Fatalf("active group 10.0 must be preserved: %v", err)
	}
	if _, err := os.Stat(filepath.Join(dir, "20.0.out")); err == nil {
		t.Fatal("inactive next-oldest 20.0 should have been deleted")
	}
}

// TestPrune_StaleScan_NoDoubleDecrement guards the
// removeStreamLocked accounting: when a file disappears between scan
// and remove (stat-locked path), p.total must not be decremented for
// it. Hand-rolled by faking a stale groupInfo whose file never existed.
func TestPrune_StaleScan_NoDoubleDecrement(t *testing.T) {
	dir := t.TempDir()
	mustWrite(t, filepath.Join(dir, "1.0.out"), 100)
	p := newPruner(dir, 1024, log.New(&bytes.Buffer{}, "", 0))
	if err := p.Seed(); err != nil {
		t.Fatal(err)
	}
	startTotal := p.Total()
	stale := groupInfo{
		key:     groupKey{eventID: 999, hookIndex: 0},
		outPath: filepath.Join(dir, "999.0.out"),
		outSize: 1234,
	}
	p.mu.Lock()
	p.removeStreamLocked(stale.outPath, stale.outSize)
	p.mu.Unlock()
	if got := p.Total(); got != startTotal {
		t.Fatalf("stale missing-file delete decremented total: %d -> %d", startTotal, got)
	}
}

// TestPrune_ConcurrentSweep_TotalMatchesDisk pins the spec contract
// that two finishers calling MaybeSweep concurrently never corrupt the
// running total. The deletion phase is serialized under p.mu, so the
// post-condition is that p.total equals the sum of bytes still on disk.
func TestPrune_ConcurrentSweep_TotalMatchesDisk(t *testing.T) {
	dir := t.TempDir()
	const groups = 12
	const perStream = 100
	for i := 1; i <= groups; i++ {
		mustWrite(t, filepath.Join(dir, fmt.Sprintf("%d.0.out", i)), perStream)
		mustWrite(t, filepath.Join(dir, fmt.Sprintf("%d.0.err", i)), perStream)
	}
	p := newPruner(dir, 400, log.New(&bytes.Buffer{}, "", 0))
	if err := p.Seed(); err != nil {
		t.Fatal(err)
	}
	var wg sync.WaitGroup
	for w := 0; w < 4; w++ {
		wg.Add(1)
		go func() { defer wg.Done(); p.MaybeSweep() }()
	}
	wg.Wait()

	var diskBytes int64
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range entries {
		info, err := e.Info()
		if err != nil {
			t.Fatal(err)
		}
		diskBytes += info.Size()
	}
	if got := p.Total(); got != diskBytes {
		t.Fatalf("p.Total()=%d, disk=%d (concurrent sweepers got out of sync)", got, diskBytes)
	}
	if diskBytes > 400 {
		t.Fatalf("disk bytes %d > cap 400 after sweeps", diskBytes)
	}
}

func mustWrite(t *testing.T, path string, n int) {
	t.Helper()
	if err := os.WriteFile(path, bytes.Repeat([]byte("x"), n), 0o600); err != nil {
		t.Fatal(err)
	}
}
