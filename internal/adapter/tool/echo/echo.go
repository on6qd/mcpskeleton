// Package echo is the reference pure-compute tool.
//
// It depends on nothing but the principal and its arguments, which makes it the
// smallest complete example of what a tool adapter looks like. The notes tool
// is the other half of the pair: one with a driven port behind it.
package echo

import (
	"context"
	"strings"

	"github.com/bartdelepeleer/mcpskeleton/internal/adapter/tool"
	"github.com/bartdelepeleer/mcpskeleton/internal/domain"
	"github.com/bartdelepeleer/mcpskeleton/internal/port"
)

// Scope is the permission a principal needs to use this tool.
const Scope = "tools:echo"

// maxTimes caps repetition so a single call cannot be used to generate an
// unbounded response.
const maxTimes = 10

// Args are the tool's arguments. The struct tags are the whole schema: the json
// name is what the model sends, and the jsonschema tag is how the field is
// described to it.
type Args struct {
	// Message is required: it has no omitempty, so the derived schema lists it
	// under "required".
	Message string `json:"message" jsonschema:"the message to repeat back"`

	// Times is optional and defaults to 1.
	Times int `json:"times,omitempty" jsonschema:"how many times to repeat the message, 1 to 10"`
}

// New returns the echo tool.
func New() port.Tool {
	return tool.Typed("echo", "Repeat a message back to the caller.", Scope, run)
}

func run(_ context.Context, p domain.Principal, in Args) (port.Result, error) {
	if strings.TrimSpace(in.Message) == "" {
		return port.Result{}, domain.NewToolError("message must not be empty")
	}

	times := in.Times
	if times == 0 {
		times = 1
	}
	if times < 1 || times > maxTimes {
		return port.Result{}, domain.NewToolError("times must be between 1 and %d, got %d", maxTimes, times)
	}

	parts := make([]string, times)
	for i := range parts {
		parts[i] = in.Message
	}
	text := strings.Join(parts, "\n")

	return port.Result{
		Text: text,
		// The caller's own identity is echoed back too, which makes this tool
		// useful for confirming end to end that authentication reached the tool
		// rather than stopping at the transport.
		Structured: map[string]any{
			"message": in.Message,
			"times":   times,
			"caller":  p.Subject,
		},
	}, nil
}
