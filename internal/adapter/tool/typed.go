// Package tool provides the helper every tool adapter is built with.
//
// A tool author writes an arguments struct and a function. Typed turns those
// into a port.Tool: it derives the JSON Schema from the struct by reflection,
// decodes incoming arguments into it, and reports a decode failure as something
// the model can read and correct.
package tool

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"

	"github.com/google/jsonschema-go/jsonschema"

	"github.com/bartdelepeleer/mcpskeleton/internal/domain"
	"github.com/bartdelepeleer/mcpskeleton/internal/port"
)

// Func is the shape a tool author writes.
//
// The principal is a parameter rather than something recovered from the
// context, matching port.Tool: a tool cannot be written without being handed
// the identity of its caller.
type Func[In any] func(ctx context.Context, p domain.Principal, in In) (port.Result, error)

// typed is the port.Tool built by Typed.
type typed[In any] struct {
	name        string
	description string
	scope       string
	schema      json.RawMessage
	resolved    *jsonschema.Resolved
	fn          Func[In]
}

// Typed builds a port.Tool from an arguments struct and a function.
//
// The schema is derived from In once, here, and a type that cannot be described
// as JSON Schema panics: that is a programming error visible at wiring time,
// not a condition to handle at runtime.
//
// There is deliberately no Out type parameter. port.Tool carries no output
// schema, so one would constrain port.Result.Structured and nothing else, while
// forcing every prose-only tool to name a type it does not have.
func Typed[In any](name, description, scope string, fn Func[In]) port.Tool {
	if name == "" {
		panic("tool: empty name")
	}
	if fn == nil {
		panic(fmt.Sprintf("tool %q: nil function", name))
	}

	schema, err := jsonschema.For[In](nil)
	if err != nil {
		panic(fmt.Sprintf("tool %q: cannot derive a schema for its arguments: %v", name, err))
	}
	raw, err := json.Marshal(schema)
	if err != nil {
		panic(fmt.Sprintf("tool %q: cannot marshal its schema: %v", name, err))
	}
	// Resolved once, here, so that validating a call is not a per-request
	// compile of the schema.
	resolved, err := schema.Resolve(nil)
	if err != nil {
		panic(fmt.Sprintf("tool %q: cannot prepare its schema for validation: %v", name, err))
	}

	return &typed[In]{
		name:        name,
		description: description,
		scope:       scope,
		schema:      raw,
		resolved:    resolved,
		fn:          fn,
	}
}

func (t *typed[In]) Name() string                 { return t.name }
func (t *typed[In]) Description() string          { return t.description }
func (t *typed[In]) RequiredScope() string        { return t.scope }
func (t *typed[In]) InputSchema() json.RawMessage { return t.schema }

// Execute validates args against the published schema, decodes them into In,
// and runs the function.
//
// Absent or empty arguments are treated as an empty object, so a tool whose
// fields are all optional can be called with nothing. Every rejection is a
// domain.ToolError rather than an infrastructure error: the model supplied the
// arguments, so the model is who can fix them.
func (t *typed[In]) Execute(ctx context.Context, p domain.Principal, args json.RawMessage) (port.Result, error) {
	if len(bytes.TrimSpace(args)) == 0 {
		args = json.RawMessage("{}")
	}

	// Validation comes before decoding, and is what makes the published schema
	// mean something. Decoding alone silently accepts a missing required field
	// as a zero value and ignores every constraint the schema declares —
	// minimum, maxLength, enum — leaving each tool to re-check by hand what it
	// already told the model. The MCP SDK's low-level tool registration does no
	// validation of its own, so if it does not happen here it does not happen.
	var instance any
	if err := json.Unmarshal(args, &instance); err != nil {
		return port.Result{}, domain.WrapToolError(err, "arguments for %q are not valid JSON", t.name)
	}
	if err := t.resolved.Validate(instance); err != nil {
		return port.Result{}, domain.WrapToolError(err, "invalid arguments for %q", t.name)
	}

	var in In
	dec := json.NewDecoder(bytes.NewReader(args))
	// Unknown fields are rejected here as well as by the schema, so a tool whose
	// arguments are not a struct — and so carry no additionalProperties — still
	// refuses a misspelled field rather than letting a model believe it was
	// honoured.
	dec.DisallowUnknownFields()
	if err := dec.Decode(&in); err != nil {
		return port.Result{}, domain.WrapToolError(err, "invalid arguments for %q", t.name)
	}

	return t.fn(ctx, p, in)
}
