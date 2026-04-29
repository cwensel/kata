package api

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"

	"github.com/danielgtaylor/huma/v2"
)

// ErrorBody is the inner payload of an error envelope.
type ErrorBody struct {
	Code    string         `json:"code"`
	Message string         `json:"message"`
	Hint    string         `json:"hint,omitempty"`
	Data    map[string]any `json:"data,omitempty"`
}

// ErrorEnvelope is the stable wire shape for non-2xx responses.
type ErrorEnvelope struct {
	Status int       `json:"status"`
	Error  ErrorBody `json:"error"`
}

// APIError is the Go representation that handlers return; satisfies Huma's
// StatusError interface so the framework serializes the envelope verbatim.
// Plan 1 §4.6 fixes the public name as APIError; renaming would break the
// documented wire contract and CLI parser.
//
//nolint:revive // see comment above re: fixed Plan 1 §4.6 public name.
type APIError struct {
	Status  int
	Code    string
	Message string
	Hint    string
	Data    map[string]any
}

// NewError constructs an APIError. Hint and data are optional.
func NewError(status int, code, message, hint string, data map[string]any) *APIError {
	return &APIError{Status: status, Code: code, Message: message, Hint: hint, Data: data}
}

// Error implements the standard error interface.
func (e *APIError) Error() string {
	return fmt.Sprintf("%d %s: %s", e.Status, e.Code, e.Message)
}

// GetStatus implements huma.StatusError so the framework picks the right code.
func (e *APIError) GetStatus() int { return e.Status }

// Envelope returns the JSON body shape used in responses.
func (e *APIError) Envelope() ErrorEnvelope {
	return ErrorEnvelope{
		Status: e.Status,
		Error: ErrorBody{
			Code:    e.Code,
			Message: e.Message,
			Hint:    e.Hint,
			Data:    e.Data,
		},
	}
}

// MarshalJSON serializes the envelope so Huma's default response writer emits
// our wire shape rather than the framework default.
func (e *APIError) MarshalJSON() ([]byte, error) {
	return json.Marshal(e.Envelope())
}

// InstallErrorFormatter wires Huma so non-API-typed errors (panics, validation
// failures) also serialize to ErrorEnvelope. Call once at server startup.
func InstallErrorFormatter() {
	huma.NewError = func(status int, message string, _ ...error) huma.StatusError {
		code := codeForStatus(status)
		return &APIError{Status: status, Code: code, Message: message}
	}
}

func codeForStatus(status int) string {
	switch status {
	case http.StatusBadRequest:
		return "validation"
	case http.StatusNotFound:
		return "not_found"
	case http.StatusConflict:
		return "conflict"
	case http.StatusPreconditionFailed:
		return "confirm_required"
	case http.StatusInternalServerError:
		return "internal"
	default:
		return "error"
	}
}

// EnsureCancelled is a small helper so handlers can early-return when ctx is
// cancelled without producing a 500 envelope.
func EnsureCancelled(ctx context.Context) error {
	if err := ctx.Err(); err != nil {
		return NewError(499, "client_closed", err.Error(), "", nil)
	}
	return nil
}
