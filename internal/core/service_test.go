package core_test

import (
	"context"
	"encoding/json"
	"errors"
	"testing"

	"github.com/bartdelepeleer/mcpskeleton/internal/core"
	"github.com/bartdelepeleer/mcpskeleton/internal/domain"
	"github.com/bartdelepeleer/mcpskeleton/internal/port"
)

func TestServiceListToolsFiltersByScope(t *testing.T) {
	t.Parallel()

	r := core.NewRegistry(
		newFakeTool("echo", "tools:echo"),
		newFakeTool("notes_read", "notes:read"),
		newFakeTool("notes_write", "notes:write"),
		newFakeTool("open", ""),
	)
	svc := core.NewService(r, core.ScopeAuthorizer{})

	alice := domain.Principal{Subject: "alice", Scopes: []string{"tools:echo", "notes:read"}}

	got := svc.ListTools(alice)

	want := []string{"echo", "notes_read", "open"}
	if len(got) != len(want) {
		t.Fatalf("ListTools returned %d tools (%v), want %d (%v)", len(got), names(got), len(want), want)
	}
	for i, name := range want {
		if got[i].Name != name {
			t.Errorf("ListTools()[%d] = %q, want %q", i, got[i].Name, name)
		}
	}
}

func TestServiceListToolsEmptyForUnscopedPrincipal(t *testing.T) {
	t.Parallel()

	r := core.NewRegistry(newFakeTool("echo", "tools:echo"))
	svc := core.NewService(r, core.ScopeAuthorizer{})

	if got := svc.ListTools(domain.Principal{Subject: "nobody"}); len(got) != 0 {
		t.Errorf("ListTools() = %v, want empty", names(got))
	}
}

func TestServiceListToolsCarriesMetadata(t *testing.T) {
	t.Parallel()

	tool := newFakeTool("echo", "")
	tool.description = "repeat a message"
	tool.schema = json.RawMessage(`{"type":"object","properties":{"message":{"type":"string"}}}`)

	svc := core.NewService(core.NewRegistry(tool), allowAll{})

	got := svc.ListTools(domain.Principal{Subject: "alice"})
	if len(got) != 1 {
		t.Fatalf("ListTools returned %d tools, want 1", len(got))
	}
	if got[0].Description != "repeat a message" {
		t.Errorf("Description = %q, want %q", got[0].Description, "repeat a message")
	}
	if string(got[0].InputSchema) != string(tool.schema) {
		t.Errorf("InputSchema = %s, want %s", got[0].InputSchema, tool.schema)
	}
}

func TestServiceCallToolSuccess(t *testing.T) {
	t.Parallel()

	tool := newFakeTool("echo", "tools:echo")
	tool.exec = func(_ context.Context, p domain.Principal, args json.RawMessage) (port.Result, error) {
		return port.Result{Text: "hello " + p.Subject, Structured: map[string]any{"ok": true}}, nil
	}
	svc := core.NewService(core.NewRegistry(tool), core.ScopeAuthorizer{})

	alice := domain.Principal{Subject: "alice", Scopes: []string{"tools:echo"}}
	out, err := svc.CallTool(context.Background(), alice, "echo", json.RawMessage(`{"a":1}`))
	if err != nil {
		t.Fatalf("CallTool error = %v", err)
	}
	if out.Failure != nil {
		t.Fatalf("Failure = %v, want nil", out.Failure)
	}
	if out.Result.Text != "hello alice" {
		t.Errorf("Result.Text = %q, want %q", out.Result.Text, "hello alice")
	}
	if out.Result.Structured == nil {
		t.Error("Result.Structured was dropped")
	}
}

// The whole point of the explicit principal parameter: the identity that
// authenticated must be the identity the tool sees, unmodified.
func TestServiceCallToolPassesPrincipalAndArgsThrough(t *testing.T) {
	t.Parallel()

	tool := newFakeTool("echo", "")
	svc := core.NewService(core.NewRegistry(tool), allowAll{})

	alice := domain.Principal{Subject: "alice", Scopes: []string{"notes:read"}}
	args := json.RawMessage(`{"message":"hi"}`)

	if _, err := svc.CallTool(context.Background(), alice, "echo", args); err != nil {
		t.Fatalf("CallTool error = %v", err)
	}

	if len(tool.calls) != 1 {
		t.Fatalf("tool was called %d times, want 1", len(tool.calls))
	}
	got := tool.calls[0]
	if got.principal.Subject != "alice" {
		t.Errorf("tool saw subject %q, want %q", got.principal.Subject, "alice")
	}
	if len(got.principal.Scopes) != 1 || got.principal.Scopes[0] != "notes:read" {
		t.Errorf("tool saw scopes %v, want [notes:read]", got.principal.Scopes)
	}
	if string(got.args) != string(args) {
		t.Errorf("tool saw args %s, want %s", got.args, args)
	}
}

func TestServiceCallToolUnknown(t *testing.T) {
	t.Parallel()

	svc := core.NewService(core.NewRegistry(newFakeTool("echo", "")), allowAll{})

	_, err := svc.CallTool(context.Background(), domain.Principal{Subject: "alice"}, "nope", nil)
	if !errors.Is(err, domain.ErrToolNotFound) {
		t.Errorf("error = %v, want one wrapping ErrToolNotFound", err)
	}
}

func TestServiceCallToolForbidden(t *testing.T) {
	t.Parallel()

	tool := newFakeTool("notes_write", "notes:write")
	svc := core.NewService(core.NewRegistry(tool), core.ScopeAuthorizer{})

	alice := domain.Principal{Subject: "alice", Scopes: []string{"notes:read"}}
	_, err := svc.CallTool(context.Background(), alice, "notes_write", nil)

	if !errors.Is(err, domain.ErrForbidden) {
		t.Fatalf("error = %v, want one wrapping ErrForbidden", err)
	}
	// A denied call must not reach the tool. If it does, every tool becomes
	// responsible for its own authorization and the central check is theatre.
	if len(tool.calls) != 0 {
		t.Errorf("tool ran despite being forbidden (%d calls)", len(tool.calls))
	}
}

// A tool-level failure is something the model reads and may retry against, so
// it comes back as an outcome, not as an error.
func TestServiceCallToolDomainFailureIsNotAnError(t *testing.T) {
	t.Parallel()

	tool := newFakeTool("notes_add", "")
	tool.exec = func(context.Context, domain.Principal, json.RawMessage) (port.Result, error) {
		return port.Result{}, domain.NewToolError("body must not be empty")
	}
	svc := core.NewService(core.NewRegistry(tool), allowAll{})

	out, err := svc.CallTool(context.Background(), domain.Principal{Subject: "alice"}, "notes_add", nil)
	if err != nil {
		t.Fatalf("CallTool error = %v, want nil (a tool failure is not an infrastructure error)", err)
	}
	if out.Failure == nil {
		t.Fatal("Failure = nil, want the tool's error")
	}
	if out.Failure.Message != "body must not be empty" {
		t.Errorf("Failure.Message = %q, want %q", out.Failure.Message, "body must not be empty")
	}
}

// A ToolError still classifies as one when the tool wrapped it around a cause.
func TestServiceCallToolWrappedDomainFailure(t *testing.T) {
	t.Parallel()

	cause := errors.New("record locked")
	tool := newFakeTool("notes_add", "")
	tool.exec = func(context.Context, domain.Principal, json.RawMessage) (port.Result, error) {
		return port.Result{}, domain.WrapToolError(cause, "could not save note")
	}
	svc := core.NewService(core.NewRegistry(tool), allowAll{})

	out, err := svc.CallTool(context.Background(), domain.Principal{Subject: "alice"}, "notes_add", nil)
	if err != nil {
		t.Fatalf("CallTool error = %v, want nil", err)
	}
	if out.Failure == nil || out.Failure.Message != "could not save note" {
		t.Fatalf("Failure = %v, want the wrapped tool error", out.Failure)
	}
	// The cause stays reachable for logs but is not part of the model-facing
	// message.
	if !errors.Is(out.Failure, cause) {
		t.Error("the underlying cause was lost")
	}
}

// Anything that is not a ToolError is infrastructure: it must not be handed to
// the model as a retryable tool result.
func TestServiceCallToolInfrastructureFailureIsAnError(t *testing.T) {
	t.Parallel()

	boom := errors.New("connection refused")
	tool := newFakeTool("notes_add", "")
	tool.exec = func(context.Context, domain.Principal, json.RawMessage) (port.Result, error) {
		return port.Result{}, boom
	}
	svc := core.NewService(core.NewRegistry(tool), allowAll{})

	out, err := svc.CallTool(context.Background(), domain.Principal{Subject: "alice"}, "notes_add", nil)
	if err == nil {
		t.Fatal("CallTool error = nil, want the infrastructure failure")
	}
	if !errors.Is(err, boom) {
		t.Errorf("error = %v, want one wrapping %v", err, boom)
	}
	if out.Failure != nil {
		t.Error("an infrastructure failure leaked into the model-facing channel")
	}
}

func TestNewServicePanicsOnNilDependencies(t *testing.T) {
	t.Parallel()

	t.Run("nil registry", func(t *testing.T) {
		t.Parallel()
		defer func() {
			if recover() == nil {
				t.Error("NewService(nil, authz) did not panic")
			}
		}()
		core.NewService(nil, allowAll{})
	})

	t.Run("nil authorizer", func(t *testing.T) {
		t.Parallel()
		defer func() {
			if recover() == nil {
				t.Error("NewService(registry, nil) did not panic")
			}
		}()
		core.NewService(core.NewRegistry(), nil)
	})
}

func names(infos []core.ToolInfo) []string {
	out := make([]string, len(infos))
	for i, info := range infos {
		out[i] = info.Name
	}
	return out
}
