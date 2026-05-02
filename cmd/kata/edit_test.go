package main

import (
	"bytes"
	"context"
	"errors"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestEdit_EmptyTitle_ValidatedClientSide covers hammer-test
// finding #4: edit --title "" (or whitespace-only) used to forward
// the value to the daemon, which returned a raw SQLite CHECK
// constraint error. Now blocked client-side with a kindValidation
// cliError that points the user at the right action (omit the
// flag to keep the existing title).
func TestEdit_EmptyTitle_ValidatedClientSide(t *testing.T) {
	for _, blank := range []string{"", "   ", "\t\n"} {
		resetFlags(t)
		cmd := newRootCmd()
		var buf bytes.Buffer
		cmd.SetOut(&buf)
		cmd.SetErr(&buf)
		cmd.SetArgs([]string{"edit", "1", "--title", blank})
		cmd.SetContext(context.Background())

		err := cmd.Execute()
		require.Error(t, err, "blank title %q should be rejected", blank)
		var ce *cliError
		require.True(t, errors.As(err, &ce), "expected *cliError, got %T", err)
		assert.Equal(t, ExitValidation, ce.ExitCode)
		assert.Equal(t, kindValidation, ce.Kind)
		assert.Contains(t, ce.Message, "must not be empty",
			"validation message should explain the failure")
	}
}
