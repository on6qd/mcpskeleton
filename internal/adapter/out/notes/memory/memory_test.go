package memory_test

import (
	"context"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/bartdelepeleer/mcpskeleton/internal/adapter/out/notes/memory"
)

func TestAddReturnsAPopulatedNote(t *testing.T) {
	t.Parallel()

	at := time.Date(2026, 9, 8, 10, 0, 0, 0, time.UTC)
	s := memory.New(
		memory.WithClock(func() time.Time { return at }),
		memory.WithIDs(func() string { return "note-1" }),
	)

	got, err := s.Add(context.Background(), "alice", "buy milk")
	if err != nil {
		t.Fatalf("Add error = %v", err)
	}

	if got.ID != "note-1" {
		t.Errorf("ID = %q, want %q", got.ID, "note-1")
	}
	if got.Owner != "alice" {
		t.Errorf("Owner = %q, want %q", got.Owner, "alice")
	}
	if got.Body != "buy milk" {
		t.Errorf("Body = %q, want %q", got.Body, "buy milk")
	}
	if !got.CreatedAt.Equal(at) {
		t.Errorf("CreatedAt = %v, want %v", got.CreatedAt, at)
	}
}

// Times are stored in UTC so that two adapters, or two deployments in different
// zones, cannot disagree about when a note was written.
func TestAddNormalisesTimeToUTC(t *testing.T) {
	t.Parallel()

	berlin, err := time.LoadLocation("Europe/Berlin")
	if err != nil {
		t.Skipf("no tzdata available: %v", err)
	}
	local := time.Date(2026, 9, 8, 12, 0, 0, 0, berlin)
	s := memory.New(memory.WithClock(func() time.Time { return local }))

	got, err := s.Add(context.Background(), "alice", "hi")
	if err != nil {
		t.Fatalf("Add error = %v", err)
	}
	if got.CreatedAt.Location() != time.UTC {
		t.Errorf("CreatedAt location = %v, want UTC", got.CreatedAt.Location())
	}
	if !got.CreatedAt.Equal(local) {
		t.Errorf("CreatedAt = %v, want the same instant as %v", got.CreatedAt, local)
	}
}

func TestListIsEmptyForAnUnknownOwner(t *testing.T) {
	t.Parallel()

	got, err := memory.New().List(context.Background(), "nobody")
	if err != nil {
		t.Fatalf("List error = %v, want nil (an owner with no notes is not an error)", err)
	}
	if len(got) != 0 {
		t.Errorf("List = %v, want empty", got)
	}
}

func TestListReturnsOldestFirst(t *testing.T) {
	t.Parallel()

	s := memory.New()
	ctx := context.Background()
	for _, body := range []string{"first", "second", "third"} {
		if _, err := s.Add(ctx, "alice", body); err != nil {
			t.Fatalf("Add(%q) error = %v", body, err)
		}
	}

	got, err := s.List(ctx, "alice")
	if err != nil {
		t.Fatalf("List error = %v", err)
	}
	want := []string{"first", "second", "third"}
	if len(got) != len(want) {
		t.Fatalf("List returned %d notes, want %d", len(got), len(want))
	}
	for i, body := range want {
		if got[i].Body != body {
			t.Errorf("List()[%d].Body = %q, want %q", i, got[i].Body, body)
		}
	}
}

// Insertion order rather than CreatedAt is the ordering, so notes written
// within one clock tick keep the order they arrived in.
func TestListOrderSurvivesAFrozenClock(t *testing.T) {
	t.Parallel()

	frozen := time.Date(2026, 9, 8, 10, 0, 0, 0, time.UTC)
	s := memory.New(memory.WithClock(func() time.Time { return frozen }))
	ctx := context.Background()

	for i := range 5 {
		if _, err := s.Add(ctx, "alice", fmt.Sprintf("note %d", i)); err != nil {
			t.Fatalf("Add error = %v", err)
		}
	}

	got, err := s.List(ctx, "alice")
	if err != nil {
		t.Fatalf("List error = %v", err)
	}
	for i := range got {
		if want := fmt.Sprintf("note %d", i); got[i].Body != want {
			t.Errorf("List()[%d].Body = %q, want %q", i, got[i].Body, want)
		}
	}
}

// The property the notes tool depends on: one user cannot read another's notes,
// and that guarantee lives here rather than in the tool.
func TestOwnersAreIsolated(t *testing.T) {
	t.Parallel()

	s := memory.New()
	ctx := context.Background()

	if _, err := s.Add(ctx, "alice", "alice's secret"); err != nil {
		t.Fatalf("Add error = %v", err)
	}
	if _, err := s.Add(ctx, "bob", "bob's secret"); err != nil {
		t.Fatalf("Add error = %v", err)
	}

	aliceNotes, err := s.List(ctx, "alice")
	if err != nil {
		t.Fatalf("List error = %v", err)
	}
	if len(aliceNotes) != 1 || aliceNotes[0].Body != "alice's secret" {
		t.Fatalf("alice sees %v, want only her own note", aliceNotes)
	}

	bobNotes, err := s.List(ctx, "bob")
	if err != nil {
		t.Fatalf("List error = %v", err)
	}
	if len(bobNotes) != 1 || bobNotes[0].Body != "bob's secret" {
		t.Fatalf("bob sees %v, want only his own note", bobNotes)
	}
}

func TestAddRefusesAnEmptyOwner(t *testing.T) {
	t.Parallel()

	if _, err := memory.New().Add(context.Background(), "", "orphan"); err == nil {
		t.Error("Add with an empty owner succeeded; the note would belong to nobody")
	}
}

// A caller must not be able to reach the store's own state through a slice it
// handed out.
func TestListReturnsACopy(t *testing.T) {
	t.Parallel()

	s := memory.New()
	ctx := context.Background()
	if _, err := s.Add(ctx, "alice", "original"); err != nil {
		t.Fatalf("Add error = %v", err)
	}

	first, err := s.List(ctx, "alice")
	if err != nil {
		t.Fatalf("List error = %v", err)
	}
	first[0].Body = "clobbered"

	second, err := s.List(ctx, "alice")
	if err != nil {
		t.Fatalf("List error = %v", err)
	}
	if second[0].Body != "original" {
		t.Errorf("mutating a returned slice changed the store: %q", second[0].Body)
	}
}

func TestGeneratedIDsAreUnique(t *testing.T) {
	t.Parallel()

	s := memory.New()
	ctx := context.Background()
	seen := make(map[string]bool)

	for range 100 {
		note, err := s.Add(ctx, "alice", "body")
		if err != nil {
			t.Fatalf("Add error = %v", err)
		}
		if note.ID == "" {
			t.Fatal("Add returned a note with no ID")
		}
		if seen[note.ID] {
			t.Fatalf("duplicate ID %q", note.ID)
		}
		seen[note.ID] = true
	}
}

// The store is mutated by concurrent requests, so unlike the registry its
// safety comes from a lock. This fails under -race if that lock is ever
// dropped or narrowed.
func TestConcurrentAddAndList(t *testing.T) {
	t.Parallel()

	s := memory.New()
	ctx := context.Background()

	var wg sync.WaitGroup
	for i := range 50 {
		wg.Add(2)
		go func() {
			defer wg.Done()
			owner := fmt.Sprintf("user-%d", i%5)
			if _, err := s.Add(ctx, owner, "body"); err != nil {
				t.Errorf("Add error = %v", err)
			}
		}()
		go func() {
			defer wg.Done()
			if _, err := s.List(ctx, fmt.Sprintf("user-%d", i%5)); err != nil {
				t.Errorf("List error = %v", err)
			}
		}()
	}
	wg.Wait()

	total := 0
	for i := range 5 {
		notes, err := s.List(ctx, fmt.Sprintf("user-%d", i))
		if err != nil {
			t.Fatalf("List error = %v", err)
		}
		total += len(notes)
	}
	if total != 50 {
		t.Errorf("stored %d notes in total, want 50", total)
	}
}
