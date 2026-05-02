// Internal test for the unexported foldDetailsIntoMessage helper
// and the public InstallErrorFormatter contract.
//
//nolint:revive // package-name lint flagged externally; internal test needs the package name
package api

import (
	"errors"
	"strings"
	"testing"

	"github.com/danielgtaylor/huma/v2"
	"github.com/stretchr/testify/assert"
)

// TestFoldDetailsIntoMessage covers hammer-test finding #11: a
// validation failure used to surface "validation failed" with no
// detail because InstallErrorFormatter dropped huma's per-field
// errs slice. Now folds up to 3 details into the message in
// "field: reason" form so close --reason banana, list --status
// nonsense, etc. give the user actionable feedback.
func TestFoldDetailsIntoMessage(t *testing.T) {
	t.Run("no details returns base message", func(t *testing.T) {
		assert.Equal(t, "validation failed",
			foldDetailsIntoMessage("validation failed", nil))
	})

	t.Run("ErrorDetailer with location surfaces field name", func(t *testing.T) {
		errs := []error{
			&huma.ErrorDetail{
				Location: "body.reason",
				Message:  "expected one of done, wontfix, duplicate",
			},
		}
		got := foldDetailsIntoMessage("validation failed", errs)
		assert.Equal(t,
			"validation failed: reason: expected one of done, wontfix, duplicate",
			got)
	})

	t.Run("path. and query. prefixes also stripped", func(t *testing.T) {
		errs := []error{
			&huma.ErrorDetail{Location: "query.status", Message: "expected enum value"},
			&huma.ErrorDetail{Location: "path.id", Message: "must be integer"},
		}
		got := foldDetailsIntoMessage("validation failed", errs)
		assert.Contains(t, got, "status: expected enum value")
		assert.Contains(t, got, "id: must be integer")
	})

	t.Run("plain error falls back to .Error()", func(t *testing.T) {
		got := foldDetailsIntoMessage("validation failed",
			[]error{errors.New("custom")})
		assert.Equal(t, "validation failed: custom", got)
	})

	t.Run("more than three details gets and-N-more suffix", func(t *testing.T) {
		errs := []error{
			&huma.ErrorDetail{Location: "body.a", Message: "x"},
			&huma.ErrorDetail{Location: "body.b", Message: "x"},
			&huma.ErrorDetail{Location: "body.c", Message: "x"},
			&huma.ErrorDetail{Location: "body.d", Message: "x"},
			&huma.ErrorDetail{Location: "body.e", Message: "x"},
		}
		got := foldDetailsIntoMessage("validation failed", errs)
		assert.True(t, strings.Contains(got, "(and 2 more)"),
			"expected `(and 2 more)` in message, got %q", got)
	})
}

// TestInstallErrorFormatter_FoldsDetailsIntoApiError pins that
// InstallErrorFormatter's huma.NewError replacement actually wires
// up the fold for code paths that go through the framework's
// validation pipeline.
func TestInstallErrorFormatter_FoldsDetailsIntoApiError(t *testing.T) {
	InstallErrorFormatter()
	// huma.NewError is the package-level function we replaced.
	se := huma.NewError(400, "validation failed",
		&huma.ErrorDetail{Location: "body.reason", Message: "must be one of done, wontfix"})
	apiErr, ok := se.(*APIError)
	if !ok {
		t.Fatalf("expected *APIError, got %T", se)
	}
	assert.Equal(t, 400, apiErr.Status)
	assert.Equal(t, "validation", apiErr.Code)
	assert.Contains(t, apiErr.Message, "reason: must be one of done, wontfix",
		"the per-field detail must reach the wire envelope")
}
