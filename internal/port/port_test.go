package port_test

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/bartdelepeleer/mcpskeleton/internal/domain"
	"github.com/bartdelepeleer/mcpskeleton/internal/port"
)

// The ports have no behaviour of their own, so what is worth testing is that
// the signatures are implementable as designed and stay stable. These stubs are
// compiled against the interfaces; a signature change breaks the build here
// before it breaks a real adapter.

type stubAuthenticator struct{}

func (stubAuthenticator) Authenticate(context.Context, string) (domain.Principal, error) {
	return domain.Principal{}, nil
}

type stubAuthorizer struct{}

func (stubAuthorizer) Allow(domain.Principal, string) bool { return true }

type stubTool struct{}

func (stubTool) Name() string                 { return "stub" }
func (stubTool) Description() string          { return "" }
func (stubTool) RequiredScope() string        { return "" }
func (stubTool) InputSchema() json.RawMessage { return json.RawMessage(`{"type":"object"}`) }
func (stubTool) Execute(context.Context, domain.Principal, json.RawMessage) (port.Result, error) {
	return port.Result{}, nil
}

type stubNoteStore struct{}

func (stubNoteStore) Add(context.Context, string, string) (domain.Note, error) {
	return domain.Note{}, nil
}
func (stubNoteStore) List(context.Context, string) ([]domain.Note, error) { return nil, nil }

var (
	_ port.Authenticator = stubAuthenticator{}
	_ port.Authorizer    = stubAuthorizer{}
	_ port.Tool          = stubTool{}
	_ port.NoteStore     = stubNoteStore{}
)

// A tool's identity parameter must be a real parameter, not something recovered
// from the context. This asserts the shape the rest of the codebase relies on.
func TestToolExecuteTakesPrincipalExplicitly(t *testing.T) {
	t.Parallel()

	var tool port.Tool = stubTool{}
	got, err := tool.Execute(context.Background(), domain.Principal{Subject: "alice"}, nil)
	if err != nil {
		t.Fatalf("Execute() error = %v", err)
	}
	if got.Text != "" || got.Structured != nil {
		t.Errorf("zero Result = %+v, want empty", got)
	}
}

// InputSchema is raw JSON so that this package needs no schema library. Whatever
// produces it must still produce valid JSON.
func TestInputSchemaIsValidJSON(t *testing.T) {
	t.Parallel()

	var schema any
	if err := json.Unmarshal(stubTool{}.InputSchema(), &schema); err != nil {
		t.Fatalf("InputSchema is not valid JSON: %v", err)
	}
}
