package main

import (
	"bytes"
	"encoding/json"
	"os"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// writeStringFile is a test-only helper for building --body-file fixtures.
func writeStringFile(t *testing.T, path, body string) {
	t.Helper()
	require.NoError(t, os.WriteFile(path, []byte(body), 0o600))
}

func TestResolveBody_FlagWins(t *testing.T) {
	got, err := resolveBody(BodySources{Body: "hello", BodySet: true}, nil)
	require.NoError(t, err)
	assert.Equal(t, "hello", got)
}

func TestResolveBody_ExplicitEmptyIsHonored(t *testing.T) {
	got, err := resolveBody(BodySources{Body: "", BodySet: true}, nil)
	require.NoError(t, err)
	assert.Equal(t, "", got)
}

func TestResolveBody_FromFile(t *testing.T) {
	dir := t.TempDir()
	path := dir + "/b.txt"
	writeStringFile(t, path, "from file")
	got, err := resolveBody(BodySources{File: path, FileSet: true}, nil)
	require.NoError(t, err)
	assert.Equal(t, "from file", got)
}

func TestResolveBody_FromStdin(t *testing.T) {
	in := bytes.NewBufferString("from stdin")
	got, err := resolveBody(BodySources{Stdin: true}, in)
	require.NoError(t, err)
	assert.Equal(t, "from stdin", got)
}

func TestResolveBody_TwoSourcesIsError(t *testing.T) {
	_, err := resolveBody(BodySources{Body: "x", BodySet: true, Stdin: true}, nil)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "exactly one")
}

// An explicit `--body ""` together with `--body-stdin` is still a conflict —
// the previous (count by non-empty value) implementation missed this case.
func TestResolveBody_TwoSourcesEmptyBodyIsError(t *testing.T) {
	_, err := resolveBody(BodySources{Body: "", BodySet: true, Stdin: true}, nil)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "exactly one")
}

func TestResolveActor_Precedence(t *testing.T) {
	t.Run("flag wins", func(t *testing.T) {
		t.Setenv("KATA_AUTHOR", "env-shouldnt-win")
		got, src := resolveActor("flag-actor", func() (string, error) { return "git-shouldnt-win", nil })
		assert.Equal(t, "flag-actor", got)
		assert.Equal(t, "flag", src)
	})
	t.Run("env wins when no flag", func(t *testing.T) {
		t.Setenv("KATA_AUTHOR", "env-actor")
		got, src := resolveActor("", func() (string, error) { return "git-shouldnt-win", nil })
		assert.Equal(t, "env-actor", got)
		assert.Equal(t, "env", src)
	})
	t.Run("git wins when no flag and no env", func(t *testing.T) {
		t.Setenv("KATA_AUTHOR", "")
		got, src := resolveActor("", func() (string, error) { return "git-user", nil })
		assert.Equal(t, "git-user", got)
		assert.Equal(t, "git", src)
	})
	t.Run("fallback when nothing else", func(t *testing.T) {
		t.Setenv("KATA_AUTHOR", "")
		got, src := resolveActor("", func() (string, error) { return "", nil })
		assert.Equal(t, "anonymous", got)
		assert.Equal(t, "fallback", src)
	})
}

func TestEmitJSON_AddsAPIVersion(t *testing.T) {
	var buf bytes.Buffer
	require.NoError(t, emitJSON(&buf, map[string]string{"x": "y"}))
	out := buf.String()
	assert.Contains(t, out, `"kata_api_version":1`)
	assert.Contains(t, out, `"x":"y"`)
	assert.True(t, strings.HasSuffix(out, "\n"))
}

func TestEmitJSON_EmptyObject(t *testing.T) {
	var buf bytes.Buffer
	require.NoError(t, emitJSON(&buf, struct{}{}))
	assert.Equal(t, "{\"kata_api_version\":1}\n", buf.String())
}

func TestEmitJSON_RejectsNonObject(t *testing.T) {
	var buf bytes.Buffer
	require.Error(t, emitJSON(&buf, "scalar"))
	require.Error(t, emitJSON(&buf, []int{1, 2, 3}))
	require.Error(t, emitJSON(&buf, nil))
}

func TestEmitJSON_RejectsReservedKey(t *testing.T) {
	var buf bytes.Buffer
	err := emitJSON(&buf, map[string]any{"kata_api_version": "evil"})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "kata_api_version")
}

// A payload whose key is unicode-escaped must still be rejected. JSON
// permits "\uXXXX" escapes inside string content, so a payload like
// {"kata_api_version":"evil"} decodes to {"kata_api_version":"evil"}
// while the raw bytes do not contain the literal reserved key. A simple
// bytes.Contains(`"kata_api_version"`) guard would have let this slip
// through and produced a duplicate-keyed envelope downstream.
func TestEmitJSON_RejectsEscapedReservedKey(t *testing.T) {
	var buf bytes.Buffer
	// Build the escape sequence explicitly — backtick raw strings interpret
	// the bytes literally, but writing `k` here avoids any rendering
	// ambiguity in the source file.
	payload := json.RawMessage([]byte(`{"\u006bata_api_version":"evil"}`))
	require.NotContains(t, string(payload), `"kata_api_version"`,
		"test fixture itself must contain the escape, not the literal key")
	err := emitJSON(&buf, payload)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "kata_api_version")
}
