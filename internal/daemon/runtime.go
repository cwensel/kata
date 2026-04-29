package daemon

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"
)

// RuntimeRecord is the on-disk shape of daemon.<pid>.json.
type RuntimeRecord struct {
	PID       int       `json:"pid"`
	Address   string    `json:"address"` // unix:///path or 127.0.0.1:7474
	DBPath    string    `json:"db_path"`
	StartedAt time.Time `json:"started_at"`
}

// WriteRuntimeFile writes <dir>/daemon.<pid>.json atomically (write to .tmp,
// then rename). Returns the resolved file path.
func WriteRuntimeFile(dir string, rec RuntimeRecord) (string, error) {
	if rec.PID <= 0 {
		return "", fmt.Errorf("pid must be > 0")
	}
	final := filepath.Join(dir, fmt.Sprintf("daemon.%d.json", rec.PID))
	tmp := final + ".tmp"
	body, err := json.MarshalIndent(rec, "", "  ")
	if err != nil {
		return "", fmt.Errorf("marshal: %w", err)
	}
	if err := os.WriteFile(tmp, body, 0o644); err != nil { //nolint:gosec // runtime files are world-readable per §2.3
		return "", fmt.Errorf("write tmp: %w", err)
	}
	if err := os.Rename(tmp, final); err != nil {
		return "", fmt.Errorf("rename: %w", err)
	}
	return final, nil
}

// ReadRuntimeFile parses one file.
func ReadRuntimeFile(path string) (RuntimeRecord, error) {
	body, err := os.ReadFile(path) //nolint:gosec // path is a runtime file selected by the caller
	if err != nil {
		return RuntimeRecord{}, fmt.Errorf("read %s: %w", path, err)
	}
	var rec RuntimeRecord
	if err := json.Unmarshal(body, &rec); err != nil {
		return RuntimeRecord{}, fmt.Errorf("parse %s: %w", path, err)
	}
	return rec, nil
}

// ListRuntimeFiles returns RuntimeRecords for each daemon.*.json in dir.
// Garbage / parse-failed files are skipped silently.
func ListRuntimeFiles(dir string) ([]RuntimeRecord, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, fmt.Errorf("read dir: %w", err)
	}
	var out []RuntimeRecord
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		name := e.Name()
		if !strings.HasPrefix(name, "daemon.") || !strings.HasSuffix(name, ".json") {
			continue
		}
		// must parse the pid out of the filename to filter .tmp etc.
		mid := strings.TrimSuffix(strings.TrimPrefix(name, "daemon."), ".json")
		if _, err := strconv.Atoi(mid); err != nil {
			continue
		}
		rec, err := ReadRuntimeFile(filepath.Join(dir, name))
		if err != nil {
			continue
		}
		out = append(out, rec)
	}
	return out, nil
}

// ProcessAlive returns true if a kill(0, pid) succeeds. Best-effort signal
// probe; doesn't distinguish "not ours" vs "alive".
func ProcessAlive(pid int) bool {
	if pid <= 0 {
		return false
	}
	p, err := os.FindProcess(pid)
	if err != nil {
		return false
	}
	if err := p.Signal(syscall.Signal(0)); err != nil {
		return false
	}
	return true
}

// CleanupStaleFiles removes any daemon.<pid>.json whose PID is dead.
func CleanupStaleFiles(dir string) error {
	recs, err := ListRuntimeFiles(dir)
	if err != nil {
		return err
	}
	for _, r := range recs {
		if !ProcessAlive(r.PID) {
			_ = os.Remove(filepath.Join(dir, fmt.Sprintf("daemon.%d.json", r.PID)))
		}
	}
	return nil
}
