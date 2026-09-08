package local_test

import (
	"encoding/base64"
	"strings"
	"testing"

	"github.com/bartdelepeleer/mcpskeleton/internal/adapter/out/auth/local"
)

func TestNewTokenFormat(t *testing.T) {
	t.Parallel()

	token, err := local.NewToken()
	if err != nil {
		t.Fatalf("NewToken error = %v", err)
	}

	secret, ok := strings.CutPrefix(token, local.Prefix)
	if !ok {
		t.Fatalf("token %q does not start with %q", token, local.Prefix)
	}

	decoded, err := base64.RawURLEncoding.DecodeString(secret)
	if err != nil {
		t.Fatalf("token body is not unpadded base64url: %v", err)
	}
	if len(decoded) != 32 {
		t.Errorf("token carries %d bytes of entropy, want 32", len(decoded))
	}
	if !local.ValidTokenFormat(token) {
		t.Error("a freshly issued token failed its own format check")
	}
}

// A token goes into an HTTP header and a shell command line, so it must not
// need quoting or escaping anywhere.
func TestNewTokenIsSafeToPasteAnywhere(t *testing.T) {
	t.Parallel()

	const safe = "abcdefghijklmnopqrstuvwxyzABCDEFGHIJKLMNOPQRSTUVWXYZ0123456789-_"

	for range 50 {
		token, err := local.NewToken()
		if err != nil {
			t.Fatalf("NewToken error = %v", err)
		}
		for _, r := range strings.TrimPrefix(token, local.Prefix) {
			if !strings.ContainsRune(safe, r) {
				t.Fatalf("token %q contains %q, which needs escaping somewhere", token, r)
			}
		}
	}
}

func TestNewTokenIsUnique(t *testing.T) {
	t.Parallel()

	seen := make(map[string]bool)
	for range 200 {
		token, err := local.NewToken()
		if err != nil {
			t.Fatalf("NewToken error = %v", err)
		}
		if seen[token] {
			t.Fatalf("NewToken returned a duplicate: %q", local.Redact(token))
		}
		seen[token] = true
	}
}

func TestHashTokenIsStableAndDistinguishing(t *testing.T) {
	t.Parallel()

	a, b := "mcps_aaa", "mcps_bbb"

	if local.HashToken(a) != local.HashToken(a) {
		t.Error("HashToken is not deterministic")
	}
	if local.HashToken(a) == local.HashToken(b) {
		t.Error("different tokens hashed to the same value")
	}
	if !strings.HasPrefix(local.HashToken(a), "sha256:") {
		t.Errorf("hash %q is not labelled with its algorithm", local.HashToken(a))
	}
	// The stored form must not contain the token itself.
	if strings.Contains(local.HashToken(a), a) {
		t.Error("the hash contains the token it was derived from")
	}
}

func TestHashesEqual(t *testing.T) {
	t.Parallel()

	h := local.HashToken("mcps_aaa")

	if !local.HashesEqual(h, h) {
		t.Error("HashesEqual said two identical hashes differ")
	}
	if local.HashesEqual(h, local.HashToken("mcps_bbb")) {
		t.Error("HashesEqual said two different hashes match")
	}
	if local.HashesEqual(h, "") {
		t.Error("HashesEqual matched against an empty hash")
	}
	if local.HashesEqual(h, h[:len(h)-1]) {
		t.Error("HashesEqual matched a truncated hash")
	}
}

func TestValidTokenFormat(t *testing.T) {
	t.Parallel()

	good, err := local.NewToken()
	if err != nil {
		t.Fatalf("NewToken error = %v", err)
	}

	tests := []struct {
		name  string
		token string
		want  bool
	}{
		{"a real token", good, true},
		{"empty", "", false},
		{"no prefix", strings.TrimPrefix(good, local.Prefix), false},
		{"wrong prefix", "sk_" + strings.TrimPrefix(good, local.Prefix), false},
		{"prefix only", local.Prefix, false},
		{"not base64", local.Prefix + "!!!not base64!!!", false},
		{"too short", local.Prefix + base64.RawURLEncoding.EncodeToString(make([]byte, 16)), false},
		{"too long", local.Prefix + base64.RawURLEncoding.EncodeToString(make([]byte, 64)), false},
		{"padded base64", local.Prefix + base64.URLEncoding.EncodeToString(make([]byte, 32)), false},
		{"a bearer header rather than a token", "Bearer " + good, false},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			if got := local.ValidTokenFormat(tc.token); got != tc.want {
				t.Errorf("ValidTokenFormat(%q) = %v, want %v", tc.token, got, tc.want)
			}
		})
	}
}

// Redaction is what stands between a token and a log file, so it has to keep
// nothing usable while keeping enough to identify which token was involved.
func TestRedact(t *testing.T) {
	t.Parallel()

	token, err := local.NewToken()
	if err != nil {
		t.Fatalf("NewToken error = %v", err)
	}

	got := local.Redact(token)

	if got == token {
		t.Fatal("Redact returned the token unchanged")
	}
	if len(got) >= len(token) {
		t.Errorf("Redact(%d chars) returned %d chars; too much survived", len(token), len(got))
	}
	if !local.HashesEqual(local.HashToken(got), local.HashToken(got)) {
		t.Fatal("sanity check failed")
	}
	// The redacted form must not be a usable token.
	if local.ValidTokenFormat(got) {
		t.Error("the redacted form is still a well-formed token")
	}
	// But it must still distinguish one token from another.
	other, err := local.NewToken()
	if err != nil {
		t.Fatalf("NewToken error = %v", err)
	}
	if local.Redact(other) == got {
		t.Error("two different tokens redact to the same string")
	}
}

func TestRedactHandlesShortInput(t *testing.T) {
	t.Parallel()

	for _, in := range []string{"", local.Prefix, local.Prefix + "ab", "garbage"} {
		got := local.Redact(in)
		if strings.Contains(got, "ab") && in == local.Prefix+"ab" {
			t.Errorf("Redact(%q) = %q, leaked the whole secret", in, got)
		}
		if got == "" {
			t.Errorf("Redact(%q) returned an empty string", in)
		}
	}
}
