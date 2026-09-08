package local_test

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/bartdelepeleer/mcpskeleton/internal/adapter/out/auth/local"
)

func newStore(t *testing.T, opts ...local.StoreOption) (*local.Store, string) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "users.json")
	s, err := local.NewStore(path, opts...)
	if err != nil {
		t.Fatalf("NewStore error = %v", err)
	}
	return s, path
}

func mustIssue(t *testing.T, s *local.Store, subject string, opts local.IssueOptions) local.IssuedToken {
	t.Helper()
	issued, err := s.IssueToken(subject, opts)
	if err != nil {
		t.Fatalf("IssueToken(%q) error = %v", subject, err)
	}
	return issued
}

func TestIssueTokenCreatesAUser(t *testing.T) {
	t.Parallel()

	s, _ := newStore(t)

	issued := mustIssue(t, s, "alice", local.IssueOptions{
		Scopes:     []string{"notes:read"},
		CreateUser: true,
	})

	if !issued.UserCreated {
		t.Error("UserCreated = false, want true for a new subject")
	}
	if !local.ValidTokenFormat(issued.Token) {
		t.Errorf("issued token %q is not well formed", local.Redact(issued.Token))
	}
	if issued.Subject != "alice" {
		t.Errorf("Subject = %q, want %q", issued.Subject, "alice")
	}
	if !strings.HasPrefix(issued.UserID, "usr_") || !strings.HasPrefix(issued.TokenID, "tok_") {
		t.Errorf("ids = %q / %q, want usr_ and tok_ prefixes", issued.UserID, issued.TokenID)
	}
}

// The guard against a typo silently provisioning a second working account.
func TestIssueTokenRefusesAnUnknownUserWithoutCreate(t *testing.T) {
	t.Parallel()

	s, path := newStore(t)

	_, err := s.IssueToken("alcie", local.IssueOptions{Scopes: []string{"notes:read"}})
	if !errors.Is(err, local.ErrUserNotFound) {
		t.Fatalf("error = %v, want one wrapping ErrUserNotFound", err)
	}
	if _, statErr := os.Stat(path); !os.IsNotExist(statErr) {
		t.Error("a refused issue still wrote the users file")
	}
}

func TestIssueTokenForAnExistingUserDoesNotCreateASecond(t *testing.T) {
	t.Parallel()

	s, _ := newStore(t)
	mustIssue(t, s, "alice", local.IssueOptions{CreateUser: true, Scopes: []string{"notes:read"}})

	second := mustIssue(t, s, "alice", local.IssueOptions{Scopes: []string{"notes:write"}})
	if second.UserCreated {
		t.Error("UserCreated = true for an existing subject")
	}

	users, err := s.Users()
	if err != nil {
		t.Fatalf("Users error = %v", err)
	}
	if len(users) != 1 {
		t.Fatalf("store holds %d users, want 1", len(users))
	}
	if len(users[0].Tokens) != 2 {
		t.Errorf("alice has %d tokens, want 2", len(users[0].Tokens))
	}
}

// Scopes belong to the token, not the user: issuing a narrow token must not
// touch the access an existing token already grants.
func TestScopesArePerTokenNotPerUser(t *testing.T) {
	t.Parallel()

	s, _ := newStore(t)

	full := mustIssue(t, s, "alice", local.IssueOptions{
		CreateUser: true,
		Scopes:     []string{"notes:read", "notes:write", "tools:echo"},
	})
	readOnly := mustIssue(t, s, "alice", local.IssueOptions{Scopes: []string{"notes:read"}})

	fullMatch, ok, err := s.FindByTokenHash(local.HashToken(full.Token))
	if err != nil || !ok {
		t.Fatalf("FindByTokenHash(full) = ok %v, err %v", ok, err)
	}
	if len(fullMatch.Token.Scopes) != 3 {
		t.Errorf("the original token now carries %v; issuing a narrow token changed it", fullMatch.Token.Scopes)
	}

	narrowMatch, ok, err := s.FindByTokenHash(local.HashToken(readOnly.Token))
	if err != nil || !ok {
		t.Fatalf("FindByTokenHash(readOnly) = ok %v, err %v", ok, err)
	}
	if len(narrowMatch.Token.Scopes) != 1 || narrowMatch.Token.Scopes[0] != "notes:read" {
		t.Errorf("narrow token carries %v, want [notes:read]", narrowMatch.Token.Scopes)
	}
}

func TestFindByTokenHash(t *testing.T) {
	t.Parallel()

	s, _ := newStore(t)
	issued := mustIssue(t, s, "alice", local.IssueOptions{CreateUser: true, Scopes: []string{"notes:read"}})

	match, ok, err := s.FindByTokenHash(local.HashToken(issued.Token))
	if err != nil {
		t.Fatalf("FindByTokenHash error = %v", err)
	}
	if !ok {
		t.Fatal("a freshly issued token was not found")
	}
	if match.User.Subject != "alice" {
		t.Errorf("Subject = %q, want %q", match.User.Subject, "alice")
	}
	if !match.User.Enabled {
		t.Error("a newly created user is not enabled")
	}
	if match.Token.ID != issued.TokenID {
		t.Errorf("Token.ID = %q, want %q", match.Token.ID, issued.TokenID)
	}
}

func TestFindByTokenHashUnknown(t *testing.T) {
	t.Parallel()

	s, _ := newStore(t)
	mustIssue(t, s, "alice", local.IssueOptions{CreateUser: true})

	_, ok, err := s.FindByTokenHash(local.HashToken("mcps_someoneelses"))
	if err != nil {
		t.Fatalf("FindByTokenHash error = %v", err)
	}
	if ok {
		t.Error("an unknown token hash matched")
	}
}

func TestRevokeToken(t *testing.T) {
	t.Parallel()

	s, _ := newStore(t)
	keep := mustIssue(t, s, "alice", local.IssueOptions{CreateUser: true})
	drop := mustIssue(t, s, "alice", local.IssueOptions{})

	if err := s.RevokeToken(drop.TokenID); err != nil {
		t.Fatalf("RevokeToken error = %v", err)
	}

	if _, ok, err := s.FindByTokenHash(local.HashToken(drop.Token)); err != nil || ok {
		t.Errorf("the revoked token still matches (ok=%v, err=%v)", ok, err)
	}
	if _, ok, err := s.FindByTokenHash(local.HashToken(keep.Token)); err != nil || !ok {
		t.Errorf("revoking one token invalidated another (ok=%v, err=%v)", ok, err)
	}
}

// A revoked token's hash is deleted rather than flagged, so no future code path
// can match it by forgetting to check a flag.
func TestRevokeTokenRemovesTheHashFromTheFile(t *testing.T) {
	t.Parallel()

	s, path := newStore(t)
	issued := mustIssue(t, s, "alice", local.IssueOptions{CreateUser: true})
	hash := local.HashToken(issued.Token)

	if err := s.RevokeToken(issued.TokenID); err != nil {
		t.Fatalf("RevokeToken error = %v", err)
	}

	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("reading %s: %v", path, err)
	}
	if strings.Contains(string(data), hash) {
		t.Error("the revoked token's hash is still in the file")
	}
	if !strings.Contains(string(data), "alice") {
		t.Error("revoking a token removed the user")
	}
}

func TestRevokeUnknownToken(t *testing.T) {
	t.Parallel()

	s, _ := newStore(t)
	mustIssue(t, s, "alice", local.IssueOptions{CreateUser: true})

	if err := s.RevokeToken("tok_nonexistent"); !errors.Is(err, local.ErrTokenNotFound) {
		t.Errorf("error = %v, want one wrapping ErrTokenNotFound", err)
	}
}

func TestCreateUser(t *testing.T) {
	t.Parallel()

	s, _ := newStore(t)

	user, err := s.CreateUser("alice")
	if err != nil {
		t.Fatalf("CreateUser error = %v", err)
	}
	if user.Subject != "alice" || !user.Enabled || len(user.Tokens) != 0 {
		t.Errorf("CreateUser = %+v, want an enabled alice with no tokens", user)
	}

	if _, err := s.CreateUser("alice"); !errors.Is(err, local.ErrUserExists) {
		t.Errorf("creating alice twice error = %v, want one wrapping ErrUserExists", err)
	}
}

// A user provisioned with no tokens simply cannot authenticate yet, which is
// the documented way to prepare an account before handing out a credential.
func TestAUserWithNoTokensCannotAuthenticate(t *testing.T) {
	t.Parallel()

	s, _ := newStore(t)
	if _, err := s.CreateUser("alice"); err != nil {
		t.Fatalf("CreateUser error = %v", err)
	}

	if _, ok, err := s.FindByTokenHash(local.HashToken("mcps_anything")); err != nil || ok {
		t.Errorf("a tokenless user matched a token (ok=%v, err=%v)", ok, err)
	}
}

// Nothing outside the store needs a token hash, and one that never leaves
// cannot be logged or printed by accident.
func TestUsersDoesNotExposeTokenHashes(t *testing.T) {
	t.Parallel()

	s, _ := newStore(t)
	mustIssue(t, s, "alice", local.IssueOptions{CreateUser: true, Scopes: []string{"notes:read"}})

	users, err := s.Users()
	if err != nil {
		t.Fatalf("Users error = %v", err)
	}
	if len(users) != 1 || len(users[0].Tokens) != 1 {
		t.Fatalf("Users = %+v, want one user with one token", users)
	}
	if users[0].Tokens[0].Hash != "" {
		t.Errorf("Users exposed a token hash: %q", users[0].Tokens[0].Hash)
	}
	// Everything an operator actually needs is still there.
	if users[0].Tokens[0].ID == "" {
		t.Error("Users dropped the token id, which is what revoke needs")
	}
	if len(users[0].Tokens[0].Scopes) != 1 {
		t.Error("Users dropped the token's scopes")
	}
}

func TestTokenIsExpired(t *testing.T) {
	t.Parallel()

	now := time.Date(2026, 9, 8, 12, 0, 0, 0, time.UTC)
	past, future := now.Add(-time.Hour), now.Add(time.Hour)

	tests := []struct {
		name    string
		expires *time.Time
		want    bool
	}{
		{"no expiry never expires", nil, false},
		{"future expiry is valid", &future, false},
		{"past expiry is expired", &past, true},
		{"expiry exactly now is expired", &now, true},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			if got := (local.Token{ExpiresAt: tc.expires}).IsExpired(now); got != tc.want {
				t.Errorf("IsExpired() = %v, want %v", got, tc.want)
			}
		})
	}
}

func TestIssuedTokenRecordsExpiry(t *testing.T) {
	t.Parallel()

	s, _ := newStore(t)
	expires := time.Date(2026, 12, 25, 0, 0, 0, 0, time.UTC)

	issued := mustIssue(t, s, "alice", local.IssueOptions{CreateUser: true, ExpiresAt: &expires})
	if issued.ExpiresAt == nil || !issued.ExpiresAt.Equal(expires) {
		t.Fatalf("ExpiresAt = %v, want %v", issued.ExpiresAt, expires)
	}

	match, ok, err := s.FindByTokenHash(local.HashToken(issued.Token))
	if err != nil || !ok {
		t.Fatalf("FindByTokenHash = ok %v, err %v", ok, err)
	}
	if match.Token.ExpiresAt == nil || !match.Token.ExpiresAt.Equal(expires) {
		t.Errorf("stored ExpiresAt = %v, want %v", match.Token.ExpiresAt, expires)
	}
}

func TestFileIsReadableAndHoldsNoPlaintext(t *testing.T) {
	t.Parallel()

	at := time.Date(2026, 9, 8, 10, 0, 0, 0, time.UTC)
	counter := 0
	s, path := newStore(t,
		local.WithClock(func() time.Time { return at }),
		local.WithIDs(func(prefix string) string {
			counter++
			return fmt.Sprintf("%s_%d", prefix, counter)
		}),
	)

	issued := mustIssue(t, s, "alice", local.IssueOptions{CreateUser: true, Scopes: []string{"notes:read"}})

	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("reading %s: %v", path, err)
	}

	// The credential itself must never be written down.
	if strings.Contains(string(data), issued.Token) {
		t.Fatal("the users file contains the plaintext token")
	}
	if !strings.Contains(string(data), local.HashToken(issued.Token)) {
		t.Error("the users file does not contain the token's hash")
	}

	var doc struct {
		Users []struct {
			ID      string `json:"id"`
			Subject string `json:"subject"`
			Enabled bool   `json:"enabled"`
			Tokens  []struct {
				ID     string   `json:"id"`
				Hash   string   `json:"hash"`
				Scopes []string `json:"scopes"`
			} `json:"tokens"`
		} `json:"users"`
	}
	if err := json.Unmarshal(data, &doc); err != nil {
		t.Fatalf("the users file is not valid JSON: %v\n%s", err, data)
	}
	if len(doc.Users) != 1 || doc.Users[0].Subject != "alice" || !doc.Users[0].Enabled {
		t.Fatalf("file holds %+v, want one enabled alice", doc.Users)
	}
	if doc.Users[0].ID != "usr_1" || doc.Users[0].Tokens[0].ID != "tok_2" {
		t.Errorf("ids = %q / %q, want the injected ones", doc.Users[0].ID, doc.Users[0].Tokens[0].ID)
	}
	// There is no password field, and there is not meant to be one.
	if strings.Contains(string(data), "password") {
		t.Error("the users file has a password field")
	}
}

func TestFilePermissionsAreRestrictive(t *testing.T) {
	t.Parallel()

	s, path := newStore(t)
	mustIssue(t, s, "alice", local.IssueOptions{CreateUser: true})

	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("Stat error = %v", err)
	}
	if perm := info.Mode().Perm(); perm != 0o600 {
		t.Errorf("users file mode = %04o, want 0600", perm)
	}
}

func TestMissingFileIsAnEmptyStore(t *testing.T) {
	t.Parallel()

	s, _ := newStore(t)

	users, err := s.Users()
	if err != nil {
		t.Fatalf("Users on a missing file error = %v, want nil", err)
	}
	if len(users) != 0 {
		t.Errorf("Users = %v, want empty", users)
	}
	if _, ok, err := s.FindByTokenHash("sha256:whatever"); err != nil || ok {
		t.Errorf("FindByTokenHash on a missing file = ok %v, err %v", ok, err)
	}
}

// Starting over on a users file that cannot be parsed would silently revoke
// every credential in it.
func TestCorruptFileIsAnErrorNotAFreshStart(t *testing.T) {
	t.Parallel()

	path := filepath.Join(t.TempDir(), "users.json")
	if err := os.WriteFile(path, []byte(`{"users": [ broken`), 0o600); err != nil {
		t.Fatalf("seeding the file: %v", err)
	}
	s, err := local.NewStore(path)
	if err != nil {
		t.Fatalf("NewStore error = %v", err)
	}

	if _, err := s.Users(); err == nil {
		t.Error("Users on a corrupt file succeeded")
	}
	if _, _, err := s.FindByTokenHash("sha256:x"); err == nil {
		t.Error("FindByTokenHash on a corrupt file succeeded; every request would be silently unauthenticated")
	}
	if _, err := s.IssueToken("alice", local.IssueOptions{CreateUser: true}); err == nil {
		t.Error("IssueToken on a corrupt file succeeded; it would have overwritten recoverable credentials")
	}

	data, err := os.ReadFile(path)
	if err != nil || len(data) == 0 {
		t.Error("the corrupt file was truncated")
	}
}

func TestNewStoreRejectsAnEmptyPath(t *testing.T) {
	t.Parallel()

	if _, err := local.NewStore(""); err == nil {
		t.Error(`NewStore("") succeeded, want an error`)
	}
}

func TestWritesLeaveNoTemporaryFiles(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	path := filepath.Join(dir, "users.json")
	s, err := local.NewStore(path)
	if err != nil {
		t.Fatalf("NewStore error = %v", err)
	}
	for i := range 5 {
		if _, err := s.IssueToken(fmt.Sprintf("user-%d", i), local.IssueOptions{CreateUser: true}); err != nil {
			t.Fatalf("IssueToken error = %v", err)
		}
	}

	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("ReadDir error = %v", err)
	}
	if len(entries) != 1 || entries[0].Name() != "users.json" {
		names := make([]string, len(entries))
		for i, e := range entries {
			names[i] = e.Name()
		}
		t.Errorf("directory holds %v, want only users.json", names)
	}
}

// Every write rewrites the whole file, so concurrent issues must not lose one —
// a lost write here is a credential an operator believes they handed out.
func TestConcurrentIssuesAreNotLost(t *testing.T) {
	t.Parallel()

	s, _ := newStore(t)

	const n = 20
	var wg sync.WaitGroup
	for i := range n {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if _, err := s.IssueToken(fmt.Sprintf("user-%d", i), local.IssueOptions{CreateUser: true}); err != nil {
				t.Errorf("IssueToken error = %v", err)
			}
		}()
	}
	wg.Wait()

	users, err := s.Users()
	if err != nil {
		t.Fatalf("Users error = %v", err)
	}
	if len(users) != n {
		t.Errorf("store holds %d users, want %d; a concurrent write was lost", len(users), n)
	}
}
