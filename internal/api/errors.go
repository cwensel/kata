package api //nolint:revive // package name "api" is fixed by Plan 1 §4 wire-types layout.

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
// Huma emits 422 for request-body validation failures; we normalize that to
// 400 with code "validation" so the wire contract documented in spec §4.7
// (no 422 in the status table) holds.
func InstallErrorFormatter() {
	huma.NewError = func(status int, message string, _ ...error) huma.StatusError {
		if status == http.StatusUnprocessableEntity {
			status = http.StatusBadRequest
		}
		return &APIError{Status: status, Code: codeForStatus(status), Message: message}
	}
}

func codeForStatus(status int) string {
	switch status {
	case http.StatusBadRequest, http.StatusUnprocessableEntity:
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

// WriteEnvelope writes an ErrorEnvelope JSON body with the given status code
// and Content-Type: application/json. Used by HTTP middleware that needs to
// emit the same wire shape as handler-returned APIErrors.
func WriteEnvelope(w http.ResponseWriter, status int, code, message string) {
	body, _ := json.Marshal(ErrorEnvelope{
		Status: status,
		Error:  ErrorBody{Code: code, Message: message},
	})
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_, _ = w.Write(body)
}

// EnsureCancelled is a small helper so handlers can early-return when ctx is
// cancelled without producing a 500 envelope.
func EnsureCancelled(ctx context.Context) error {
	if err := ctx.Err(); err != nil {
		return NewError(499, "client_closed", err.Error(), "", nil)
	}
	return nil
}
