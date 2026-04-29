package main

import (
	"bytes"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestResolveBody_FlagWins(t *testing.T) {
	got, err := resolveBody(BodySources{Body: "hello"}, nil)
	require.NoError(t, err)
	assert.Equal(t, "hello", got)
}

func TestResolveBody_FromFile(t *testing.T) {
	dir := t.TempDir()
	path := dir + "/b.txt"
	require.NoError(t, writeStringFile(path, "from file"))
	got, err := resolveBody(BodySources{File: path}, nil)
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
	_, err := resolveBody(BodySources{Body: "x", Stdin: true}, nil)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "exactly one")
}

func TestResolveActor_Precedence(t *testing.T) {
	t.Setenv("KATA_AUTHOR", "")
	got, src := resolveActor("flag-actor", nil)
	assert.Equal(t, "flag-actor", got)
	assert.Equal(t, "flag", src)

	t.Setenv("KATA_AUTHOR", "env-actor")
	got, src = resolveActor("", nil)
	assert.Equal(t, "env-actor", got)
	assert.Equal(t, "env", src)

	t.Setenv("KATA_AUTHOR", "")
	got, src = resolveActor("", func() (string, error) { return "git-user", nil })
	assert.Equal(t, "git-user", got)
	assert.Equal(t, "git", src)

	got, src = resolveActor("", func() (string, error) { return "", nil })
	assert.Equal(t, "anonymous", got)
	assert.Equal(t, "fallback", src)
}

func TestEmitJSON_AddsAPIVersion(t *testing.T) {
	var buf bytes.Buffer
	require.NoError(t, emitJSON(&buf, map[string]string{"x": "y"}))
	out := buf.String()
	assert.Contains(t, out, `"kata_api_version":1`)
	assert.Contains(t, out, `"x":"y"`)
	assert.True(t, strings.HasSuffix(out, "\n"))
}
