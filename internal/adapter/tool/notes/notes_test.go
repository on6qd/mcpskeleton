package notes_test

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/bartdelepeleer/mcpskeleton/internal/adapter/out/notes/memory"
	"github.com/bartdelepeleer/mcpskeleton/internal/adapter/tool/notes"
	"github.com/bartdelepeleer/mcpskeleton/internal/domain"
	"github.com/bartdelepeleer/mcpskeleton/internal/port"
)

func principal(subject string) domain.Principal {
	return domain.Principal{Subject: subject, Scopes: []string{notes.ScopeRead, notes.ScopeWrite}}
}

func TestMetadata(t *testing.T) {
	t.Parallel()

	store := memory.New()

	tests := []struct {
		tool  port.Tool
		name  string
		scope string
	}{
		{notes.NewAdd(store), "notes_add", notes.ScopeWrite},
		{notes.NewList(store), "notes_list", notes.ScopeRead},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			if got := tc.tool.Name(); got != tc.name {
				t.Errorf("Name() = %q, want %q", got, tc.name)
			}
			if got := tc.tool.RequiredScope(); got != tc.scope {
				t.Errorf("RequiredScope() = %q, want %q", got, tc.scope)
			}
			if tc.tool.Description() == "" {
				t.Error("Description() is empty")
			}
		})
	}
}

// Reading and writing carry separate scopes so a token can be issued with one
// and not the other. If they ever collapse to the same string, the filtering
// this skeleton demonstrates stops being visible.
func TestReadAndWriteScopesDiffer(t *testing.T) {
	t.Parallel()

	if notes.ScopeRead == notes.ScopeWrite {
		t.Fatal("read and write share a scope; a read-only token becomes impossible")
	}
}

// The central guarantee of this tool: a caller cannot say whose note it is.
// The schema has nowhere to put an owner, so it cannot be spoofed or mistyped.
func TestAddSchemaHasNoOwnerField(t *testing.T) {
	t.Parallel()

	var schema struct {
		Properties map[string]json.RawMessage `json:"properties"`
		Required   []string                   `json:"required"`
	}
	if err := json.Unmarshal(notes.NewAdd(memory.New()).InputSchema(), &schema); err != nil {
		t.Fatalf("InputSchema is not valid JSON: %v", err)
	}

	for _, forbidden := range []string{"owner", "subject", "user"} {
		if _, ok := schema.Properties[forbidden]; ok {
			t.Errorf("schema exposes a %q property; ownership must come from the principal", forbidden)
		}
	}
	if len(schema.Required) != 1 || schema.Required[0] != "body" {
		t.Errorf("required = %v, want [body]", schema.Required)
	}
}

func TestAddThenList(t *testing.T) {
	t.Parallel()

	store := memory.New()
	add, list := notes.NewAdd(store), notes.NewList(store)
	ctx := context.Background()
	alice := principal("alice")

	got, err := add.Execute(ctx, alice, json.RawMessage(`{"body":"buy milk"}`))
	if err != nil {
		t.Fatalf("notes_add error = %v", err)
	}
	if !strings.HasPrefix(got.Text, "Saved note ") {
		t.Errorf("notes_add Text = %q, want a confirmation naming the note", got.Text)
	}

	listed, err := list.Execute(ctx, alice, nil)
	if err != nil {
		t.Fatalf("notes_list error = %v", err)
	}
	if !strings.Contains(listed.Text, "buy milk") {
		t.Errorf("notes_list Text = %q, want it to contain the note", listed.Text)
	}
}

// The note must be filed under the principal, not under anything the caller
// supplied. This checks the store directly rather than trusting the tool's own
// output.
func TestAddFilesTheNoteUnderThePrincipal(t *testing.T) {
	t.Parallel()

	store := memory.New()
	ctx := context.Background()

	if _, err := notes.NewAdd(store).Execute(ctx, principal("alice"), json.RawMessage(`{"body":"hi"}`)); err != nil {
		t.Fatalf("notes_add error = %v", err)
	}

	stored, err := store.List(ctx, "alice")
	if err != nil {
		t.Fatalf("store.List error = %v", err)
	}
	if len(stored) != 1 {
		t.Fatalf("alice has %d notes in the store, want 1", len(stored))
	}
	if stored[0].Owner != "alice" {
		t.Errorf("note owner = %q, want %q", stored[0].Owner, "alice")
	}
}

// An attempt to name someone else must be rejected outright rather than
// silently ignored: the model should learn that the field does not exist.
func TestAddRejectsAnOwnerArgument(t *testing.T) {
	t.Parallel()

	store := memory.New()
	ctx := context.Background()

	_, err := notes.NewAdd(store).Execute(ctx, principal("alice"),
		json.RawMessage(`{"body":"hi","owner":"bob"}`))
	if err == nil {
		t.Fatal("notes_add accepted an owner argument")
	}
	var toolErr *domain.ToolError
	if !errors.As(err, &toolErr) {
		t.Errorf("error = %v (%T), want a *domain.ToolError", err, err)
	}

	bobNotes, err := store.List(ctx, "bob")
	if err != nil {
		t.Fatalf("store.List error = %v", err)
	}
	if len(bobNotes) != 0 {
		t.Errorf("a note was filed under bob: %v", bobNotes)
	}
}

// One user must never see another's notes through this tool.
func TestListIsScopedToTheCaller(t *testing.T) {
	t.Parallel()

	store := memory.New()
	add, list := notes.NewAdd(store), notes.NewList(store)
	ctx := context.Background()

	if _, err := add.Execute(ctx, principal("alice"), json.RawMessage(`{"body":"alice secret"}`)); err != nil {
		t.Fatalf("notes_add for alice error = %v", err)
	}
	if _, err := add.Execute(ctx, principal("bob"), json.RawMessage(`{"body":"bob secret"}`)); err != nil {
		t.Fatalf("notes_add for bob error = %v", err)
	}

	got, err := list.Execute(ctx, principal("alice"), nil)
	if err != nil {
		t.Fatalf("notes_list error = %v", err)
	}
	if strings.Contains(got.Text, "bob secret") {
		t.Errorf("alice's listing contains bob's note: %q", got.Text)
	}
	if !strings.Contains(got.Text, "alice secret") {
		t.Errorf("alice's listing is missing her own note: %q", got.Text)
	}
}

// An empty listing is a normal outcome. Saying so in words stops a model
// reading a blank response as a failure.
func TestListWithNoNotesSaysSo(t *testing.T) {
	t.Parallel()

	got, err := notes.NewList(memory.New()).Execute(context.Background(), principal("alice"), nil)
	if err != nil {
		t.Fatalf("notes_list error = %v", err)
	}
	if got.Text != "No notes." {
		t.Errorf("Text = %q, want %q", got.Text, "No notes.")
	}

	structured, ok := got.Structured.(map[string]any)
	if !ok {
		t.Fatalf("Structured = %T, want map", got.Structured)
	}
	if structured["notes"] == nil {
		t.Error("Structured has no notes key")
	}
}

// The caller only ever sees their own notes, so the owner is not repeated back.
func TestListOutputOmitsTheOwner(t *testing.T) {
	t.Parallel()

	store := memory.New()
	ctx := context.Background()
	if _, err := notes.NewAdd(store).Execute(ctx, principal("alice"), json.RawMessage(`{"body":"hi"}`)); err != nil {
		t.Fatalf("notes_add error = %v", err)
	}

	got, err := notes.NewList(store).Execute(ctx, principal("alice"), nil)
	if err != nil {
		t.Fatalf("notes_list error = %v", err)
	}
	encoded, err := json.Marshal(got.Structured)
	if err != nil {
		t.Fatalf("marshalling structured output: %v", err)
	}
	if strings.Contains(string(encoded), "owner") {
		t.Errorf("structured output names the owner: %s", encoded)
	}
}

func TestAddRejectionsAreToolErrors(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		args string
	}{
		{"empty body", `{"body":""}`},
		{"whitespace-only body", `{"body":"   \n\t "}`},
		{"missing body", `{}`},
		{"body over the cap", `{"body":"` + strings.Repeat("x", 5000) + `"}`},
		{"malformed JSON", `{"body":`},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			_, err := notes.NewAdd(memory.New()).Execute(context.Background(), principal("alice"), json.RawMessage(tc.args))
			if err == nil {
				t.Fatal("notes_add error = nil, want a ToolError")
			}
			var toolErr *domain.ToolError
			if !errors.As(err, &toolErr) {
				t.Fatalf("error = %v (%T), want a *domain.ToolError", err, err)
			}
		})
	}
}

// A storage failure is not something the model can fix, so it must stay an
// infrastructure error and never be handed to it as a retryable tool result.
func TestStorageFailuresAreNotToolErrors(t *testing.T) {
	t.Parallel()

	boom := errors.New("disk on fire")
	store := failingStore{err: boom}
	ctx := context.Background()

	t.Run("add", func(t *testing.T) {
		t.Parallel()
		_, err := notes.NewAdd(store).Execute(ctx, principal("alice"), json.RawMessage(`{"body":"hi"}`))
		assertInfrastructureError(t, err, boom)
	})

	t.Run("list", func(t *testing.T) {
		t.Parallel()
		_, err := notes.NewList(store).Execute(ctx, principal("alice"), nil)
		assertInfrastructureError(t, err, boom)
	})
}

func assertInfrastructureError(t *testing.T, err, cause error) {
	t.Helper()
	if err == nil {
		t.Fatal("error = nil, want the storage failure")
	}
	if !errors.Is(err, cause) {
		t.Errorf("error = %v, want one wrapping %v", err, cause)
	}
	var toolErr *domain.ToolError
	if errors.As(err, &toolErr) {
		t.Error("a storage failure was handed to the model as a tool error")
	}
}

func TestAllReturnsBothTools(t *testing.T) {
	t.Parallel()

	got := notes.All(memory.New())
	if len(got) != 2 {
		t.Fatalf("All returned %d tools, want 2", len(got))
	}
	names := map[string]bool{got[0].Name(): true, got[1].Name(): true}
	for _, want := range []string{"notes_add", "notes_list"} {
		if !names[want] {
			t.Errorf("All did not return %q", want)
		}
	}
}

func TestNilStorePanics(t *testing.T) {
	t.Parallel()

	for _, tc := range []struct {
		name string
		make func()
	}{
		{"add", func() { notes.NewAdd(nil) }},
		{"list", func() { notes.NewList(nil) }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			defer func() {
				if recover() == nil {
					t.Error("constructing with a nil store did not panic")
				}
			}()
			tc.make()
		})
	}
}

// failingStore is a port.NoteStore that always fails, so the tool's handling of
// storage failures can be exercised without breaking a real one.
type failingStore struct{ err error }

func (f failingStore) Add(context.Context, string, string) (domain.Note, error) {
	return domain.Note{}, f.err
}

func (f failingStore) List(context.Context, string) ([]domain.Note, error) {
	return nil, f.err
}

var _ port.NoteStore = failingStore{}
