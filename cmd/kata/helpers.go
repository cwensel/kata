// Package main is the kata CLI entry point.
package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"strings"
)

// Exit codes per spec §4.7.
const (
	ExitOK            = 0
	ExitInternal      = 1
	ExitUsage         = 2
	ExitValidation    = 3
	ExitNotFound      = 4
	ExitConflict      = 5
	ExitConfirm       = 6
	ExitDaemonUnavail = 7
)

// BodySources is the parsed --body / --body-file / --body-stdin trio.
type BodySources struct {
	Body  string
	File  string
	Stdin bool
}

// gitUserFn is a function signature for resolveActor's git fallback so tests
// can inject a stub instead of touching the real `git config user.name`.
type gitUserFn func() (string, error)

// resolveBody returns the resolved body text. Mutually exclusive sources;
// returns error otherwise. Empty result allowed when no source set.
func resolveBody(b BodySources, stdin io.Reader) (string, error) {
	count := 0
	if b.Body != "" {
		count++
	}
	if b.File != "" {
		count++
	}
	if b.Stdin {
		count++
	}
	if count > 1 {
		return "", errors.New("must pass exactly one of --body, --body-file, --body-stdin")
	}
	switch {
	case b.Body != "":
		return b.Body, nil
	case b.File != "":
		//nolint:gosec // user-supplied path is the whole point of --body-file
		bs, err := os.ReadFile(b.File)
		if err != nil {
			return "", fmt.Errorf("read %s: %w", b.File, err)
		}
		return strings.TrimRight(string(bs), "\n"), nil
	case b.Stdin:
		if stdin == nil {
			stdin = os.Stdin
		}
		var buf bytes.Buffer
		if _, err := io.Copy(&buf, stdin); err != nil {
			return "", fmt.Errorf("read stdin: %w", err)
		}
		return strings.TrimRight(buf.String(), "\n"), nil
	default:
		return "", nil
	}
}

// resolveActor implements precedence flag > env > git > "anonymous". Returns
// (actor, source) where source is one of "flag"|"env"|"git"|"fallback".
func resolveActor(flag string, gitUser gitUserFn) (string, string) {
	if flag != "" {
		return flag, "flag"
	}
	if v := os.Getenv("KATA_AUTHOR"); v != "" {
		return v, "env"
	}
	if gitUser == nil {
		gitUser = readGitUserName
	}
	if name, _ := gitUser(); name != "" {
		return name, "git"
	}
	return "anonymous", "fallback"
}

func readGitUserName() (string, error) {
	cmd := exec.Command("git", "config", "user.name")
	out, err := cmd.Output()
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(string(out)), nil
}

// emitJSON marshals v with a "kata_api_version":1 wrapper and a trailing
// newline.
func emitJSON(w io.Writer, v any) error {
	wrapped := map[string]any{"kata_api_version": 1}
	for k, val := range structToMap(v) {
		wrapped[k] = val
	}
	bs, err := json.Marshal(wrapped)
	if err != nil {
		return err
	}
	bs = append(bs, '\n')
	_, err = w.Write(bs)
	return err
}

func structToMap(v any) map[string]any {
	bs, _ := json.Marshal(v)
	var m map[string]any
	_ = json.Unmarshal(bs, &m)
	if m == nil {
		m = map[string]any{"value": v}
	}
	return m
}

// writeStringFile is a tiny wrapper used by tests.
func writeStringFile(path, body string) error {
	//nolint:gosec // test helper writing to a temp dir
	return os.WriteFile(path, []byte(body), 0o644)
}

// httpDoJSON sends a request body, returns (status, response body bytes).
//
//nolint:unused // used by upcoming CLI command tasks (T21-T27)
func httpDoJSON(ctx context.Context, client *http.Client, method, url string, body any) (int, []byte, error) {
	var rdr io.Reader
	if body != nil {
		bs, err := json.Marshal(body)
		if err != nil {
			return 0, nil, err
		}
		rdr = bytes.NewReader(bs)
	}
	req, err := http.NewRequestWithContext(ctx, method, url, rdr)
	if err != nil {
		return 0, nil, err
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	//nolint:gosec // URL is constructed from daemon socket address, not user input
	resp, err := client.Do(req)
	if err != nil {
		return 0, nil, err
	}
	defer func() { _ = resp.Body.Close() }()
	bs, err := io.ReadAll(resp.Body)
	if err != nil {
		return 0, nil, err
	}
	return resp.StatusCode, bs, nil
}
