package api_test

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/wesm/kata/internal/api"
)

func TestAPIError_StatusAndBodyShape(t *testing.T) {
	err := api.NewError(404, "issue_not_found", "issue #42 does not exist", "kata search", nil)
	assert.Equal(t, 404, err.Status)

	body := err.Envelope()
	assert.Equal(t, "issue_not_found", body.Error.Code)
	assert.Equal(t, "issue #42 does not exist", body.Error.Message)
	assert.Equal(t, "kata search", body.Error.Hint)

	js, err2 := json.Marshal(body)
	require.NoError(t, err2)
	assert.Contains(t, string(js), `"code":"issue_not_found"`)
}

func TestAPIError_DataPropagates(t *testing.T) {
	err := api.NewError(409, "duplicate_candidates", "x", "", map[string]any{
		"candidates": []int{1, 2},
	})
	body := err.Envelope()
	assert.Equal(t, []int{1, 2}, body.Error.Data["candidates"])
}
