package core_test

import (
	"testing"

	"github.com/bartdelepeleer/mcpskeleton/internal/core"
	"github.com/bartdelepeleer/mcpskeleton/internal/domain"
)

func TestScopeAuthorizerAllow(t *testing.T) {
	t.Parallel()

	alice := domain.Principal{Subject: "alice", Scopes: []string{"notes:read", "tools:echo"}}
	nobody := domain.Principal{Subject: "nobody"}

	tests := []struct {
		name     string
		p        domain.Principal
		required string
		want     bool
	}{
		{"held scope is allowed", alice, "notes:read", true},
		{"unheld scope is denied", alice, "notes:write", false},
		{"no scope required is allowed", alice, "", true},
		{"no scope required, principal has none", nobody, "", true},
		{"principal with no scopes is denied", nobody, "notes:read", false},

		// Scopes are opaque to the core: it compares them and does nothing else.
		// These cases pin that down, because a future implementation that starts
		// splitting on ":" would silently widen everyone's access.
		{"a prefix of a held scope is not held", alice, "notes", false},
		{"a suffix of a held scope is not held", alice, "read", false},
		{"a wildcard is just another string", alice, "notes:*", false},
		{"case differences are not held", alice, "NOTES:READ", false},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			if got := (core.ScopeAuthorizer{}).Allow(tc.p, tc.required); got != tc.want {
				t.Errorf("Allow(%q) = %v, want %v", tc.required, got, tc.want)
			}
		})
	}
}
