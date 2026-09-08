package echo_test

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/bartdelepeleer/mcpskeleton/internal/adapter/tool/echo"
	"github.com/bartdelepeleer/mcpskeleton/internal/domain"
)

func alice() domain.Principal {
	return domain.Principal{Subject: "alice", Scopes: []string{echo.Scope}}
}

func TestEchoMetadata(t *testing.T) {
	t.Parallel()

	tl := echo.New()

	if got := tl.Name(); got != "echo" {
		t.Errorf("Name() = %q, want %q", got, "echo")
	}
	if got := tl.RequiredScope(); got != echo.Scope {
		t.Errorf("RequiredScope() = %q, want %q", got, echo.Scope)
	}
	if tl.Description() == "" {
		t.Error("Description() is empty; the model relies on it to choose the tool")
	}
}

func TestEchoSchemaRequiresMessageOnly(t *testing.T) {
	t.Parallel()

	var schema struct {
		Properties map[string]json.RawMessage `json:"properties"`
		Required   []string                   `json:"required"`
	}
	if err := json.Unmarshal(echo.New().InputSchema(), &schema); err != nil {
		t.Fatalf("InputSchema is not valid JSON: %v", err)
	}

	for _, field := range []string{"message", "times"} {
		if _, ok := schema.Properties[field]; !ok {
			t.Errorf("schema has no %q property", field)
		}
	}
	if len(schema.Required) != 1 || schema.Required[0] != "message" {
		t.Errorf("required = %v, want [message]", schema.Required)
	}
}

func TestEchoRepeats(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		args string
		want string
	}{
		{"defaults to once", `{"message":"hi"}`, "hi"},
		{"explicit once", `{"message":"hi","times":1}`, "hi"},
		{"repeats on separate lines", `{"message":"hi","times":3}`, "hi\nhi\nhi"},
		{"at the cap", `{"message":"x","times":10}`, strings.TrimSuffix(strings.Repeat("x\n", 10), "\n")},
		{"whitespace inside a message is preserved", `{"message":"a b  c"}`, "a b  c"},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			got, err := echo.New().Execute(context.Background(), alice(), json.RawMessage(tc.args))
			if err != nil {
				t.Fatalf("Execute error = %v", err)
			}
			if got.Text != tc.want {
				t.Errorf("Text = %q, want %q", got.Text, tc.want)
			}
		})
	}
}

// The tool reports who called it, which is what makes it useful for confirming
// end to end that identity reached the tool rather than stopping at the
// transport.
func TestEchoReportsTheCaller(t *testing.T) {
	t.Parallel()

	got, err := echo.New().Execute(
		context.Background(),
		domain.Principal{Subject: "bob"},
		json.RawMessage(`{"message":"hi"}`),
	)
	if err != nil {
		t.Fatalf("Execute error = %v", err)
	}

	structured, ok := got.Structured.(map[string]any)
	if !ok {
		t.Fatalf("Structured = %T, want map", got.Structured)
	}
	if structured["caller"] != "bob" {
		t.Errorf("Structured[caller] = %v, want %q", structured["caller"], "bob")
	}
	if structured["message"] != "hi" {
		t.Errorf("Structured[message] = %v, want %q", structured["message"], "hi")
	}
	if structured["times"] != 1 {
		t.Errorf("Structured[times] = %v, want 1", structured["times"])
	}
}

// Every rejection here is the model's mistake, so every one must be a
// ToolError it can read and retry against.
func TestEchoRejectionsAreToolErrors(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		args string
	}{
		{"empty message", `{"message":""}`},
		{"whitespace-only message", `{"message":"   "}`},
		{"missing message", `{"times":2}`},
		{"times above the cap", `{"message":"hi","times":11}`},
		{"negative times", `{"message":"hi","times":-1}`},
		{"unknown field", `{"message":"hi","repeat":2}`},
		{"malformed JSON", `{"message":`},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			_, err := echo.New().Execute(context.Background(), alice(), json.RawMessage(tc.args))
			if err == nil {
				t.Fatal("Execute error = nil, want a ToolError")
			}
			var toolErr *domain.ToolError
			if !errors.As(err, &toolErr) {
				t.Fatalf("Execute error = %v (%T), want a *domain.ToolError", err, err)
			}
		})
	}
}

// A missing required field is a schema violation the model should see, not a
// silent success on the zero value.
func TestEchoMissingMessageIsRejectedNotDefaulted(t *testing.T) {
	t.Parallel()

	_, err := echo.New().Execute(context.Background(), alice(), json.RawMessage(`{}`))
	if err == nil {
		t.Fatal("Execute with no message succeeded; the empty message was silently accepted")
	}
}
