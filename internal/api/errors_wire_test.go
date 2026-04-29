package api_test

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/wesm/kata/internal/api"
)

// TestAPIError_WireShape pins the on-the-wire JSON envelope shape so a future
// refactor (e.g., struct-tag changes, MarshalJSON edits, or moving to a
// different framework writer) can't silently regress to flat or PascalCase
// fields. The CLI parser depends on this exact shape.
func TestAPIError_WireShape(t *testing.T) {
	e := api.NewError(404, "project_not_found", "msg", "hint", map[string]any{"k": "v"})
	bs, err := json.Marshal(e)
	require.NoError(t, err)

	var got map[string]any
	require.NoError(t, json.Unmarshal(bs, &got))
	assert.EqualValues(t, 404, got["status"])
	inner, ok := got["error"].(map[string]any)
	require.True(t, ok, "error should be an object")
	assert.Equal(t, "project_not_found", inner["code"])
	assert.Equal(t, "msg", inner["message"])
	assert.Equal(t, "hint", inner["hint"])
	assert.Equal(t, map[string]any{"k": "v"}, inner["data"])
}
