// Package notestest holds the contract every NoteStore adapter must satisfy.
//
// The point of having two adapters behind port.NoteStore is that a tool cannot
// tell them apart. That claim is only worth something if it is checked, so the
// behaviour the port promises is written once here and run against every
// implementation, rather than being restated — and quietly diverging — in each
// adapter's own tests.
//
// An adapter's own test file still covers what is specific to it: how the
// in-memory store behaves under concurrency, how the file store handles a
// corrupt file. This covers what they must agree on.
package notestest

import (
	"context"
	"fmt"
	"testing"

	"github.com/bartdelepeleer/mcpskeleton/internal/domain"
	"github.com/bartdelepeleer/mcpskeleton/internal/port"
)

// NewStore builds a fresh, empty store for one subtest. Implementations that
// need temporary files should use t.TempDir.
type NewStore func(t *testing.T) port.NoteStore

// RunContract runs every contract case against stores built by newStore.
func RunContract(t *testing.T, newStore NewStore) {
	t.Helper()

	cases := []struct {
		name string
		run  func(t *testing.T, s port.NoteStore)
	}{
		{"add returns a populated note", addReturnsAPopulatedNote},
		{"list is empty for an unknown owner", listIsEmptyForAnUnknownOwner},
		{"list returns only the owner's notes", listReturnsOnlyTheOwnersNotes},
		{"list returns oldest first", listReturnsOldestFirst},
		{"add refuses an empty owner", addRefusesAnEmptyOwner},
		{"ids are unique", idsAreUnique},
		{"created at is utc", createdAtIsUTC},
		{"empty bodies are stored verbatim", emptyBodiesAreStoredVerbatim},
		{"bodies are stored verbatim", bodiesAreStoredVerbatim},
		{"list does not expose the store's state", listDoesNotExposeState},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			tc.run(t, newStore(t))
		})
	}
}

func addReturnsAPopulatedNote(t *testing.T, s port.NoteStore) {
	got, err := s.Add(context.Background(), "alice", "buy milk")
	if err != nil {
		t.Fatalf("Add error = %v", err)
	}
	if got.ID == "" {
		t.Error("Add returned a note with no ID")
	}
	if got.Owner != "alice" {
		t.Errorf("Owner = %q, want %q", got.Owner, "alice")
	}
	if got.Body != "buy milk" {
		t.Errorf("Body = %q, want %q", got.Body, "buy milk")
	}
	if got.CreatedAt.IsZero() {
		t.Error("CreatedAt is zero")
	}
}

func listIsEmptyForAnUnknownOwner(t *testing.T, s port.NoteStore) {
	got, err := s.List(context.Background(), "nobody")
	if err != nil {
		t.Fatalf("List error = %v, want nil (an owner with no notes is not an error)", err)
	}
	if len(got) != 0 {
		t.Errorf("List = %v, want empty", got)
	}
}

// The isolation guarantee the notes tool relies on. It belongs to the port, so
// every adapter has to provide it.
func listReturnsOnlyTheOwnersNotes(t *testing.T, s port.NoteStore) {
	ctx := context.Background()
	mustAdd(t, s, "alice", "alice one")
	mustAdd(t, s, "bob", "bob one")
	mustAdd(t, s, "alice", "alice two")

	got, err := s.List(ctx, "alice")
	if err != nil {
		t.Fatalf("List error = %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("alice has %d notes, want 2", len(got))
	}
	for _, n := range got {
		if n.Owner != "alice" {
			t.Errorf("List returned a note owned by %q", n.Owner)
		}
	}
}

func listReturnsOldestFirst(t *testing.T, s port.NoteStore) {
	want := []string{"first", "second", "third"}
	for _, body := range want {
		mustAdd(t, s, "alice", body)
	}

	got, err := s.List(context.Background(), "alice")
	if err != nil {
		t.Fatalf("List error = %v", err)
	}
	if len(got) != len(want) {
		t.Fatalf("List returned %d notes, want %d", len(got), len(want))
	}
	for i, body := range want {
		if got[i].Body != body {
			t.Errorf("List()[%d].Body = %q, want %q", i, got[i].Body, body)
		}
	}
}

func addRefusesAnEmptyOwner(t *testing.T, s port.NoteStore) {
	if _, err := s.Add(context.Background(), "", "orphan"); err == nil {
		t.Error("Add with an empty owner succeeded; the note would belong to nobody")
	}
}

func idsAreUnique(t *testing.T, s port.NoteStore) {
	seen := make(map[string]bool)
	for i := range 25 {
		note := mustAdd(t, s, "alice", fmt.Sprintf("note %d", i))
		if seen[note.ID] {
			t.Fatalf("duplicate ID %q", note.ID)
		}
		seen[note.ID] = true
	}
}

func createdAtIsUTC(t *testing.T, s port.NoteStore) {
	note := mustAdd(t, s, "alice", "hi")
	if _, offset := note.CreatedAt.Zone(); offset != 0 {
		t.Errorf("CreatedAt zone offset = %d, want 0 (UTC)", offset)
	}
}

// Whether an empty body is meaningful is the tool's decision, not the store's.
// A store that silently rejected one would move that policy below the port.
func emptyBodiesAreStoredVerbatim(t *testing.T, s port.NoteStore) {
	if _, err := s.Add(context.Background(), "alice", ""); err != nil {
		t.Fatalf("Add with an empty body error = %v; validation belongs to the tool", err)
	}
	got, err := s.List(context.Background(), "alice")
	if err != nil {
		t.Fatalf("List error = %v", err)
	}
	if len(got) != 1 || got[0].Body != "" {
		t.Errorf("List = %v, want one note with an empty body", got)
	}
}

func bodiesAreStoredVerbatim(t *testing.T, s port.NoteStore) {
	bodies := []string{
		"  leading and trailing  ",
		"multi\nline\nbody",
		`{"looks":"like json"}`,
		"unicode: naïve café 日本語 🎉",
		`quotes "and" \backslashes\`,
	}
	for _, body := range bodies {
		mustAdd(t, s, "alice", body)
	}

	got, err := s.List(context.Background(), "alice")
	if err != nil {
		t.Fatalf("List error = %v", err)
	}
	if len(got) != len(bodies) {
		t.Fatalf("List returned %d notes, want %d", len(got), len(bodies))
	}
	for i, want := range bodies {
		if got[i].Body != want {
			t.Errorf("List()[%d].Body = %q, want %q", i, got[i].Body, want)
		}
	}
}

func listDoesNotExposeState(t *testing.T, s port.NoteStore) {
	mustAdd(t, s, "alice", "original")

	first, err := s.List(context.Background(), "alice")
	if err != nil {
		t.Fatalf("List error = %v", err)
	}
	first[0].Body = "clobbered"

	second, err := s.List(context.Background(), "alice")
	if err != nil {
		t.Fatalf("List error = %v", err)
	}
	if second[0].Body != "original" {
		t.Errorf("mutating a returned slice changed the store: %q", second[0].Body)
	}
}

func mustAdd(t *testing.T, s port.NoteStore, owner, body string) domain.Note {
	t.Helper()
	n, err := s.Add(context.Background(), owner, body)
	if err != nil {
		t.Fatalf("Add(%q, %q) error = %v", owner, body, err)
	}
	return n
}
