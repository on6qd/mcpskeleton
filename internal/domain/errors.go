package domain

import (
	"errors"
	"fmt"
)

// Sentinel errors for the failures the core classifies. They exist so that
// adapters can react to a category of failure without matching on strings.
var (
	// ErrUnauthenticated means no valid identity was established. It surfaces as
	// HTTP 401 at the transport boundary.
	ErrUnauthenticated = errors.New("unauthenticated")

	// ErrForbidden means a valid identity lacked the required scope. It surfaces
	// as HTTP 403, or as a rejected tool call.
	ErrForbidden = errors.New("forbidden")

	// ErrToolNotFound means no tool is registered under the requested name.
	ErrToolNotFound = errors.New("tool not found")
)

// ToolError is a failure *inside* a tool that the model should see and may be
// able to act on: bad arguments, a missing record, a rejected value.
//
// It is deliberately distinct from an infrastructure failure. The core maps a
// ToolError to an MCP result with isError set, so the model reads the message
// and can retry; anything else becomes a protocol-level error, which the model
// cannot reason about.
type ToolError struct {
	// Message is shown to the model. It must not contain secrets.
	Message string

	// Err is an optional underlying cause, kept for logs and errors.Is/As. It is
	// not shown to the model.
	Err error
}

// NewToolError builds a ToolError with a formatted message and no cause.
func NewToolError(format string, args ...any) *ToolError {
	return &ToolError{Message: fmt.Sprintf(format, args...)}
}

// WrapToolError builds a ToolError with a formatted message wrapping a cause.
func WrapToolError(err error, format string, args ...any) *ToolError {
	return &ToolError{Message: fmt.Sprintf(format, args...), Err: err}
}

func (e *ToolError) Error() string {
	if e.Err != nil {
		return e.Message + ": " + e.Err.Error()
	}
	return e.Message
}

// Unwrap exposes the cause to errors.Is and errors.As.
func (e *ToolError) Unwrap() error { return e.Err }
