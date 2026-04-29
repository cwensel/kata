package main

import (
	"bytes"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestRoot_HelpListsUniversalFlags(t *testing.T) {
	cmd := newRootCmd()
	var buf bytes.Buffer
	cmd.SetOut(&buf)
	cmd.SetArgs([]string{"--help"})
	require.NoError(t, cmd.Execute())
	out := buf.String()
	assert.Contains(t, out, "--json")
	assert.Contains(t, out, "--quiet")
	assert.Contains(t, out, "--as")
	assert.Contains(t, out, "--workspace")
}
