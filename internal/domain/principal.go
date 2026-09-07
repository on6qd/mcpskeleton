// Package domain holds the types the whole application agrees on. It imports
// nothing outside the standard library, and in particular knows nothing about
// MCP, HTTP, JSON files or databases.
package domain

import "time"

// Principal is an authenticated identity. It is produced by an Authenticator
// adapter and is complete when returned: the core never looks anything up to
// finish filling it in. That constraint is what lets the local file-backed
// authenticator be replaced by a database- or OIDC-backed one without the core
// changing.
type Principal struct {
	// Subject identifies the user. It is stable and unique within whatever
	// authenticated it.
	Subject string

	// Scopes are opaque permission strings. The core compares them; it never
	// parses, splits or enumerates them, because an external identity provider's
	// scopes will not look like ours.
	Scopes []string

	// ExpiresAt is when the credential stops being valid. The zero value means
	// the credential does not expire.
	ExpiresAt time.Time
}

// HasScope reports whether the principal carries the given scope.
//
// Comparison is exact. An empty want is treated as "no scope required" and is
// always satisfied.
func (p Principal) HasScope(want string) bool {
	if want == "" {
		return true
	}
	for _, s := range p.Scopes {
		if s == want {
			return true
		}
	}
	return false
}

// IsExpired reports whether the credential has expired as of now. A principal
// with no expiry never expires.
func (p Principal) IsExpired(now time.Time) bool {
	if p.ExpiresAt.IsZero() {
		return false
	}
	return !now.Before(p.ExpiresAt)
}
