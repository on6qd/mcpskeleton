package core_test

import (
	"context"
	"encoding/json"

	"github.com/bartdelepeleer/mcpskeleton/internal/domain"
	"github.com/bartdelepeleer/mcpskeleton/internal/port"
)

// fakeTool is a port.Tool whose behaviour each test sets directly. Tests fake at
// the port, never at a concrete type, so what is exercised is the contract the
// core actually depends on.
type fakeTool struct {
	name        string
	description string
	scope       string
	schema      json.RawMessage

	// exec, when set, is what Execute runs. When nil, Execute succeeds and
	// records what it was called with.
	exec func(ctx context.Context, p domain.Principal, args json.RawMessage) (port.Result, error)

	// calls records every Execute invocation, so a test can assert that a tool
	// was reached — or, more usefully, that it was not.
	calls []fakeCall
}

type fakeCall struct {
	principal domain.Principal
	args      json.RawMessage
}

func newFakeTool(name, scope string) *fakeTool {
	return &fakeTool{name: name, scope: scope, description: name + " description"}
}

func (f *fakeTool) Name() string          { return f.name }
func (f *fakeTool) Description() string   { return f.description }
func (f *fakeTool) RequiredScope() string { return f.scope }

func (f *fakeTool) InputSchema() json.RawMessage {
	if f.schema != nil {
		return f.schema
	}
	return json.RawMessage(`{"type":"object"}`)
}

func (f *fakeTool) Execute(ctx context.Context, p domain.Principal, args json.RawMessage) (port.Result, error) {
	f.calls = append(f.calls, fakeCall{principal: p, args: args})
	if f.exec != nil {
		return f.exec(ctx, p, args)
	}
	return port.Result{Text: f.name + " ran"}, nil
}

// allowAll and denyAll are authorizers that ignore scopes entirely, so a test
// can isolate dispatch behaviour from policy behaviour.
type allowAll struct{}

func (allowAll) Allow(domain.Principal, string) bool { return true }

type denyAll struct{}

func (denyAll) Allow(domain.Principal, string) bool { return false }

var (
	_ port.Tool       = (*fakeTool)(nil)
	_ port.Authorizer = allowAll{}
	_ port.Authorizer = denyAll{}
)
