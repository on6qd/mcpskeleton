package local_test

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/bartdelepeleer/mcpskeleton/internal/adapter/out/auth/local"
	"github.com/bartdelepeleer/mcpskeleton/internal/domain"
)

func TestAuthenticateResolvesAPrincipal(t *testing.T) {
	t.Parallel()

	s, _ := newStore(t)
	issued := mustIssue(t, s, "alice", local.IssueOptions{
		CreateUser: true,
		Scopes:     []string{"notes:read", "tools:echo"},
	})

	got, err := local.NewAuthenticator(s).Authenticate(context.Background(), issued.Token)
	if err != nil {
		t.Fatalf("Authenticate error = %v", err)
	}

	if got.Subject != "alice" {
		t.Errorf("Subject = %q, want %q", got.Subject, "alice")
	}
	if len(got.Scopes) != 2 {
		t.Fatalf("Scopes = %v, want two", got.Scopes)
	}
	for _, want := range []string{"notes:read", "tools:echo"} {
		if !got.HasScope(want) {
			t.Errorf("principal is missing scope %q", want)
		}
	}
	if !got.ExpiresAt.IsZero() {
		t.Errorf("ExpiresAt = %v, want zero for a token with no expiry", got.ExpiresAt)
	}
}

// The constraint the whole design rests on: what comes back is complete, so
// nothing downstream has to look anything up to finish it.
func TestAuthenticateReturnsACompletePrincipal(t *testing.T) {
	t.Parallel()

	expires := time.Date(2026, 12, 25, 0, 0, 0, 0, time.UTC)
	s, _ := newStore(t)
	issued := mustIssue(t, s, "alice", local.IssueOptions{
		CreateUser: true,
		Scopes:     []string{"notes:read"},
		ExpiresAt:  &expires,
	})

	got, err := local.NewAuthenticator(s,
		local.WithAuthClock(func() time.Time { return expires.Add(-time.Hour) }),
	).Authenticate(context.Background(), issued.Token)
	if err != nil {
		t.Fatalf("Authenticate error = %v", err)
	}

	if got.Subject == "" {
		t.Error("Subject is empty")
	}
	if len(got.Scopes) == 0 {
		t.Error("Scopes is empty")
	}
	if !got.ExpiresAt.Equal(expires) {
		t.Errorf("ExpiresAt = %v, want %v", got.ExpiresAt, expires)
	}
}

// Scopes come from the token, so two tokens for the same user can grant
// different access.
func TestAuthenticateUsesTheTokensScopesNotTheUsers(t *testing.T) {
	t.Parallel()

	s, _ := newStore(t)
	full := mustIssue(t, s, "alice", local.IssueOptions{
		CreateUser: true,
		Scopes:     []string{"notes:read", "notes:write"},
	})
	readOnly := mustIssue(t, s, "alice", local.IssueOptions{Scopes: []string{"notes:read"}})

	auth := local.NewAuthenticator(s)
	ctx := context.Background()

	fullPrincipal, err := auth.Authenticate(ctx, full.Token)
	if err != nil {
		t.Fatalf("Authenticate(full) error = %v", err)
	}
	readPrincipal, err := auth.Authenticate(ctx, readOnly.Token)
	if err != nil {
		t.Fatalf("Authenticate(readOnly) error = %v", err)
	}

	if fullPrincipal.Subject != readPrincipal.Subject {
		t.Fatal("the two tokens resolved to different subjects")
	}
	if !fullPrincipal.HasScope("notes:write") {
		t.Error("the full token lost notes:write")
	}
	if readPrincipal.HasScope("notes:write") {
		t.Error("the read-only token granted notes:write")
	}
}

// A mutation of the returned principal must not reach the store's state.
func TestAuthenticateReturnsIndependentScopes(t *testing.T) {
	t.Parallel()

	s, _ := newStore(t)
	issued := mustIssue(t, s, "alice", local.IssueOptions{CreateUser: true, Scopes: []string{"notes:read"}})
	auth := local.NewAuthenticator(s)

	first, err := auth.Authenticate(context.Background(), issued.Token)
	if err != nil {
		t.Fatalf("Authenticate error = %v", err)
	}
	first.Scopes[0] = "admin:everything"

	second, err := auth.Authenticate(context.Background(), issued.Token)
	if err != nil {
		t.Fatalf("Authenticate error = %v", err)
	}
	if second.HasScope("admin:everything") {
		t.Error("mutating a returned principal changed what the next one gets")
	}
}

func TestAuthenticateRejections(t *testing.T) {
	t.Parallel()

	past := time.Date(2020, 1, 1, 0, 0, 0, 0, time.UTC)

	tests := []struct {
		name string
		// setup returns the credential to present.
		setup  func(t *testing.T, s *local.Store) string
		reason string
	}{
		{
			name:   "no credential",
			setup:  func(*testing.T, *local.Store) string { return "" },
			reason: "no credential presented",
		},
		{
			name:   "not a token at all",
			setup:  func(*testing.T, *local.Store) string { return "hunter2" },
			reason: "malformed credential",
		},
		{
			name: "a whole header rather than a token",
			setup: func(t *testing.T, s *local.Store) string {
				return "Bearer " + mustIssue(t, s, "alice", local.IssueOptions{CreateUser: true}).Token
			},
			reason: "malformed credential",
		},
		{
			name: "well formed but unknown",
			setup: func(t *testing.T, s *local.Store) string {
				token, err := local.NewToken()
				if err != nil {
					t.Fatalf("NewToken error = %v", err)
				}
				return token
			},
			reason: "unknown token",
		},
		{
			name: "revoked",
			setup: func(t *testing.T, s *local.Store) string {
				issued := mustIssue(t, s, "alice", local.IssueOptions{CreateUser: true})
				if err := s.RevokeToken(issued.TokenID); err != nil {
					t.Fatalf("RevokeToken error = %v", err)
				}
				return issued.Token
			},
			reason: "unknown token",
		},
		{
			name: "expired",
			setup: func(t *testing.T, s *local.Store) string {
				return mustIssue(t, s, "alice", local.IssueOptions{
					CreateUser: true,
					ExpiresAt:  &past,
				}).Token
			},
			reason: "token expired",
		},
		{
			name: "user disabled",
			setup: func(t *testing.T, s *local.Store) string {
				issued := mustIssue(t, s, "alice", local.IssueOptions{CreateUser: true})
				disableUser(t, s.Path(), "alice")
				return issued.Token
			},
			reason: "user disabled",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			s, _ := newStore(t)
			credential := tc.setup(t, s)

			_, err := local.NewAuthenticator(s).Authenticate(context.Background(), credential)
			if err == nil {
				t.Fatal("Authenticate error = nil, want a rejection")
			}
			if !errors.Is(err, domain.ErrUnauthenticated) {
				t.Errorf("error = %v, want one matching ErrUnauthenticated", err)
			}
			if got := local.FailureReason(err); got != tc.reason {
				t.Errorf("FailureReason = %q, want %q", got, tc.reason)
			}
		})
	}
}

// A caller must not be able to tell an unknown token from a revoked, expired or
// disabled one. If the messages ever diverge, this is an enumeration oracle.
func TestEveryRejectionLooksIdentical(t *testing.T) {
	t.Parallel()

	past := time.Date(2020, 1, 1, 0, 0, 0, 0, time.UTC)

	credentials := func(t *testing.T, s *local.Store) []string {
		unknown, err := local.NewToken()
		if err != nil {
			t.Fatalf("NewToken error = %v", err)
		}
		revoked := mustIssue(t, s, "bob", local.IssueOptions{CreateUser: true})
		if err := s.RevokeToken(revoked.TokenID); err != nil {
			t.Fatalf("RevokeToken error = %v", err)
		}
		expired := mustIssue(t, s, "carol", local.IssueOptions{CreateUser: true, ExpiresAt: &past})
		disabled := mustIssue(t, s, "dave", local.IssueOptions{CreateUser: true})
		disableUser(t, s.Path(), "dave")

		return []string{"", "garbage", unknown, revoked.Token, expired.Token, disabled.Token}
	}

	s, _ := newStore(t)
	auth := local.NewAuthenticator(s)

	var messages []string
	for _, credential := range credentials(t, s) {
		_, err := auth.Authenticate(context.Background(), credential)
		if err == nil {
			t.Fatalf("Authenticate(%q) succeeded, want a rejection", local.Redact(credential))
		}
		messages = append(messages, err.Error())
	}

	for i, msg := range messages {
		if msg != messages[0] {
			t.Errorf("rejection %d says %q but rejection 0 says %q; the difference is an oracle", i, msg, messages[0])
		}
	}
}

// The reason is for logs only. If it were reachable through Error(), someone
// formatting the error into a response would leak it.
func TestFailureReasonIsNotInTheMessage(t *testing.T) {
	t.Parallel()

	s, _ := newStore(t)
	_, err := local.NewAuthenticator(s).Authenticate(context.Background(), "garbage")
	if err == nil {
		t.Fatal("Authenticate succeeded, want a rejection")
	}

	reason := local.FailureReason(err)
	if reason == "" {
		t.Fatal("FailureReason is empty; there is nothing to log")
	}
	if strings.Contains(err.Error(), reason) {
		t.Errorf("the error message %q contains the reason %q", err.Error(), reason)
	}
}

func TestFailureReasonOfOtherErrors(t *testing.T) {
	t.Parallel()

	if got := local.FailureReason(nil); got != "" {
		t.Errorf("FailureReason(nil) = %q, want empty", got)
	}
	if got := local.FailureReason(errors.New("something else")); got != "" {
		t.Errorf("FailureReason(other) = %q, want empty", got)
	}
}

// A users file that cannot be read is an operator's problem, not a rejected
// credential. Reporting it as "invalid credential" would turn a broken
// deployment into a mystery, and a 401 storm into a red herring.
func TestUnreadableStoreIsNotAnAuthenticationFailure(t *testing.T) {
	t.Parallel()

	path := filepath.Join(t.TempDir(), "users.json")
	if err := os.WriteFile(path, []byte(`{"users": [ broken`), 0o600); err != nil {
		t.Fatalf("seeding the file: %v", err)
	}
	s, err := local.NewStore(path)
	if err != nil {
		t.Fatalf("NewStore error = %v", err)
	}

	token, err := local.NewToken()
	if err != nil {
		t.Fatalf("NewToken error = %v", err)
	}

	_, err = local.NewAuthenticator(s).Authenticate(context.Background(), token)
	if err == nil {
		t.Fatal("Authenticate on a corrupt users file succeeded")
	}
	if errors.Is(err, domain.ErrUnauthenticated) {
		t.Error("a broken users file was reported as an invalid credential")
	}
}

func TestNilStorePanics(t *testing.T) {
	t.Parallel()

	defer func() {
		if recover() == nil {
			t.Error("NewAuthenticator(nil) did not panic")
		}
	}()
	local.NewAuthenticator(nil)
}

// disableUser flips enabled to false by editing the file, which is the
// documented way an operator does it.
func disableUser(t *testing.T, path, subject string) {
	t.Helper()

	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("reading %s: %v", path, err)
	}
	var doc struct {
		Users []map[string]any `json:"users"`
	}
	if err := json.Unmarshal(data, &doc); err != nil {
		t.Fatalf("parsing %s: %v", path, err)
	}
	found := false
	for _, u := range doc.Users {
		if u["subject"] == subject {
			u["enabled"] = false
			found = true
		}
	}
	if !found {
		t.Fatalf("no user %q in %s", subject, path)
	}
	out, err := json.MarshalIndent(doc, "", "  ")
	if err != nil {
		t.Fatalf("encoding: %v", err)
	}
	if err := os.WriteFile(path, out, 0o600); err != nil {
		t.Fatalf("writing %s: %v", path, err)
	}
}
