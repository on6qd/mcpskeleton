package registry_test

import (
	"encoding/json"
	"testing"

	"github.com/bartdelepeleer/mcpskeleton/internal/adapter/out/notes/memory"
	"github.com/bartdelepeleer/mcpskeleton/internal/adapter/tool/echo"
	"github.com/bartdelepeleer/mcpskeleton/internal/adapter/tool/notes"
	"github.com/bartdelepeleer/mcpskeleton/internal/registry"
)

func TestRegistryHoldsTheReferenceTools(t *testing.T) {
	t.Parallel()

	got := registry.New(memory.New()).Names()

	want := []string{"echo", "notes_add", "notes_list"}
	if len(got) != len(want) {
		t.Fatalf("tools = %v, want %v", got, want)
	}
	for i, name := range want {
		if got[i] != name {
			t.Errorf("tools[%d] = %q, want %q", i, got[i], name)
		}
	}
}

// Every registered tool has to be usable: a name, something for the model to
// read, and a schema a client can plan against. This is the check that catches
// a tool added to the slice with a piece missing.
func TestEveryToolIsWellFormed(t *testing.T) {
	t.Parallel()

	for _, tool := range registry.New(memory.New()).All() {
		t.Run(tool.Name(), func(t *testing.T) {
			t.Parallel()

			if tool.Name() == "" {
				t.Error("the tool has no name")
			}
			if tool.Description() == "" {
				t.Error("the tool has no description; the model has nothing to choose it by")
			}

			var schema map[string]any
			if err := json.Unmarshal(tool.InputSchema(), &schema); err != nil {
				t.Fatalf("the input schema is not valid JSON: %v", err)
			}
			// The SDK requires an object schema, and rejects a tool without one
			// at registration time rather than at call time.
			if schema["type"] != "object" {
				t.Errorf(`schema type = %v, want "object"`, schema["type"])
			}
		})
	}
}

func TestScopesAreTheOnesTheToolsDeclare(t *testing.T) {
	t.Parallel()

	got := registry.New(memory.New()).Scopes()

	want := []string{notes.ScopeRead, notes.ScopeWrite, echo.Scope}
	if len(got) != len(want) {
		t.Fatalf("scopes = %v, want the three the tools declare (%v)", got, want)
	}
	for _, scope := range want {
		found := false
		for _, s := range got {
			if s == scope {
				found = true
			}
		}
		if !found {
			t.Errorf("scopes = %v, missing %q", got, scope)
		}
	}
}
