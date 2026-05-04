package jsonl_test

import (
	"context"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/wesm/kata/internal/jsonl"
)

func TestImportPropagatesDecoderFailures(t *testing.T) {
	tests := []struct {
		name    string
		input   string
		wantErr string
	}{
		{
			name:    "truncated JSON",
			input:   `{"kind":"meta","data":{"key":"export_version","value":"1"}}` + "\n" + `{"kind":"project","data":`,
			wantErr: "line 2",
		},
		{
			name:    "missing export version",
			input:   `{"kind":"project","data":{"id":1}}` + "\n",
			wantErr: "missing export_version",
		},
		{
			name: "kind order violation",
			input: strings.Join([]string{
				`{"kind":"meta","data":{"key":"export_version","value":"1"}}`,
				`{"kind":"event","data":{"id":1}}`,
				`{"kind":"issue","data":{"id":1}}`,
			}, "\n") + "\n",
			wantErr: "kind order violation",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			target := openImportTargetDB(t)

			err := jsonl.Import(context.Background(), strings.NewReader(tt.input), target)

			require.Error(t, err)
			assert.Contains(t, err.Error(), tt.wantErr)
		})
	}
}
