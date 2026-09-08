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

// AuthFailure is a rejected credential.
//
// Its message is the same constant whatever went wrong, so a caller cannot use
// the response to tell an unknown credential from a revoked, expired or
// disabled one. The reason is carried alongside, for logs, and is deliberately
// unreachable through Error() so that formatting the error into a response
// cannot leak it.
//
// This lives in domain rather than in an authenticator because it is part of
// the Authenticator port's contract: every adapter owes the same silence, and
// every transport reads the reason the same way.
type AuthFailure struct {
	// Reason is why authentication failed, for logs only. Never for a response.
	Reason string
}

// unauthenticatedMessage is what every rejected credential says.
const unauthenticatedMessage = "invalid credential"

// NewAuthFailure returns a rejection that reveals nothing and remembers why.
func NewAuthFailure(reason string) *AuthFailure {
	return &AuthFailure{Reason: reason}
}

func (e *AuthFailure) Error() string { return unauthenticatedMessage }

// Unwrap makes every rejection match ErrUnauthenticated, which is how a
// transport knows to answer 401.
func (e *AuthFailure) Unwrap() error { return ErrUnauthenticated }

// FailureReason returns why an authentication failed, or "" if err is not an
// authentication failure.
func FailureReason(err error) string {
	var f *AuthFailure
	if errors.As(err, &f) {
		return f.Reason
	}
	return ""
}
