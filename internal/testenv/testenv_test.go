package testenv_test

import (
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
	bs, err := io.ReadAll(resp.Body)
	require.NoError(t, err)
	assert.Equal(t, 200, resp.StatusCode)
	assert.Contains(t, string(bs), `"ok":true`)
}
