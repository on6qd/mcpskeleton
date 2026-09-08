package tool_test

import (
	"context"
	"encoding/json"
	"errors"
	"testing"

	"github.com/bartdelepeleer/mcpskeleton/internal/adapter/tool"
	"github.com/bartdelepeleer/mcpskeleton/internal/domain"
	"github.com/bartdelepeleer/mcpskeleton/internal/port"
)

type greetArgs struct {
	Name  string `json:"name" jsonschema:"the person to greet"`
	Times int    `json:"times,omitempty" jsonschema:"how many times"`
}

func greet(_ context.Context, p domain.Principal, in greetArgs) (port.Result, error) {
	return port.Result{
		Text:       "hello " + in.Name + " from " + p.Subject,
		Structured: map[string]any{"times": in.Times},
	}, nil
}

func TestTypedMetadata(t *testing.T) {
	t.Parallel()

	tl := tool.Typed("greet", "say hello", "tools:greet", greet)

	if got := tl.Name(); got != "greet" {
		t.Errorf("Name() = %q, want %q", got, "greet")
	}
	if got := tl.Description(); got != "say hello" {
		t.Errorf("Description() = %q, want %q", got, "say hello")
	}
	if got := tl.RequiredScope(); got != "tools:greet" {
		t.Errorf("RequiredScope() = %q, want %q", got, "tools:greet")
	}
}

func TestTypedDerivesSchemaFromArgs(t *testing.T) {
	t.Parallel()

	tl := tool.Typed("greet", "say hello", "", greet)

	var schema struct {
		Type       string `json:"type"`
		Properties map[string]struct {
			Type        string `json:"type"`
			Description string `json:"description"`
		} `json:"properties"`
		Required []string `json:"required"`
	}
	if err := json.Unmarshal(tl.InputSchema(), &schema); err != nil {
		t.Fatalf("InputSchema is not valid JSON: %v", err)
	}

	if schema.Type != "object" {
		t.Errorf("schema type = %q, want %q", schema.Type, "object")
	}
	if got, ok := schema.Properties["name"]; !ok {
		t.Error(`schema has no "name" property`)
	} else {
		if got.Type != "string" {
			t.Errorf(`"name" type = %q, want "string"`, got.Type)
		}
		// The jsonschema struct tag is the tool author's way of describing a
		// field to the model, so it must survive into the published schema.
		if got.Description != "the person to greet" {
			t.Errorf(`"name" description = %q, want the struct tag`, got.Description)
		}
	}
	if got, ok := schema.Properties["times"]; !ok {
		t.Error(`schema has no "times" property`)
	} else if got.Type != "integer" {
		t.Errorf(`"times" type = %q, want "integer"`, got.Type)
	}

	// omitempty makes a field optional; everything else is required.
	if len(schema.Required) != 1 || schema.Required[0] != "name" {
		t.Errorf("required = %v, want [name] (times is omitempty)", schema.Required)
	}
}

// The schema is derived once at construction, not per call.
func TestTypedSchemaIsStable(t *testing.T) {
	t.Parallel()

	tl := tool.Typed("greet", "", "", greet)
	first, second := string(tl.InputSchema()), string(tl.InputSchema())
	if first != second {
		t.Errorf("InputSchema returned different JSON on successive calls:\n%s\n%s", first, second)
	}
}

func TestTypedExecuteDecodesArgs(t *testing.T) {
	t.Parallel()

	tl := tool.Typed("greet", "", "", greet)

	got, err := tl.Execute(
		context.Background(),
		domain.Principal{Subject: "alice"},
		json.RawMessage(`{"name":"bob","times":3}`),
	)
	if err != nil {
		t.Fatalf("Execute error = %v", err)
	}
	if want := "hello bob from alice"; got.Text != want {
		t.Errorf("Text = %q, want %q", got.Text, want)
	}
	structured, ok := got.Structured.(map[string]any)
	if !ok {
		t.Fatalf("Structured = %T, want map", got.Structured)
	}
	if structured["times"] != 3 {
		t.Errorf("Structured[times] = %v, want 3", structured["times"])
	}
}

// A tool whose fields are all optional must be callable with nothing at all,
// because clients differ on whether they send {} or omit arguments entirely.
func TestTypedExecuteEmptyArguments(t *testing.T) {
	t.Parallel()

	type optionalArgs struct {
		Note string `json:"note,omitempty"`
	}
	tl := tool.Typed("opt", "", "", func(_ context.Context, _ domain.Principal, in optionalArgs) (port.Result, error) {
		return port.Result{Text: "note=" + in.Note}, nil
	})

	for _, args := range []json.RawMessage{nil, {}, json.RawMessage(`  `), json.RawMessage(`{}`)} {
		got, err := tl.Execute(context.Background(), domain.Principal{Subject: "alice"}, args)
		if err != nil {
			t.Errorf("Execute(%q) error = %v", args, err)
			continue
		}
		if got.Text != "note=" {
			t.Errorf("Execute(%q) Text = %q, want %q", args, got.Text, "note=")
		}
	}
}

// Bad arguments are the model's mistake, so they must arrive as a ToolError the
// model can read and correct — not as an infrastructure error it cannot act on.
func TestTypedExecuteBadArgumentsAreToolErrors(t *testing.T) {
	t.Parallel()

	tl := tool.Typed("greet", "", "", greet)

	tests := []struct {
		name string
		args json.RawMessage
	}{
		{"malformed JSON", json.RawMessage(`{"name":`)},
		{"wrong type", json.RawMessage(`{"name":42}`)},
		{"not an object", json.RawMessage(`"just a string"`)},
		//nolint:misspell // the misspelling is what this case tests
		{"misspelled field", json.RawMessage(`{"nmae":"bob"}`)},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			_, err := tl.Execute(context.Background(), domain.Principal{Subject: "alice"}, tc.args)
			if err == nil {
				t.Fatal("Execute error = nil, want a ToolError")
			}
			var toolErr *domain.ToolError
			if !errors.As(err, &toolErr) {
				t.Fatalf("Execute error = %v (%T), want a *domain.ToolError", err, err)
			}
			if toolErr.Message == "" {
				t.Error("ToolError has no message for the model to read")
			}
		})
	}
}

// A tool's own failure passes through unchanged; the helper must not reclassify
// what the tool author decided.
func TestTypedExecutePropagatesToolErrors(t *testing.T) {
	t.Parallel()

	sentinel := domain.NewToolError("body must not be empty")
	tl := tool.Typed("fail", "", "", func(context.Context, domain.Principal, struct{}) (port.Result, error) {
		return port.Result{}, sentinel
	})

	_, err := tl.Execute(context.Background(), domain.Principal{Subject: "alice"}, nil)
	if !errors.Is(err, error(sentinel)) {
		t.Errorf("Execute error = %v, want the tool's own error", err)
	}
}

func TestTypedExecutePropagatesInfrastructureErrors(t *testing.T) {
	t.Parallel()

	boom := errors.New("connection refused")
	tl := tool.Typed("fail", "", "", func(context.Context, domain.Principal, struct{}) (port.Result, error) {
		return port.Result{}, boom
	})

	_, err := tl.Execute(context.Background(), domain.Principal{Subject: "alice"}, nil)
	if !errors.Is(err, boom) {
		t.Fatalf("Execute error = %v, want %v", err, boom)
	}
	var toolErr *domain.ToolError
	if errors.As(err, &toolErr) {
		t.Error("an infrastructure error was reclassified as a ToolError")
	}
}

func TestTypedPassesContextThrough(t *testing.T) {
	t.Parallel()

	type ctxKey struct{}
	tl := tool.Typed("ctx", "", "", func(ctx context.Context, _ domain.Principal, _ struct{}) (port.Result, error) {
		v, _ := ctx.Value(ctxKey{}).(string)
		return port.Result{Text: v}, nil
	})

	ctx := context.WithValue(context.Background(), ctxKey{}, "carried")
	got, err := tl.Execute(ctx, domain.Principal{Subject: "alice"}, nil)
	if err != nil {
		t.Fatalf("Execute error = %v", err)
	}
	if got.Text != "carried" {
		t.Errorf("Text = %q, want %q", got.Text, "carried")
	}
}

func TestTypedPanicsOnWiringMistakes(t *testing.T) {
	t.Parallel()

	t.Run("empty name", func(t *testing.T) {
		t.Parallel()
		defer func() {
			if recover() == nil {
				t.Error("Typed with an empty name did not panic")
			}
		}()
		tool.Typed("", "", "", greet)
	})

	t.Run("nil function", func(t *testing.T) {
		t.Parallel()
		defer func() {
			if recover() == nil {
				t.Error("Typed with a nil function did not panic")
			}
		}()
		tool.Typed[greetArgs]("greet", "", "", nil)
	})

	t.Run("undescribable argument type", func(t *testing.T) {
		t.Parallel()
		defer func() {
			if recover() == nil {
				t.Error("Typed with an unrepresentable argument type did not panic")
			}
		}()
		type bad struct {
			Fn func() `json:"fn"`
		}
		tool.Typed("bad", "", "", func(context.Context, domain.Principal, bad) (port.Result, error) {
			return port.Result{}, nil
		})
	})
}

var _ port.Tool = tool.Typed("compile", "", "", greet)

// The schema published to the model has to mean something. Decoding alone
// accepts a missing required field as a zero value and ignores what the schema
// declares, which would leave each tool re-checking by hand what it already
// told the model.
//
// What the derived schema can express is types, which fields are required
// (everything without omitempty) and that no other properties are allowed.
// Those are what is enforced here.
func TestTypedEnforcesThePublishedSchema(t *testing.T) {
	t.Parallel()

	type constrainedArgs struct {
		Name    string   `json:"name" jsonschema:"the name"`
		Count   int      `json:"count,omitempty"`
		Tags    []string `json:"tags,omitempty"`
		Enabled bool     `json:"enabled,omitempty"`
	}

	tl := tool.Typed("constrained", "", "", func(context.Context, domain.Principal, constrainedArgs) (port.Result, error) {
		return port.Result{Text: "ran"}, nil
	})

	tests := []struct {
		name string
		args string
		ok   bool
	}{
		{"everything supplied", `{"name":"x","count":5,"tags":["a"],"enabled":true}`, true},
		{"optional fields omitted", `{"name":"x"}`, true},
		{"required field missing", `{"count":5}`, false},
		{"string where an integer belongs", `{"name":"x","count":"five"}`, false},
		{"integer where a string belongs", `{"name":123}`, false},
		{"object where an array belongs", `{"name":"x","tags":{"a":1}}`, false},
		{"string where a boolean belongs", `{"name":"x","enabled":"yes"}`, false},
		{"an undeclared property", `{"name":"x","colour":"red"}`, false},
		{"not an object at all", `["name"]`, false},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			_, err := tl.Execute(context.Background(), domain.Principal{Subject: "alice"}, json.RawMessage(tc.args))
			switch {
			case tc.ok && err != nil:
				t.Errorf("Execute(%s) error = %v, want success", tc.args, err)
			case !tc.ok && err == nil:
				t.Errorf("Execute(%s) succeeded; the published schema is not enforced", tc.args)
			case !tc.ok:
				var toolErr *domain.ToolError
				if !errors.As(err, &toolErr) {
					t.Errorf("Execute(%s) error = %v (%T), want a *domain.ToolError", tc.args, err, err)
				}
			}
		})
	}
}

// A required field must be rejected rather than silently arriving as its zero
// value, which is the specific failure that motivated validating at all.
func TestTypedRejectsAMissingRequiredFieldRatherThanZeroing(t *testing.T) {
	t.Parallel()

	type args struct {
		Name string `json:"name"`
	}

	var reached bool
	tl := tool.Typed("required", "", "", func(_ context.Context, _ domain.Principal, in args) (port.Result, error) {
		reached = true
		return port.Result{Text: in.Name}, nil
	})

	if _, err := tl.Execute(context.Background(), domain.Principal{Subject: "alice"}, json.RawMessage(`{}`)); err == nil {
		t.Error("Execute succeeded with a required field missing")
	}
	if reached {
		t.Error("the tool ran with a zero value where a required argument should have been")
	}
}
