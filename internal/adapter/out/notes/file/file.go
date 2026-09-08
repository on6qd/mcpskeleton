// Package file is a JSON-file NoteStore.
//
// It is the durable half of the reference adapter pair. Together with the
// in-memory store it demonstrates the property this codebase is built around:
// the tool above the port does not change when the storage below it does.
//
// Scope of the guarantee: this adapter is safe for concurrent use within one
// process. It does not coordinate with other processes writing the same file,
// because doing so properly means file locking, and a skeleton that pretended
// to offer cross-process safety would be worse than one that says it does not.
// The database adapter is the answer to that, not a lock file.
package file

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/bartdelepeleer/mcpskeleton/internal/domain"
	"github.com/bartdelepeleer/mcpskeleton/internal/port"
)

// Store keeps notes in a JSON file.
type Store struct {
	// mu serialises read-modify-write cycles. Every operation reads the file, so
	// there is no in-memory cache to keep coherent with what is on disk.
	mu   sync.Mutex
	path string

	now   func() time.Time
	newID func() string
}

// document is the on-disk shape. Notes are a flat list rather than a map keyed
// by owner so that the file stays readable, and diffable, by a human.
type document struct {
	Notes []storedNote `json:"notes"`
}

type storedNote struct {
	ID        string    `json:"id"`
	Owner     string    `json:"owner"`
	Body      string    `json:"body"`
	CreatedAt time.Time `json:"created_at"`
}

// Option customises a Store. Options exist for tests; production wiring calls
// New with none.
type Option func(*Store)

// WithClock replaces the store's clock.
func WithClock(now func() time.Time) Option {
	return func(s *Store) { s.now = now }
}

// WithIDs replaces the store's identifier generator.
func WithIDs(newID func() string) Option {
	return func(s *Store) { s.newID = newID }
}

// New returns a Store backed by the file at path.
//
// The file is not read or created here: a store whose file does not exist yet
// is a valid empty store, and the file appears on the first Add. That way
// starting the server does not create state the operator did not ask for.
func New(path string, opts ...Option) (*Store, error) {
	if path == "" {
		return nil, errors.New("notes: empty file path")
	}
	s := &Store{path: path, now: time.Now, newID: randomID}
	for _, opt := range opts {
		opt(s)
	}
	return s, nil
}

// Add stores a note owned by owner and returns it.
func (s *Store) Add(_ context.Context, owner, body string) (domain.Note, error) {
	if owner == "" {
		return domain.Note{}, errors.New("notes: refusing to store a note with no owner")
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	doc, err := s.load()
	if err != nil {
		return domain.Note{}, err
	}

	note := domain.Note{
		ID:        s.newID(),
		Owner:     owner,
		Body:      body,
		CreatedAt: s.now().UTC(),
	}
	doc.Notes = append(doc.Notes, storedNote(note))

	if err := s.save(doc); err != nil {
		return domain.Note{}, err
	}
	return note, nil
}

// List returns owner's notes, oldest first.
func (s *Store) List(_ context.Context, owner string) ([]domain.Note, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	doc, err := s.load()
	if err != nil {
		return nil, err
	}

	// File order is insertion order, which is the ordering the port promises.
	notes := make([]domain.Note, 0, len(doc.Notes))
	for _, n := range doc.Notes {
		if n.Owner == owner {
			notes = append(notes, domain.Note(n))
		}
	}
	return notes, nil
}

// load reads the file. A missing file is an empty store; a malformed one is an
// error, because silently starting over would destroy notes an operator may be
// able to recover by hand.
func (s *Store) load() (document, error) {
	data, err := os.ReadFile(s.path)
	if errors.Is(err, fs.ErrNotExist) {
		return document{}, nil
	}
	if err != nil {
		return document{}, fmt.Errorf("notes: reading %s: %w", s.path, err)
	}
	if len(data) == 0 {
		return document{}, nil
	}

	var doc document
	if err := json.Unmarshal(data, &doc); err != nil {
		return document{}, fmt.Errorf("notes: %s is not valid JSON: %w", s.path, err)
	}
	return doc, nil
}

// save writes the file atomically: a temporary file in the same directory,
// fsynced, then renamed over the target. A crash mid-write leaves the previous
// file intact rather than a truncated one.
func (s *Store) save(doc document) error {
	data, err := json.MarshalIndent(doc, "", "  ")
	if err != nil {
		return fmt.Errorf("notes: encoding: %w", err)
	}
	data = append(data, '\n')

	dir := filepath.Dir(s.path)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return fmt.Errorf("notes: creating %s: %w", dir, err)
	}

	// The temporary file must share a directory with the target, because rename
	// is only atomic within a filesystem.
	tmp, err := os.CreateTemp(dir, filepath.Base(s.path)+".tmp-*")
	if err != nil {
		return fmt.Errorf("notes: creating a temporary file in %s: %w", dir, err)
	}
	tmpName := tmp.Name()
	defer os.Remove(tmpName) // no-op once the rename has succeeded

	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		return fmt.Errorf("notes: writing %s: %w", tmpName, err)
	}
	if err := tmp.Sync(); err != nil {
		tmp.Close()
		return fmt.Errorf("notes: syncing %s: %w", tmpName, err)
	}
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("notes: closing %s: %w", tmpName, err)
	}
	if err := os.Chmod(tmpName, 0o600); err != nil {
		return fmt.Errorf("notes: setting permissions on %s: %w", tmpName, err)
	}
	if err := os.Rename(tmpName, s.path); err != nil {
		return fmt.Errorf("notes: replacing %s: %w", s.path, err)
	}
	return nil
}

func randomID() string {
	var b [12]byte
	if _, err := rand.Read(b[:]); err != nil {
		panic("notes: cannot read random bytes: " + err.Error())
	}
	return hex.EncodeToString(b[:])
}

var _ port.NoteStore = (*Store)(nil)
