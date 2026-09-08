// Package memory is an in-memory NoteStore.
//
// It is one of two adapters behind port.NoteStore. The pair exists to
// demonstrate, with running code rather than a comment, the swap the
// authenticator will make when it moves from a local file to a database: same
// port, same tool, different storage, no change above the seam.
//
// Notes live for as long as the process does. That is a deliberate property of
// this adapter, not a limitation to work around — the file adapter is there for
// when durability matters.
package memory

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"slices"
	"sync"
	"time"

	"github.com/bartdelepeleer/mcpskeleton/internal/domain"
	"github.com/bartdelepeleer/mcpskeleton/internal/port"
)

// Store keeps notes in a map, keyed by owner.
//
// Unlike the tool registry, this is mutated at runtime by concurrent requests,
// so it carries a lock.
type Store struct {
	mu      sync.RWMutex
	byOwner map[string][]domain.Note

	// now and newID are injectable so tests can make time and identifiers
	// predictable without reaching into the store's internals.
	now   func() time.Time
	newID func() string
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

// New returns an empty Store.
func New(opts ...Option) *Store {
	s := &Store{
		byOwner: make(map[string][]domain.Note),
		now:     time.Now,
		newID:   randomID,
	}
	for _, opt := range opts {
		opt(s)
	}
	return s
}

// Add stores a note owned by owner.
//
// An empty owner is refused. Ownership comes from an authenticated principal,
// so an empty one means the caller has lost track of who it is acting for, and
// silently filing the note under "" would make it visible to nobody or to
// everybody depending on how it is later read.
func (s *Store) Add(_ context.Context, owner, body string) (domain.Note, error) {
	if owner == "" {
		return domain.Note{}, fmt.Errorf("notes: refusing to store a note with no owner")
	}

	note := domain.Note{
		ID:        s.newID(),
		Owner:     owner,
		Body:      body,
		CreatedAt: s.now().UTC(),
	}

	s.mu.Lock()
	defer s.mu.Unlock()
	s.byOwner[owner] = append(s.byOwner[owner], note)

	return note, nil
}

// List returns owner's notes, oldest first.
//
// Insertion order is the ordering: two notes added in the same clock tick keep
// the order they arrived in, which sorting by CreatedAt would not guarantee.
// An owner with no notes yields an empty slice, never an error.
func (s *Store) List(_ context.Context, owner string) ([]domain.Note, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	// Cloned, so a caller cannot mutate the store's own slice — and so a
	// concurrent Add appending to it cannot race with the caller reading it.
	return slices.Clone(s.byOwner[owner]), nil
}

func randomID() string {
	var b [12]byte
	if _, err := rand.Read(b[:]); err != nil {
		// crypto/rand does not fail on any platform this runs on; if it does,
		// the process has bigger problems than an unstored note.
		panic("notes: cannot read random bytes: " + err.Error())
	}
	return hex.EncodeToString(b[:])
}

var _ port.NoteStore = (*Store)(nil)
