package hooks

import (
	"bytes"
	"log"
	"os"
	"path/filepath"
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

func mustWrite(t *testing.T, path string, n int) {
	t.Helper()
	if err := os.WriteFile(path, bytes.Repeat([]byte("x"), n), 0o600); err != nil {
		t.Fatal(err)
	}
}
