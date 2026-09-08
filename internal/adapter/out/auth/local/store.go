package local

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"sync"
	"time"
)

// Errors the store returns for conditions a caller is expected to handle.
var (
	// ErrUserNotFound means no user has the requested subject.
	ErrUserNotFound = errors.New("local: user not found")

	// ErrUserExists means a user with that subject is already present.
	ErrUserExists = errors.New("local: user already exists")

	// ErrTokenNotFound means no token has the requested id.
	ErrTokenNotFound = errors.New("local: token not found")
)

// User is a provisioned identity.
//
// There is no password field and there will not be one. This server is a
// resource server, not an identity provider: credentials are issued by an
// operator, and the day a browser login is genuinely needed, that is an
// identity provider's job and a different Authenticator adapter.
type User struct {
	ID      string  `json:"id"`
	Subject string  `json:"subject"`
	Enabled bool    `json:"enabled"`
	Tokens  []Token `json:"tokens"`
}

// Token is one issued credential.
//
// Scopes live here rather than on the user. That is a departure from the
// original sketch, made because it is both more useful and more honest: it
// makes "issue alice a read-only token" possible without touching the access
// she already has, and it matches how every OAuth provider works, so the day
// the OIDC adapter arrives and reads scopes from token claims, the shape of a
// Principal does not change.
type Token struct {
	ID        string     `json:"id"`
	Hash      string     `json:"hash"`
	Scopes    []string   `json:"scopes"`
	CreatedAt time.Time  `json:"created_at"`
	ExpiresAt *time.Time `json:"expires_at,omitempty"`
}

// IsExpired reports whether the token has expired as of now. A token with no
// expiry never expires.
func (t Token) IsExpired(now time.Time) bool {
	return t.ExpiresAt != nil && !now.Before(*t.ExpiresAt)
}

// document is the on-disk shape of the users file.
type document struct {
	Users []User `json:"users"`
}

// Store reads and writes the users file.
//
// Every operation reads the file. That is deliberate: revoking a token takes
// effect on the very next request, with no cache to invalidate and no window
// during which a revoked credential still works. A users file is a few
// kilobytes, so the cost is a rounding error — and if it ever stops being one,
// the answer is the database adapter, not a cache here.
type Store struct {
	mu   sync.Mutex
	path string

	now   func() time.Time
	newID func(prefix string) string
}

// StoreOption customises a Store. Options exist for tests; production wiring
// calls NewStore with none.
type StoreOption func(*Store)

// WithClock replaces the store's clock.
func WithClock(now func() time.Time) StoreOption {
	return func(s *Store) { s.now = now }
}

// WithIDs replaces the store's identifier generator. It is called with the
// prefix the identifier should carry.
func WithIDs(newID func(prefix string) string) StoreOption {
	return func(s *Store) { s.newID = newID }
}

// NewStore returns a Store backed by the file at path.
//
// The file is not read or created here. A store whose file does not exist is a
// valid store with no users, and it stays that way until a token is issued, so
// starting the server never creates credentials nobody asked for.
func NewStore(path string, opts ...StoreOption) (*Store, error) {
	if path == "" {
		return nil, errors.New("local: empty users file path")
	}
	s := &Store{path: path, now: time.Now, newID: randomID}
	for _, opt := range opts {
		opt(s)
	}
	return s, nil
}

// Path returns the file the store reads and writes.
func (s *Store) Path() string { return s.path }

// Match is a token hash resolved to the user and token that carry it.
type Match struct {
	User  User
	Token Token
}

// FindByTokenHash returns the user and token carrying hash.
//
// It reports found=false for an unknown hash, and says nothing about why: a
// caller must not be able to tell an unknown token from a revoked one.
func (s *Store) FindByTokenHash(hash string) (Match, bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	doc, err := s.load()
	if err != nil {
		return Match{}, false, err
	}

	for _, u := range doc.Users {
		for _, tk := range u.Tokens {
			if HashesEqual(tk.Hash, hash) {
				return Match{User: u, Token: tk}, true, nil
			}
		}
	}
	return Match{}, false, nil
}

// Users returns every provisioned user, in file order.
//
// Token hashes are cleared from the result. Nothing outside this store needs
// them, and a hash that never leaves cannot be logged or printed by accident.
func (s *Store) Users() ([]User, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	doc, err := s.load()
	if err != nil {
		return nil, err
	}

	users := make([]User, 0, len(doc.Users))
	for _, u := range doc.Users {
		u.Tokens = slices.Clone(u.Tokens)
		for i := range u.Tokens {
			u.Tokens[i].Hash = ""
		}
		users = append(users, u)
	}
	return users, nil
}

// IssueOptions controls IssueToken.
type IssueOptions struct {
	// Scopes the issued token carries. An empty slice issues a token that can
	// see no tools at all, which is valid and occasionally what you want.
	Scopes []string

	// ExpiresAt is when the token stops working. Nil means it never expires.
	ExpiresAt *time.Time

	// CreateUser allows provisioning a user that does not exist yet. Without
	// it an unknown subject is an error, so that a typo produces a failure
	// rather than a second user with a working credential.
	CreateUser bool
}

// IssuedToken is the result of issuing a credential.
type IssuedToken struct {
	// Token is the plaintext credential. It exists only in this value: the
	// store keeps a hash, so this is the only chance anyone has to record it.
	Token string

	TokenID     string
	UserID      string
	Subject     string
	Scopes      []string
	ExpiresAt   *time.Time
	UserCreated bool
}

// IssueToken mints a token for subject and records its hash.
func (s *Store) IssueToken(subject string, opts IssueOptions) (IssuedToken, error) {
	if subject == "" {
		return IssuedToken{}, errors.New("local: empty subject")
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	doc, err := s.load()
	if err != nil {
		return IssuedToken{}, err
	}

	idx := slices.IndexFunc(doc.Users, func(u User) bool { return u.Subject == subject })
	created := false
	if idx < 0 {
		if !opts.CreateUser {
			return IssuedToken{}, fmt.Errorf("%w: %q", ErrUserNotFound, subject)
		}
		doc.Users = append(doc.Users, User{
			ID:      s.newID("usr"),
			Subject: subject,
			Enabled: true,
		})
		idx = len(doc.Users) - 1
		created = true
	}

	plaintext, err := NewToken()
	if err != nil {
		return IssuedToken{}, err
	}

	token := Token{
		ID:        s.newID("tok"),
		Hash:      HashToken(plaintext),
		Scopes:    slices.Clone(opts.Scopes),
		CreatedAt: s.now().UTC(),
		ExpiresAt: opts.ExpiresAt,
	}
	doc.Users[idx].Tokens = append(doc.Users[idx].Tokens, token)

	if err := s.save(doc); err != nil {
		return IssuedToken{}, err
	}

	return IssuedToken{
		Token:       plaintext,
		TokenID:     token.ID,
		UserID:      doc.Users[idx].ID,
		Subject:     subject,
		Scopes:      token.Scopes,
		ExpiresAt:   token.ExpiresAt,
		UserCreated: created,
	}, nil
}

// CreateUser provisions a subject with no tokens.
func (s *Store) CreateUser(subject string) (User, error) {
	if subject == "" {
		return User{}, errors.New("local: empty subject")
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	doc, err := s.load()
	if err != nil {
		return User{}, err
	}
	if slices.ContainsFunc(doc.Users, func(u User) bool { return u.Subject == subject }) {
		return User{}, fmt.Errorf("%w: %q", ErrUserExists, subject)
	}

	user := User{ID: s.newID("usr"), Subject: subject, Enabled: true}
	doc.Users = append(doc.Users, user)

	if err := s.save(doc); err != nil {
		return User{}, err
	}
	return user, nil
}

// RevokeToken removes the token with the given id.
//
// The record is deleted rather than flagged. A revoked token is not history
// worth keeping in a credentials file, and a hash that is gone cannot be
// matched by a bug in some future code path that forgets to check a flag.
func (s *Store) RevokeToken(tokenID string) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	doc, err := s.load()
	if err != nil {
		return err
	}

	for i := range doc.Users {
		before := len(doc.Users[i].Tokens)
		doc.Users[i].Tokens = slices.DeleteFunc(doc.Users[i].Tokens, func(t Token) bool {
			return t.ID == tokenID
		})
		if len(doc.Users[i].Tokens) != before {
			return s.save(doc)
		}
	}
	return fmt.Errorf("%w: %q", ErrTokenNotFound, tokenID)
}

// load reads the file. A missing or empty file is an empty store; a malformed
// one is an error, because starting over on top of it would silently revoke
// every credential in it.
func (s *Store) load() (document, error) {
	data, err := os.ReadFile(s.path)
	if errors.Is(err, fs.ErrNotExist) {
		return document{}, nil
	}
	if err != nil {
		return document{}, fmt.Errorf("local: reading %s: %w", s.path, err)
	}
	if len(data) == 0 {
		return document{}, nil
	}

	var doc document
	if err := json.Unmarshal(data, &doc); err != nil {
		return document{}, fmt.Errorf("local: %s is not valid JSON: %w", s.path, err)
	}
	return doc, nil
}

// save writes the file atomically, 0600. A crash mid-write leaves the previous
// file intact rather than a truncated one — which for this file would mean
// locking every user out.
func (s *Store) save(doc document) error {
	if doc.Users == nil {
		doc.Users = []User{}
	}
	data, err := json.MarshalIndent(doc, "", "  ")
	if err != nil {
		return fmt.Errorf("local: encoding: %w", err)
	}
	data = append(data, '\n')

	dir := filepath.Dir(s.path)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return fmt.Errorf("local: creating %s: %w", dir, err)
	}

	tmp, err := os.CreateTemp(dir, filepath.Base(s.path)+".tmp-*")
	if err != nil {
		return fmt.Errorf("local: creating a temporary file in %s: %w", dir, err)
	}
	tmpName := tmp.Name()
	defer os.Remove(tmpName)

	// Permissions are narrowed before any content is written, so the file is
	// never briefly readable by others while it holds credential hashes.
	if err := tmp.Chmod(0o600); err != nil {
		tmp.Close()
		return fmt.Errorf("local: setting permissions on %s: %w", tmpName, err)
	}
	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		return fmt.Errorf("local: writing %s: %w", tmpName, err)
	}
	if err := tmp.Sync(); err != nil {
		tmp.Close()
		return fmt.Errorf("local: syncing %s: %w", tmpName, err)
	}
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("local: closing %s: %w", tmpName, err)
	}
	if err := os.Rename(tmpName, s.path); err != nil {
		return fmt.Errorf("local: replacing %s: %w", s.path, err)
	}
	return nil
}

func randomID(prefix string) string {
	var b [8]byte
	if _, err := rand.Read(b[:]); err != nil {
		panic("local: cannot read random bytes: " + err.Error())
	}
	return prefix + "_" + hex.EncodeToString(b[:])
}
