package testenv_test

import (
	"context"
	"io"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/wesm/kata/internal/testenv"
)

func TestEnv_BootsDaemonAndAnswersPing(t *testing.T) {
	env := testenv.New(t)
	resp, err := env.HTTP.Get(env.URL + "/api/v1/ping")
	require.NoError(t, err)
	defer func() { _ = resp.Body.Close() }()
	bs, _ := io.ReadAll(resp.Body)
	assert.Equal(t, 200, resp.StatusCode)
	assert.Contains(t, string(bs), `"ok":true`)
	_ = context.Background
}
