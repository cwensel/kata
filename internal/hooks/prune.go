package hooks

import (
	"errors"
	"fmt"
	"io/fs"
	"log"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
)

type pruner struct {
	mu        sync.Mutex
	dir       string
	capBytes  int64
	total     int64
	daemonLog *log.Logger
}

func newPruner(dir string, capBytes int64, daemonLog *log.Logger) *pruner {
	return &pruner{dir: dir, capBytes: capBytes, daemonLog: daemonLog}
}

// Seed walks the output dir once, totaling current bytes. Called at
// dispatcher startup.
func (p *pruner) Seed() error {
	var sum int64
	err := filepath.WalkDir(p.dir, func(_ string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			return nil
		}
		info, err := d.Info()
		if err != nil {
			return err
		}
		sum += info.Size()
		return nil
	})
	if err != nil && !errors.Is(err, fs.ErrNotExist) {
		return fmt.Errorf("seed pruner: %w", err)
	}
	p.mu.Lock()
	p.total = sum
	p.mu.Unlock()
	return nil
}

func (p *pruner) Total() int64 {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.total
}

// AddRun adds the byte counts produced by one finished run to the
// running total and, if over cap, triggers MaybeSweep.
func (p *pruner) AddRun(_ int64, _ int, outBytes, errBytes int64) {
	p.mu.Lock()
	p.total += outBytes + errBytes
	over := p.total > p.capBytes
	p.mu.Unlock()
	if over {
		p.MaybeSweep()
	}
}

type groupKey struct {
	eventID   int64
	hookIndex int
}

type groupInfo struct {
	key     groupKey
	outPath string
	errPath string
	outSize int64
	errSize int64
}

// MaybeSweep is a no-op if total <= cap. Otherwise it rescans the dir,
// sorts groups by (event_id, hook_index) ascending (oldest first),
// and deletes groups until total <= cap. Errors are logged but never
// returned.
func (p *pruner) MaybeSweep() {
	p.mu.Lock()
	if p.total <= p.capBytes {
		p.mu.Unlock()
		return
	}
	p.mu.Unlock()

	groups, err := scanGroups(p.dir)
	if err != nil {
		p.daemonLog.Printf("hooks: prune scan: %v", err)
		return
	}
	sort.Slice(groups, func(i, j int) bool {
		if groups[i].key.eventID != groups[j].key.eventID {
			return groups[i].key.eventID < groups[j].key.eventID
		}
		return groups[i].key.hookIndex < groups[j].key.hookIndex
	})

	p.mu.Lock()
	defer p.mu.Unlock()
	for _, g := range groups {
		if p.total <= p.capBytes {
			return
		}
		p.deleteGroupLocked(g)
	}
}

// deleteGroupLocked removes the .out and .err of one group. Caller
// holds p.mu so total accounting stays consistent. Missing files are
// silent (already gone is fine); other errors are logged.
func (p *pruner) deleteGroupLocked(g groupInfo) {
	if g.outPath != "" {
		if err := os.Remove(g.outPath); err != nil && !errors.Is(err, fs.ErrNotExist) {
			p.daemonLog.Printf("hooks: prune remove %s: %v", g.outPath, err)
		} else {
			p.total -= g.outSize
		}
	}
	if g.errPath != "" {
		if err := os.Remove(g.errPath); err != nil && !errors.Is(err, fs.ErrNotExist) {
			p.daemonLog.Printf("hooks: prune remove %s: %v", g.errPath, err)
		} else {
			p.total -= g.errSize
		}
	}
}

// parseStem splits "<event_id>.<hook_index>" into its key. Returns
// false for any name that doesn't match (caller skips silently).
func parseStem(stem string) (groupKey, bool) {
	dot := strings.IndexByte(stem, '.')
	if dot <= 0 {
		return groupKey{}, false
	}
	evID, err1 := strconv.ParseInt(stem[:dot], 10, 64)
	hookIdx, err2 := strconv.Atoi(stem[dot+1:])
	if err1 != nil || err2 != nil {
		return groupKey{}, false
	}
	return groupKey{eventID: evID, hookIndex: hookIdx}, true
}

// classifyEntry pulls the (.out|.err) suffix and the parsed key from a
// directory entry. The bool is false for unrelated files (no suffix
// or unparseable stem).
func classifyEntry(name string) (key groupKey, stream string, ok bool) {
	var stem string
	switch {
	case strings.HasSuffix(name, ".out"):
		stream = "out"
		stem = strings.TrimSuffix(name, ".out")
	case strings.HasSuffix(name, ".err"):
		stream = "err"
		stem = strings.TrimSuffix(name, ".err")
	default:
		return groupKey{}, "", false
	}
	k, ok := parseStem(stem)
	if !ok {
		return groupKey{}, "", false
	}
	return k, stream, true
}

// scanGroups lists output files and groups by (event_id, hook_index).
// Filenames are <event_id>.<hook_index>.{out,err}. Files that don't
// match the pattern are ignored.
func scanGroups(dir string) ([]groupInfo, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return nil, nil
		}
		return nil, err
	}
	byKey := make(map[groupKey]*groupInfo)
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		key, stream, ok := classifyEntry(e.Name())
		if !ok {
			continue
		}
		info, err := e.Info()
		if err != nil {
			continue
		}
		recordEntry(byKey, dir, e.Name(), key, stream, info.Size())
	}
	out := make([]groupInfo, 0, len(byKey))
	for _, g := range byKey {
		out = append(out, *g)
	}
	return out, nil
}

func recordEntry(byKey map[groupKey]*groupInfo, dir, name string, k groupKey, stream string, size int64) {
	g := byKey[k]
	if g == nil {
		g = &groupInfo{key: k}
		byKey[k] = g
	}
	full := filepath.Join(dir, name)
	if stream == "out" {
		g.outPath = full
		g.outSize = size
	} else {
		g.errPath = full
		g.errSize = size
	}
}
