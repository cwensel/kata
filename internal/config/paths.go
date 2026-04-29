// Package config resolves the kata data directory, database path, and
// per-database runtime namespace from environment variables.
package config

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"os"
	"path/filepath"
)

// KataHome returns the resolved data directory honoring $KATA_HOME, falling back
// to $HOME/.kata. The directory is not created here; callers materialize it.
func KataHome() (string, error) {
	if v := os.Getenv("KATA_HOME"); v != "" {
		return v, nil
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("resolve user home: %w", err)
	}
	return filepath.Join(home, ".kata"), nil
}

// KataDB returns the effective DB path honoring $KATA_DB, falling back to
// <KataHome>/kata.db. Returned path is not validated for existence.
func KataDB() (string, error) {
	if v := os.Getenv("KATA_DB"); v != "" {
		return v, nil
	}
	home, err := KataHome()
	if err != nil {
		return "", err
	}
	return filepath.Join(home, "kata.db"), nil
}

// DBHash returns the first 12 lower-hex chars of sha256(absolute(dbPath)).
// Used to namespace runtime files, sockets, and hook output per database.
func DBHash(dbPath string) string {
	abs, err := filepath.Abs(dbPath)
	if err != nil {
		abs = dbPath
	}
	sum := sha256.Sum256([]byte(abs))
	return hex.EncodeToString(sum[:])[:12]
}

// RuntimeDir returns <KataHome>/runtime/<dbhash>. The directory is not created.
func RuntimeDir() (string, error) {
	home, err := KataHome()
	if err != nil {
		return "", err
	}
	db, err := KataDB()
	if err != nil {
		return "", err
	}
	return filepath.Join(home, "runtime", DBHash(db)), nil
}
