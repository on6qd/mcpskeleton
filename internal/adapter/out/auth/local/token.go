// Package local is the file-backed Authenticator: the adapter that owns, today,
// the entire decision of what a credential means.
//
// It is written to be replaced. When authentication moves to a database, or to
// an identity provider, that is a sibling package implementing the same
// port.Authenticator and nothing above the port changes. Nothing here — token
// formats, hashes, the users file — is visible to the core.
package local

import (
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"encoding/hex"
	"fmt"
	"strings"
)

const (
	// Prefix marks a string as one of this server's tokens. It exists so a
	// leaked credential is recognisable in a log or a repository by automated
	// secret scanners, and so a token pasted into the wrong field is obvious.
	Prefix = "mcps_"

	// secretBytes is the amount of entropy in a token.
	//
	// 256 bits of crypto/rand is why these are hashed with SHA-256 rather than
	// argon2id. Password hashes are slow in order to frustrate guessing a
	// low-entropy human secret; there is nothing to guess here, and a slow hash
	// on every request would cost real latency to defend against an attack that
	// cannot happen.
	secretBytes = 32

	// hashPrefix labels a stored hash with the algorithm that produced it, so a
	// future change of algorithm can be rolled out by recognising both.
	hashPrefix = "sha256:"
)

// NewToken returns a fresh token: the prefix followed by 32 random bytes in
// unpadded base64url.
func NewToken() (string, error) {
	secret := make([]byte, secretBytes)
	if _, err := rand.Read(secret); err != nil {
		return "", fmt.Errorf("local: reading random bytes: %w", err)
	}
	return Prefix + base64.RawURLEncoding.EncodeToString(secret), nil
}

// HashToken returns the stored form of a token.
//
// The whole token string is hashed, prefix included, so there is no question of
// which part was covered.
func HashToken(token string) string {
	sum := sha256.Sum256([]byte(token))
	return hashPrefix + hex.EncodeToString(sum[:])
}

// HashesEqual compares two stored hashes in constant time.
//
// Comparing digests rather than tokens already removes most of the timing risk,
// since a digest cannot be inverted into the secret that produced it. This is
// the cheap remaining precaution, and it costs nothing.
func HashesEqual(a, b string) bool {
	return subtle.ConstantTimeCompare([]byte(a), []byte(b)) == 1
}

// ValidTokenFormat reports whether s has the shape of a token this package
// issues.
//
// It is a cheap filter, not a security check: it lets an obviously malformed
// credential be rejected without a lookup. A well-formed unknown token is
// rejected later, in exactly the same way, so the two are indistinguishable to
// a caller.
func ValidTokenFormat(s string) bool {
	secret, ok := strings.CutPrefix(s, Prefix)
	if !ok {
		return false
	}
	decoded, err := base64.RawURLEncoding.DecodeString(secret)
	if err != nil {
		return false
	}
	return len(decoded) == secretBytes
}

// Redact returns a form of a token safe to put in a log line.
//
// Enough of the tail is kept to match it against a token an operator is holding,
// and never enough to use it. The prefix is dropped because every token has it.
func Redact(token string) string {
	const keep = 6
	secret := strings.TrimPrefix(token, Prefix)
	if len(secret) <= keep {
		return Prefix + "..."
	}
	return Prefix + "..." + secret[len(secret)-keep:]
}
