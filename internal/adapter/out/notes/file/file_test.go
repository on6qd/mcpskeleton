package file_test

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/bartdelepeleer/mcpskeleton/internal/adapter/out/notes/file"
)

func newStore(t *testing.T, path string, opts ...file.Option) *file.Store {
	t.Helper()
	s, err := file.New(path, opts...)
	if err != nil {
		t.Fatalf("file.New(%q) error = %v", path, err)
	}
	return s
}

// The reason this adapter exists: notes outlive the process.
func TestNotesSurviveANewStore(t *testing.T) {
	t.Parallel()

	path := filepath.Join(t.TempDir(), "notes.json")
	ctx := context.Background()

	first := newStore(t, path)
	if _, err := first.Add(ctx, "alice", "buy milk"); err != nil {
		t.Fatalf("Add error = %v", err)
	}

	// A completely separate store, as after a restart.
	second := newStore(t, path)
	got, err := second.List(ctx, "alice")
	if err != nil {
		t.Fatalf("List error = %v", err)
	}
	if len(got) != 1 || got[0].Body != "buy milk" {
		t.Fatalf("after reopening, List = %v, want the stored note", got)
	}
}

// Constructing a store must not create state the operator did not ask for;
// starting the server should not litter the filesystem.
func TestNewDoesNotCreateTheFile(t *testing.T) {
	t.Parallel()

	path := filepath.Join(t.TempDir(), "notes.json")
	newStore(t, path)

	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Errorf("New created %s; it should appear on first Add", path)
	}
}

func TestMissingFileIsAnEmptyStore(t *testing.T) {
	t.Parallel()

	s := newStore(t, filepath.Join(t.TempDir(), "absent.json"))

	got, err := s.List(context.Background(), "alice")
	if err != nil {
		t.Fatalf("List on a missing file error = %v, want nil", err)
	}
	if len(got) != 0 {
		t.Errorf("List = %v, want empty", got)
	}
}

func TestAddCreatesTheFileAndAnyMissingDirectories(t *testing.T) {
	t.Parallel()

	path := filepath.Join(t.TempDir(), "nested", "deeper", "notes.json")
	s := newStore(t, path)

	if _, err := s.Add(context.Background(), "alice", "hi"); err != nil {
		t.Fatalf("Add error = %v", err)
	}
	if _, err := os.Stat(path); err != nil {
		t.Fatalf("expected %s to exist: %v", path, err)
	}
}

// Notes are user data. A file the store cannot parse might still be recoverable
// by hand, so it must refuse rather than start over on top of it.
func TestCorruptFileIsAnErrorNotAFreshStart(t *testing.T) {
	t.Parallel()

	path := filepath.Join(t.TempDir(), "notes.json")
	if err := os.WriteFile(path, []byte(`{"notes": [ this is not json`), 0o600); err != nil {
		t.Fatalf("seeding the file: %v", err)
	}
	s := newStore(t, path)
	ctx := context.Background()

	if _, err := s.List(ctx, "alice"); err == nil {
		t.Error("List on a corrupt file succeeded; the damage would go unnoticed")
	}
	if _, err := s.Add(ctx, "alice", "hi"); err == nil {
		t.Error("Add on a corrupt file succeeded; it would have overwritten recoverable data")
	}

	// And the original bytes are still there for an operator to rescue.
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("reading back: %v", err)
	}
	if len(data) == 0 {
		t.Error("the corrupt file was truncated")
	}
}

// An empty file is a plausible result of a failed write elsewhere, and is
// unambiguous: there is nothing to lose by treating it as empty.
func TestEmptyFileIsAnEmptyStore(t *testing.T) {
	t.Parallel()

	path := filepath.Join(t.TempDir(), "notes.json")
	if err := os.WriteFile(path, nil, 0o600); err != nil {
		t.Fatalf("seeding the file: %v", err)
	}

	got, err := newStore(t, path).List(context.Background(), "alice")
	if err != nil {
		t.Fatalf("List on an empty file error = %v, want nil", err)
	}
	if len(got) != 0 {
		t.Errorf("List = %v, want empty", got)
	}
}

func TestFileIsReadableJSON(t *testing.T) {
	t.Parallel()

	path := filepath.Join(t.TempDir(), "notes.json")
	at := time.Date(2026, 9, 8, 10, 0, 0, 0, time.UTC)
	s := newStore(t, path,
		file.WithClock(func() time.Time { return at }),
		file.WithIDs(func() string { return "note-1" }),
	)
	if _, err := s.Add(context.Background(), "alice", "buy milk"); err != nil {
		t.Fatalf("Add error = %v", err)
	}

	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("reading %s: %v", path, err)
	}

	var doc struct {
		Notes []struct {
			ID        string    `json:"id"`
			Owner     string    `json:"owner"`
			Body      string    `json:"body"`
			CreatedAt time.Time `json:"created_at"`
		} `json:"notes"`
	}
	if err := json.Unmarshal(data, &doc); err != nil {
		t.Fatalf("the file is not valid JSON: %v\n%s", err, data)
	}
	if len(doc.Notes) != 1 {
		t.Fatalf("file holds %d notes, want 1", len(doc.Notes))
	}
	n := doc.Notes[0]
	if n.ID != "note-1" || n.Owner != "alice" || n.Body != "buy milk" || !n.CreatedAt.Equal(at) {
		t.Errorf("stored note = %+v, want the note that was added", n)
	}
}

// The file may hold other users' notes, so it should not be world readable.
func TestFilePermissionsAreRestrictive(t *testing.T) {
	t.Parallel()

	path := filepath.Join(t.TempDir(), "notes.json")
	if _, err := newStore(t, path).Add(context.Background(), "alice", "hi"); err != nil {
		t.Fatalf("Add error = %v", err)
	}

	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("Stat error = %v", err)
	}
	if perm := info.Mode().Perm(); perm != 0o600 {
		t.Errorf("file mode = %04o, want 0600", perm)
	}
}

// The write is a temp file plus a rename, so no partial state should ever be
// visible under the target name, and no temporary files should be left behind.
func TestWritesLeaveNoTemporaryFiles(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	path := filepath.Join(dir, "notes.json")
	s := newStore(t, path)
	ctx := context.Background()

	for i := range 5 {
		if _, err := s.Add(ctx, "alice", fmt.Sprintf("note %d", i)); err != nil {
			t.Fatalf("Add error = %v", err)
		}
	}

	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("ReadDir error = %v", err)
	}
	if len(entries) != 1 || entries[0].Name() != "notes.json" {
		names := make([]string, len(entries))
		for i, e := range entries {
			names[i] = e.Name()
		}
		t.Errorf("directory holds %v, want only notes.json", names)
	}
}

// Every write rewrites the whole file, so concurrent Adds must not lose any.
func TestConcurrentAddsAreNotLost(t *testing.T) {
	t.Parallel()

	path := filepath.Join(t.TempDir(), "notes.json")
	s := newStore(t, path)
	ctx := context.Background()

	const n = 30
	var wg sync.WaitGroup
	for i := range n {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if _, err := s.Add(ctx, "alice", fmt.Sprintf("note %d", i)); err != nil {
				t.Errorf("Add error = %v", err)
			}
		}()
	}
	wg.Wait()

	got, err := s.List(ctx, "alice")
	if err != nil {
		t.Fatalf("List error = %v", err)
	}
	if len(got) != n {
		t.Errorf("stored %d notes, want %d; a concurrent write was lost", len(got), n)
	}
}

func TestNewRejectsAnEmptyPath(t *testing.T) {
	t.Parallel()

	if _, err := file.New(""); err == nil {
		t.Error("file.New(\"\") succeeded, want an error")
	}
}
