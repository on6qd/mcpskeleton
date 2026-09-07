package domain_test

import (
	"errors"
	"testing"

	"github.com/bartdelepeleer/mcpskeleton/internal/domain"
)

func TestNewToolError(t *testing.T) {
	t.Parallel()

	err := domain.NewToolError("no note with id %q", "abc")

	if got, want := err.Error(), `no note with id "abc"`; got != want {
		t.Errorf("Error() = %q, want %q", got, want)
	}
	if err.Unwrap() != nil {
		t.Errorf("Unwrap() = %v, want nil", err.Unwrap())
	}
}

func TestWrapToolError(t *testing.T) {
	t.Parallel()

	cause := errors.New("disk on fire")
	err := domain.WrapToolError(cause, "could not save note")

	if got, want := err.Error(), "could not save note: disk on fire"; got != want {
		t.Errorf("Error() = %q, want %q", got, want)
	}
	if !errors.Is(err, cause) {
		t.Error("errors.Is could not find the wrapped cause")
	}
}

// A ToolError must be distinguishable from any other error, because the core
// routes it to an isError result while everything else becomes a protocol error.
func TestToolErrorIsDistinguishable(t *testing.T) {
	t.Parallel()

	var toolErr *domain.ToolError

	if !errors.As(domain.NewToolError("nope"), &toolErr) {
		t.Fatal("errors.As failed to match a ToolError")
	}
	if errors.As(errors.New("plain"), &toolErr) {
		t.Error("errors.As matched a plain error as a ToolError")
	}
}

// Wrapping must not make a ToolError look like an infrastructure error, nor an
// infrastructure error look like a ToolError.
func TestToolErrorWrappingDoesNotLeakCategory(t *testing.T) {
	t.Parallel()

	err := domain.WrapToolError(domain.ErrToolNotFound, "unknown tool")

	var toolErr *domain.ToolError
	if !errors.As(err, &toolErr) {
		t.Error("wrapped ToolError no longer matches as a ToolError")
	}
	if !errors.Is(err, domain.ErrToolNotFound) {
		t.Error("wrapped sentinel is no longer reachable via errors.Is")
	}
	if errors.Is(err, domain.ErrForbidden) {
		t.Error("unrelated sentinel matched")
	}
}

func TestSentinelsAreDistinct(t *testing.T) {
	t.Parallel()

	all := []error{domain.ErrUnauthenticated, domain.ErrForbidden, domain.ErrToolNotFound}
	for i, a := range all {
		for j, b := range all {
			if i != j && errors.Is(a, b) {
				t.Errorf("sentinel %v matched %v", a, b)
			}
		}
	}
}
