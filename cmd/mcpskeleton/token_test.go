package main

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// cli runs a command against a temporary users file and returns its output.
type cli struct {
	usersFile string
	now       time.Time
}

func newCLI(t *testing.T) *cli {
	t.Helper()
	return &cli{
		usersFile: filepath.Join(t.TempDir(), "users.json"),
		now:       time.Date(2026, 9, 8, 12, 0, 0, 0, time.UTC),
	}
}

func (c *cli) run(t *testing.T, args ...string) (string, error) {
	t.Helper()

	var out strings.Builder
	env := func(name string) string {
		if name == "MCPSKELETON_USERS_FILE" {
			return c.usersFile
		}
		return ""
	}
	err := run(context.Background(), args, env, &out, &out, func() time.Time { return c.now })
	return out.String(), err
}

func (c *cli) mustRun(t *testing.T, args ...string) string {
	t.Helper()
	out, err := c.run(t, args...)
	if err != nil {
		t.Fatalf("run(%v) error = %v\n%s", args, err, out)
	}
	return out
}

// tokenFrom pulls the issued credential out of the command's output, which is
// the only place it ever exists.
func tokenFrom(t *testing.T, out string) string {
	t.Helper()
	for _, line := range strings.Split(out, "\n") {
		if field := strings.TrimSpace(line); strings.HasPrefix(field, "mcps_") && !strings.Contains(field, " ") {
			return field
		}
	}
	t.Fatalf("no token in the output:\n%s", out)
	return ""
}

func TestTokenIssueCreatesAUserAndPrintsTheTokenOnce(t *testing.T) {
	t.Parallel()

	c := newCLI(t)
	out := c.mustRun(t, "token", "issue", "--user", "alice", "--create", "--scopes", "notes:read,notes:write")

	if !strings.Contains(out, `Created user "alice"`) {
		t.Errorf("output does not say the user was created:\n%s", out)
	}
	if !strings.Contains(out, "notes:read,notes:write") {
		t.Errorf("output does not show the scopes:\n%s", out)
	}
	if !strings.Contains(out, "shown once") {
		t.Errorf("output does not warn that the token cannot be recovered:\n%s", out)
	}
	// The quickstart lives in the output, so nobody has to go looking for it.
	if !strings.Contains(out, "claude mcp add") {
		t.Errorf("output does not show how to use the token:\n%s", out)
	}

	token := tokenFrom(t, out)

	// And the plaintext really is not kept anywhere.
	stored, err := os.ReadFile(c.usersFile)
	if err != nil {
		t.Fatalf("reading the users file: %v", err)
	}
	if strings.Contains(string(stored), token) {
		t.Error("the users file contains the plaintext token")
	}
}

// The guard that turns a typo into a failure rather than a second account with
// a working credential.
func TestTokenIssueRefusesAnUnknownUserWithoutCreate(t *testing.T) {
	t.Parallel()

	c := newCLI(t)
	c.mustRun(t, "token", "issue", "--user", "alice", "--create")

	out, err := c.run(t, "token", "issue", "--user", "alcie")
	if err == nil {
		t.Fatalf("issuing for an unknown user succeeded:\n%s", out)
	}
	if !strings.Contains(err.Error(), "--create") {
		t.Errorf("error = %q, want it to say how to provision the user", err)
	}

	users := c.mustRun(t, "user", "list")
	if strings.Contains(users, "alcie") {
		t.Errorf("the typo created a user:\n%s", users)
	}
}

func TestTokenIssueRequiresAUser(t *testing.T) {
	t.Parallel()

	c := newCLI(t)
	if _, err := c.run(t, "token", "issue", "--create"); err == nil {
		t.Error("token issue succeeded with no --user")
	}
}

func TestTokenIssueTTL(t *testing.T) {
	t.Parallel()

	tests := []struct {
		ttl  string
		want string
	}{
		{"90d", "2026-12-07T12:00:00Z"},
		{"24h", "2026-09-09T12:00:00Z"},
		{"1d", "2026-09-09T12:00:00Z"},
	}

	for _, tc := range tests {
		t.Run(tc.ttl, func(t *testing.T) {
			t.Parallel()

			c := newCLI(t)
			out := c.mustRun(t, "token", "issue", "--user", "alice", "--create", "--ttl", tc.ttl)
			if !strings.Contains(out, tc.want) {
				t.Errorf("output does not show an expiry of %s:\n%s", tc.want, out)
			}
		})
	}
}

func TestTokenIssueRejectsABadTTL(t *testing.T) {
	t.Parallel()

	for _, ttl := range []string{"soon", "-5d", "0d", "-1h", "90days"} {
		t.Run(ttl, func(t *testing.T) {
			t.Parallel()

			c := newCLI(t)
			if _, err := c.run(t, "token", "issue", "--user", "alice", "--create", "--ttl", ttl); err == nil {
				t.Errorf("--ttl %q was accepted", ttl)
			}
		})
	}
}

func TestTokenIssueWithNoTTLNeverExpires(t *testing.T) {
	t.Parallel()

	c := newCLI(t)
	out := c.mustRun(t, "token", "issue", "--user", "alice", "--create")
	if !strings.Contains(out, "never") {
		t.Errorf("output does not say the token never expires:\n%s", out)
	}
}

func TestTokenListShowsWhatWasIssued(t *testing.T) {
	t.Parallel()

	c := newCLI(t)
	c.mustRun(t, "token", "issue", "--user", "alice", "--create", "--scopes", "notes:read")
	c.mustRun(t, "token", "issue", "--user", "bob", "--create", "--scopes", "tools:echo")

	out := c.mustRun(t, "token", "list")

	for _, want := range []string{"alice", "bob", "notes:read", "tools:echo", "TOKEN ID"} {
		if !strings.Contains(out, want) {
			t.Errorf("token list output is missing %q:\n%s", want, out)
		}
	}
	// A listing must never be able to show a credential.
	if strings.Contains(out, "mcps_") {
		t.Errorf("token list printed a credential:\n%s", out)
	}
	if strings.Contains(out, "sha256:") {
		t.Errorf("token list printed a hash:\n%s", out)
	}
}

func TestTokenListWhenNothingHasBeenIssued(t *testing.T) {
	t.Parallel()

	c := newCLI(t)
	out := c.mustRun(t, "token", "list")
	if !strings.Contains(out, "No tokens") {
		t.Errorf("output does not say the list is empty:\n%s", out)
	}
}

func TestTokenRevoke(t *testing.T) {
	t.Parallel()

	c := newCLI(t)
	issued := c.mustRun(t, "token", "issue", "--user", "alice", "--create")
	token := tokenFrom(t, issued)

	id := tokenIDFrom(t, issued)
	out := c.mustRun(t, "token", "revoke", id)
	if !strings.Contains(out, "next request") {
		t.Errorf("output does not say when revocation takes effect:\n%s", out)
	}

	// The hash is gone from the file, so nothing can match it again.
	stored, err := os.ReadFile(c.usersFile)
	if err != nil {
		t.Fatalf("reading the users file: %v", err)
	}
	if strings.Contains(string(stored), token) {
		t.Error("the revoked token is still in the file")
	}
	if strings.Contains(c.mustRun(t, "token", "list"), id) {
		t.Error("the revoked token is still listed")
	}
}

func TestTokenRevokeUnknown(t *testing.T) {
	t.Parallel()

	c := newCLI(t)
	_, err := c.run(t, "token", "revoke", "tok_nope")
	if err == nil {
		t.Fatal("revoking an unknown token succeeded")
	}
	if !strings.Contains(err.Error(), "token list") {
		t.Errorf("error = %q, want it to say where to find the ids", err)
	}
}

func TestTokenRevokeRequiresExactlyOneID(t *testing.T) {
	t.Parallel()

	c := newCLI(t)
	for _, args := range [][]string{
		{"token", "revoke"},
		{"token", "revoke", "a", "b"},
	} {
		if _, err := c.run(t, args...); err == nil {
			t.Errorf("run(%v) succeeded, want an error", args)
		}
	}
}

func TestUserList(t *testing.T) {
	t.Parallel()

	c := newCLI(t)
	c.mustRun(t, "token", "issue", "--user", "alice", "--create")
	c.mustRun(t, "token", "issue", "--user", "alice")

	out := c.mustRun(t, "user", "list")
	if !strings.Contains(out, "alice") {
		t.Errorf("user list is missing alice:\n%s", out)
	}
	// alice has two tokens.
	if !strings.Contains(out, "2") {
		t.Errorf("user list does not show the token count:\n%s", out)
	}
	if strings.Contains(out, "mcps_") || strings.Contains(out, "sha256:") {
		t.Errorf("user list printed credential material:\n%s", out)
	}
}

func TestUserListWhenEmpty(t *testing.T) {
	t.Parallel()

	c := newCLI(t)
	out := c.mustRun(t, "user", "list")
	if !strings.Contains(out, "No users") {
		t.Errorf("output does not say there are no users:\n%s", out)
	}
}

func TestUnknownSubcommands(t *testing.T) {
	t.Parallel()

	c := newCLI(t)
	for _, args := range [][]string{
		{"token"},
		{"token", "mint"},
		{"user"},
		{"user", "add", "alice"},
	} {
		if _, err := c.run(t, args...); err == nil {
			t.Errorf("run(%v) succeeded, want an error", args)
		}
	}
}

// tokenIDFrom pulls the token id out of issue output.
func tokenIDFrom(t *testing.T, out string) string {
	t.Helper()
	for _, line := range strings.Split(out, "\n") {
		if after, ok := strings.CutPrefix(line, "Token id:"); ok {
			return strings.TrimSpace(after)
		}
	}
	t.Fatalf("no token id in the output:\n%s", out)
	return ""
}
