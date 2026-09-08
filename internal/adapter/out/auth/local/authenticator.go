package local

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"time"

	"github.com/bartdelepeleer/mcpskeleton/internal/domain"
	"github.com/bartdelepeleer/mcpskeleton/internal/port"
)

// Authenticator resolves a bearer token against the users file.
//
// It is the entire local answer to "what does this credential mean", and the
// only place in the codebase that knows tokens are hashed or that a users file
// exists. Replacing it with a database or an identity provider replaces this
// type and nothing else.
type Authenticator struct {
	store *Store
	now   func() time.Time
}

// AuthenticatorOption customises an Authenticator. Options exist for tests.
type AuthenticatorOption func(*Authenticator)

// WithAuthClock replaces the authenticator's clock, which decides expiry.
func WithAuthClock(now func() time.Time) AuthenticatorOption {
	return func(a *Authenticator) { a.now = now }
}

// NewAuthenticator returns an Authenticator backed by store.
func NewAuthenticator(store *Store, opts ...AuthenticatorOption) *Authenticator {
	if store == nil {
		panic("local: nil store")
	}
	a := &Authenticator{store: store, now: time.Now}
	for _, opt := range opts {
		opt(a)
	}
	return a
}

// Authenticate resolves bearer into a Principal.
//
// Every way of failing produces the same error message, so a caller cannot use
// the response to tell an unknown token from a revoked, expired or disabled
// one. The reason is carried separately, for logs, where it is useful and
// harmless.
//
// A failure to read the users file is not an authentication failure — it is an
// operator's problem, and reporting it as "invalid credential" would turn a
// broken deployment into a mystery.
func (a *Authenticator) Authenticate(_ context.Context, bearer string) (domain.Principal, error) {
	if bearer == "" {
		return domain.Principal{}, failure("no credential presented")
	}
	// A cheap filter that avoids a file read for something that cannot be one of
	// our tokens. It is not a security check: a well-formed unknown token is
	// rejected below in exactly the same way.
	if !ValidTokenFormat(bearer) {
		return domain.Principal{}, failure("malformed credential")
	}

	match, found, err := a.store.FindByTokenHash(HashToken(bearer))
	if err != nil {
		return domain.Principal{}, fmt.Errorf("local: reading credentials: %w", err)
	}
	if !found {
		return domain.Principal{}, failure("unknown token")
	}
	if !match.User.Enabled {
		return domain.Principal{}, failure("user disabled")
	}
	if match.Token.IsExpired(a.now()) {
		return domain.Principal{}, failure("token expired")
	}

	// The Principal is complete here. The core will not look anything up to
	// finish it, which is the constraint that keeps this adapter replaceable.
	principal := domain.Principal{
		Subject: match.User.Subject,
		Scopes:  slices.Clone(match.Token.Scopes),
	}
	if match.Token.ExpiresAt != nil {
		principal.ExpiresAt = *match.Token.ExpiresAt
	}
	return principal, nil
}

// unauthenticatedMessage is what every authentication failure says, whatever
// actually went wrong.
const unauthenticatedMessage = "invalid credential"

// authFailure is an authentication failure that reveals nothing in its message
// and remembers why for the benefit of logs.
type authFailure struct{ reason string }

func failure(reason string) error { return &authFailure{reason: reason} }

func (e *authFailure) Error() string { return unauthenticatedMessage }

// Unwrap makes every authentication failure match domain.ErrUnauthenticated,
// which is how the transport knows to answer 401.
func (e *authFailure) Unwrap() error { return domain.ErrUnauthenticated }

// FailureReason returns why an authentication failed, for logging.
//
// It returns "" for anything that is not an authentication failure. The reason
// is deliberately unreachable through Error(), so it cannot end up in a
// response by someone formatting the error.
func FailureReason(err error) string {
	var f *authFailure
	if errors.As(err, &f) {
		return f.reason
	}
	return ""
}

var _ port.Authenticator = (*Authenticator)(nil)
