package domain_test

import (
	"testing"
	"time"

	"github.com/bartdelepeleer/mcpskeleton/internal/domain"
)

func TestPrincipalHasScope(t *testing.T) {
	t.Parallel()

	p := domain.Principal{Subject: "alice", Scopes: []string{"notes:read", "notes:write"}}

	tests := []struct {
		name  string
		p     domain.Principal
		want  string
		allow bool
	}{
		{"held scope", p, "notes:read", true},
		{"other held scope", p, "notes:write", true},
		{"absent scope", p, "tools:echo", false},
		{"empty requirement is always satisfied", p, "", true},
		{"empty requirement with no scopes", domain.Principal{Subject: "bob"}, "", true},
		{"no scopes at all", domain.Principal{Subject: "bob"}, "notes:read", false},
		{"scopes are compared exactly, not by prefix", p, "notes", false},
		{"scopes are compared exactly, not by suffix", p, "read", false},
		{"comparison is case sensitive", p, "Notes:Read", false},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			if got := tc.p.HasScope(tc.want); got != tc.allow {
				t.Errorf("HasScope(%q) = %v, want %v", tc.want, got, tc.allow)
			}
		})
	}
}

func TestPrincipalIsExpired(t *testing.T) {
	t.Parallel()

	now := time.Date(2026, 9, 7, 12, 0, 0, 0, time.UTC)

	tests := []struct {
		name    string
		expires time.Time
		want    bool
	}{
		{"zero expiry never expires", time.Time{}, false},
		{"future expiry is valid", now.Add(time.Hour), false},
		{"past expiry is expired", now.Add(-time.Hour), true},
		{"expiry exactly now is expired", now, true},
		{"one nanosecond of life left", now.Add(time.Nanosecond), false},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			p := domain.Principal{Subject: "alice", ExpiresAt: tc.expires}
			if got := p.IsExpired(now); got != tc.want {
				t.Errorf("IsExpired() = %v, want %v", got, tc.want)
			}
		})
	}
}
