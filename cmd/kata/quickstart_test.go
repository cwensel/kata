package main

import (
	"bytes"
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestQuickstart_PrintsAgentInstructions(t *testing.T) {
	resetFlags(t)
	cmd := newQuickstartCmd()
	var out bytes.Buffer
	cmd.SetOut(&out)
	cmd.SetArgs(nil)

	require.NoError(t, cmd.Execute())
	assert.Contains(t, out.String(), "kata agent quickstart")
	assert.Contains(t, out.String(), "Search before creating")
	assert.Contains(t, out.String(), "Do not run delete or purge")
}

func TestQuickstart_JSON(t *testing.T) {
	resetFlags(t)
	flags.JSON = true
	cmd := newQuickstartCmd()
	var out bytes.Buffer
	cmd.SetOut(&out)
	cmd.SetArgs(nil)

	require.NoError(t, cmd.Execute())
	var got struct {
		APIVersion int    `json:"kata_api_version"`
		Quickstart string `json:"quickstart"`
	}
	require.NoError(t, json.Unmarshal(out.Bytes(), &got))
	assert.Equal(t, 1, got.APIVersion)
	assert.Contains(t, got.Quickstart, "kata agent quickstart")
}
