package jsonl_test

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/wesm/kata/internal/jsonl"
)

func TestDecoderRequiresExportVersionFirst(t *testing.T) {
	input := strings.NewReader(`{"kind":"project","data":{"id":1}}` + "\n")

	_, err := jsonl.NewDecoder(input).ReadAll(context.Background())

	require.Error(t, err)
	assert.ErrorIs(t, err, jsonl.ErrMissingExportVersion)
	assert.Contains(t, err.Error(), "line 1")
}

func TestDecoderRejectsUnknownKind(t *testing.T) {
	input := strings.NewReader(
		`{"kind":"meta","data":{"key":"export_version","value":"1"}}` + "\n" +
			`{"kind":"bogus","data":{"id":1}}` + "\n",
	)

	_, err := jsonl.NewDecoder(input).ReadAll(context.Background())

	require.Error(t, err)
	assert.ErrorIs(t, err, jsonl.ErrUnknownKind)
	assert.Contains(t, err.Error(), "line 2")
}

func TestDecoderRejectsOutOfOrderKind(t *testing.T) {
	input := strings.NewReader(
		`{"kind":"meta","data":{"key":"export_version","value":"1"}}` + "\n" +
			`{"kind":"link","data":{"id":1}}` + "\n" +
			`{"kind":"issue","data":{"id":1}}` + "\n",
	)

	_, err := jsonl.NewDecoder(input).ReadAll(context.Background())

	require.Error(t, err)
	assert.ErrorIs(t, err, jsonl.ErrKindOrderViolation)
	assert.Contains(t, err.Error(), "line 3")
}

func TestDecoderReportsInvalidJSONLine(t *testing.T) {
	input := strings.NewReader(
		`{"kind":"meta","data":{"key":"export_version","value":"1"}}` + "\n" +
			`{"kind":"project","data":` + "\n",
	)

	_, err := jsonl.NewDecoder(input).ReadAll(context.Background())

	require.Error(t, err)
	assert.Contains(t, err.Error(), "line 2")
}

func TestDecoderReadsOrderedEnvelopes(t *testing.T) {
	input := strings.NewReader(
		`{"kind":"meta","data":{"key":"export_version","value":"1"},"ignored":true}` + "\n" +
			`{"kind":"meta","data":{"key":"schema_version","value":"1"}}` + "\n" +
			`{"kind":"project","data":{"id":1}}` + "\n",
	)

	got, err := jsonl.NewDecoder(input).ReadAll(context.Background())

	require.NoError(t, err)
	require.Len(t, got, 3)
	assert.Equal(t, jsonl.KindMeta, got[0].Kind)
	assert.Equal(t, jsonl.KindProject, got[2].Kind)
}

func TestEncoderWritesCompactJSONLines(t *testing.T) {
	var out strings.Builder
	enc := jsonl.NewEncoder(&out)

	err := enc.Write(jsonl.Envelope{
		Kind: jsonl.KindMeta,
		Data: []byte(`{"key":"export_version","value":"1"}`),
	})

	require.NoError(t, err)
	assert.Equal(t, `{"kind":"meta","data":{"key":"export_version","value":"1"}}`+"\n", out.String())
}

func TestDecoderReturnsContextError(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	_, err := jsonl.NewDecoder(strings.NewReader(`{"kind":"meta","data":{"key":"export_version","value":"1"}}` + "\n")).ReadAll(ctx)

	require.Error(t, err)
	assert.True(t, errors.Is(err, context.Canceled))
}
