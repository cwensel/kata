package main

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/spf13/cobra"
	"github.com/wesm/kata/internal/config"
)

// daemonLogsCmd registers `kata daemon logs --hooks ...`. The --hooks
// flag is required in v1; future log streams (broadcaster, audit) can
// be selected by additional flags without breaking the command shape.
func daemonLogsCmd() *cobra.Command {
	var (
		hooks      bool
		tail       bool
		limit      int
		failedOnly bool
		eventType  string
		hookIndex  int
	)
	cmd := &cobra.Command{
		Use:   "logs",
		Short: "read daemon logs (hook runs)",
		RunE: func(cmd *cobra.Command, _ []string) error {
			if !hooks {
				return &cliError{ExitCode: ExitUsage, Message: "currently only --hooks is supported"}
			}
			f := &hookLogFilter{failedOnly: failedOnly, eventType: eventType, hookIndex: hookIndex}
			if tail {
				return runHookLogTail(cmd.Context(), cmd.OutOrStdout(), cmd.ErrOrStderr(), limit, f)
			}
			return runHookLogOnce(cmd.OutOrStdout(), cmd.ErrOrStderr(), limit, f)
		},
	}
	cmd.Flags().BoolVar(&hooks, "hooks", false, "show hook run logs")
	cmd.Flags().BoolVar(&tail, "tail", false, "follow the active runs.jsonl")
	cmd.Flags().IntVar(&limit, "limit", 100, "maximum matching records (default 100)")
	cmd.Flags().BoolVar(&failedOnly, "failed-only", false, "show only failed runs (result != ok || exit_code != 0)")
	cmd.Flags().StringVar(&eventType, "event-type", "", "filter by event type")
	cmd.Flags().IntVar(&hookIndex, "hook-index", -1, "filter by hook index (-1 = all)")
	return cmd
}

type hookLogFilter struct {
	failedOnly bool
	eventType  string
	hookIndex  int
}

func (f *hookLogFilter) accept(line []byte) (json.RawMessage, bool) {
	var rec map[string]json.RawMessage
	if err := json.Unmarshal(line, &rec); err != nil {
		return nil, false
	}
	if f.failedOnly && isOK(rec) {
		return nil, false
	}
	if f.eventType != "" && jsonString(rec, "event_type") != f.eventType {
		return nil, false
	}
	if f.hookIndex >= 0 && jsonInt(rec, "hook_index") != f.hookIndex {
		return nil, false
	}
	return json.RawMessage(line), true
}

// isOK returns true when result == "ok" and exit_code == 0.
func isOK(rec map[string]json.RawMessage) bool {
	return jsonString(rec, "result") == "ok" && jsonInt(rec, "exit_code") == 0
}

func jsonString(rec map[string]json.RawMessage, key string) string {
	var s string
	_ = json.Unmarshal(rec[key], &s)
	return s
}

func jsonInt(rec map[string]json.RawMessage, key string) int {
	var n int
	_ = json.Unmarshal(rec[key], &n)
	return n
}

// runHookLogOnce reads runs.jsonl.K → runs.jsonl in chronological order
// and prints up to limit matching records (the *last* limit, not the
// first — most recent failures are usually what the operator wants).
func runHookLogOnce(stdout, stderr io.Writer, limit int, f *hookLogFilter) error {
	files, err := orderedRunsFiles()
	if err != nil {
		return err
	}
	var matches []string
	for _, p := range files {
		m, err := readMatchesFromFile(p, stderr, f)
		if err != nil {
			return err
		}
		matches = append(matches, m...)
	}
	start := 0
	if limit > 0 && len(matches) > limit {
		start = len(matches) - limit
	}
	for _, m := range matches[start:] {
		writeLine(stdout, m)
	}
	return nil
}

// writeLine emits one log record + newline as raw bytes. Routed
// through io.Writer.Write rather than fmt.Fprintln so gosec's XSS
// taint analyzer (G705) doesn't flag file-tainted bytes traveling
// through fmt formatting verbs.
func writeLine(w io.Writer, s string) {
	_, _ = w.Write([]byte(s))
	_, _ = w.Write([]byte{'\n'})
}

// readMatchesFromFile reads one file and returns its matching records.
// Missing-file is not an error (rotation can race with read).
func readMatchesFromFile(path string, stderr io.Writer, f *hookLogFilter) ([]string, error) {
	fh, err := os.Open(path) //nolint:gosec // G304: path is daemon-controlled state-dir filename
	if errors.Is(err, fs.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	defer func() { _ = fh.Close() }()
	var matches []string
	scanner := bufio.NewScanner(fh)
	scanner.Buffer(make([]byte, 0, 64*1024), 4*1024*1024)
	lineNo := 0
	for scanner.Scan() {
		lineNo++
		line := scanner.Bytes()
		if len(bytes.TrimSpace(line)) == 0 {
			continue
		}
		rec, ok := f.accept(append([]byte(nil), line...))
		if !ok {
			if !json.Valid(line) {
				_, _ = fmt.Fprintf(stderr, "kata: skipping malformed line %d in %s\n", lineNo, path)
			}
			continue
		}
		matches = append(matches, string(rec))
	}
	if err := scanner.Err(); err != nil {
		return matches, fmt.Errorf("scanning %s: %w", path, err)
	}
	return matches, nil
}

// runHookLogTail prints the last `limit` matches from existing files,
// then follows the active runs.jsonl. Detects rotation by os.SameFile
// identity change or size shrink. When awaitActiveFile had to wait for
// runs.jsonl to appear, follow starts at offset 0 so the file's initial
// content (which runHookLogOnce missed) is emitted; otherwise follow
// starts at the file's current size to avoid double-emitting content
// that runHookLogOnce already printed.
func runHookLogTail(ctx context.Context, stdout, stderr io.Writer, limit int, f *hookLogFilter) error {
	if err := runHookLogOnce(stdout, stderr, limit, f); err != nil {
		return err
	}
	active, waited, err := awaitActiveFile(ctx)
	if err != nil || active == "" {
		return err
	}
	return followActive(ctx, stdout, stderr, active, waited, f)
}

// awaitActiveFile blocks until runs.jsonl exists (or ctx is done) and
// reports whether the call had to wait. waited=false means the file
// existed on the first stat (runHookLogOnce already saw it); waited=true
// means it appeared later and its current contents are unprinted.
//
// Specifically watches for runs.jsonl rather than "any file in the
// directory" — a rotated-only directory (runs.jsonl.N present, active
// missing) must NOT short-circuit, otherwise --tail would follow a
// rotated file that never gets new content.
func awaitActiveFile(ctx context.Context) (path string, waited bool, err error) {
	root, err := hookRunsRoot()
	if err != nil {
		return "", false, err
	}
	active := filepath.Join(root, "runs.jsonl")
	first := true
	for {
		switch _, statErr := os.Stat(active); {
		case statErr == nil:
			return active, !first, nil
		case !errors.Is(statErr, fs.ErrNotExist):
			return "", false, statErr
		}
		first = false
		select {
		case <-ctx.Done():
			return "", false, nil
		case <-time.After(200 * time.Millisecond):
		}
	}
}

// followState carries the polling state across followActive ticks.
type followState struct {
	prevSize int64
	prevInfo os.FileInfo
}

// followActive polls the active runs.jsonl, emitting newly appended
// matching records. Detects rotation via os.SameFile identity change
// or size shrink (rewinds prevSize to zero on rotation). When
// startAtZero is true, prevSize stays 0 so the file's existing content
// is emitted on the first tick — used when runs.jsonl appeared after
// runHookLogOnce ran and its initial bytes are still unprinted.
func followActive(ctx context.Context, stdout, stderr io.Writer, active string, startAtZero bool, f *hookLogFilter) error {
	st := &followState{}
	if info, err := os.Stat(active); err == nil {
		st.prevInfo = info
		if !startAtZero {
			st.prevSize = info.Size()
		}
	}
	for {
		select {
		case <-ctx.Done():
			return nil
		case <-time.After(200 * time.Millisecond):
		}
		if err := tailTick(active, stdout, stderr, st, f); err != nil {
			return err
		}
	}
}

// tailTick performs one poll cycle: stats the active file, detects
// rotation via SameFile change or size shrink, and emits any newly
// appended lines.
func tailTick(active string, stdout, stderr io.Writer, st *followState, f *hookLogFilter) error {
	info, err := os.Stat(active)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return nil
		}
		return err
	}
	if rotationDetected(st.prevInfo, info, st.prevSize) {
		st.prevSize = 0
	}
	st.prevInfo = info
	if info.Size() == st.prevSize {
		return nil
	}
	n, err := emitNewLines(active, st.prevSize, stdout, stderr, f)
	if err != nil {
		return err
	}
	st.prevSize += n
	return nil
}

// rotationDetected returns true when the active file changed identity
// or shrank, indicating a rotation that should reset prevSize to 0.
func rotationDetected(prev, current os.FileInfo, prevSize int64) bool {
	return prev == nil || current.Size() < prevSize || !os.SameFile(prev, current)
}

// emitNewLines reads from `from` to EOF and prints matching records.
// Returns the number of bytes consumed by *complete* lines (those
// ending in '\n'). A partial trailing line — possible when the writer's
// final write hasn't landed yet — is intentionally NOT counted, so the
// caller's offset stays at the start of the mid-line and a later tick
// can pick the record up once the writer flushes.
func emitNewLines(path string, from int64, stdout, stderr io.Writer, f *hookLogFilter) (int64, error) {
	fh, err := os.Open(path) //nolint:gosec // G304: path is daemon-controlled state-dir filename
	if err != nil {
		return 0, err
	}
	defer func() { _ = fh.Close() }()
	if _, err := fh.Seek(from, io.SeekStart); err != nil {
		return 0, err
	}
	reader := bufio.NewReaderSize(fh, 64*1024)
	var read int64
	lineNo := 0
	for {
		line, rerr := reader.ReadBytes('\n')
		if len(line) > 0 && line[len(line)-1] == '\n' {
			read += int64(len(line))
			lineNo++
			emitOne(line[:len(line)-1], lineNo, path, stdout, stderr, f)
		}
		if rerr != nil {
			if errors.Is(rerr, io.EOF) {
				return read, nil
			}
			return read, rerr
		}
	}
}

// emitOne is the per-record body shared by tail emission paths: skip
// blank lines, warn-and-skip malformed JSON, otherwise print matches.
func emitOne(content []byte, lineNo int, path string, stdout, stderr io.Writer, f *hookLogFilter) {
	if len(bytes.TrimSpace(content)) == 0 {
		return
	}
	rec, ok := f.accept(append([]byte(nil), content...))
	if !ok {
		if !json.Valid(content) {
			_, _ = fmt.Fprintf(stderr, "kata: skipping malformed line %d in %s\n", lineNo, path)
		}
		return
	}
	writeLine(stdout, string(rec))
}

// hookRunsRoot returns $KATA_HOME/hooks/<dbhash> for the active KATA_DB.
func hookRunsRoot() (string, error) {
	dbPath, err := config.KataDB()
	if err != nil {
		return "", err
	}
	return config.HookRootDir(config.DBHash(dbPath))
}

// orderedRunsFiles returns the rotated runs files plus the active file
// in chronological order: runs.jsonl.K (oldest) → runs.jsonl (newest).
func orderedRunsFiles() ([]string, error) {
	root, err := hookRunsRoot()
	if err != nil {
		return nil, err
	}
	return scanRunsFiles(root)
}

// rotatedRun is a parsed runs.jsonl.N entry: idx is N, path is the
// absolute filesystem path.
type rotatedRun struct {
	path string
	idx  int
}

func scanRunsFiles(root string) ([]string, error) {
	entries, err := os.ReadDir(root)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return nil, nil
		}
		return nil, err
	}
	rotateds, hasActive := classifyRunsEntries(root, entries)
	sort.Slice(rotateds, func(i, j int) bool { return rotateds[i].idx > rotateds[j].idx })
	out := make([]string, 0, len(rotateds)+1)
	for _, r := range rotateds {
		out = append(out, r.path)
	}
	if hasActive {
		out = append(out, filepath.Join(root, "runs.jsonl"))
	}
	return out, nil
}

// classifyRunsEntries splits a directory listing into rotated entries
// (runs.jsonl.N) and a hasActive flag for runs.jsonl. Anything else is
// ignored.
func classifyRunsEntries(root string, entries []os.DirEntry) ([]rotatedRun, bool) {
	var rotateds []rotatedRun
	var hasActive bool
	for _, e := range entries {
		name := e.Name()
		if name == "runs.jsonl" {
			hasActive = true
			continue
		}
		idx, ok := parseRotatedIndex(name)
		if !ok {
			continue
		}
		rotateds = append(rotateds, rotatedRun{path: filepath.Join(root, name), idx: idx})
	}
	return rotateds, hasActive
}

// parseRotatedIndex returns the numeric suffix of "runs.jsonl.N",
// or false if the name doesn't match.
func parseRotatedIndex(name string) (int, bool) {
	if !strings.HasPrefix(name, "runs.jsonl.") {
		return 0, false
	}
	idx, err := strconv.Atoi(strings.TrimPrefix(name, "runs.jsonl."))
	if err != nil {
		return 0, false
	}
	return idx, true
}
