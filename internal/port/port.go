// Package port declares the interfaces that separate the core from the world.
//
// Everything the core needs from outside is a port here, and everything outside
// reaches the core through one. Like domain, this package imports nothing but
// the standard library and domain — including, deliberately, no MCP SDK types.
// A port that mentioned the SDK would make the SDK impossible to replace, which
// is the failure mode ports exist to prevent.
package port

import (
	"context"
	"encoding/json"

	"github.com/bartdelepeleer/mcpskeleton/internal/domain"
)

// Authenticator turns a bearer credential into an authenticated identity.
//
// This is the coarse seam described in the plan: the adapter owns the entire
// decision of what a credential means. It parses the credential, verifies it
// however it likes, and returns a complete Principal. The core does not know
// what a token hash is, and will not know what a JWT is either.
//
// Implementations return domain.ErrUnauthenticated (possibly wrapped) when the
// credential is absent, malformed, unknown, revoked or expired. They must not
// distinguish between those cases in the returned error message, because that
// message may reach an unauthenticated caller.
type Authenticator interface {
	Authenticate(ctx context.Context, bearer string) (domain.Principal, error)
}

// Authorizer decides whether a principal may perform an action requiring a
// given scope.
//
// It takes a scope string rather than a tool, so that it never needs to know
// what a tool is, and so scopes stay opaque.
type Authorizer interface {
	Allow(p domain.Principal, requiredScope string) bool
}

// Result is what a tool produces on success.
type Result struct {
	// Text is the content returned to the model.
	Text string

	// Structured, when non-nil, is returned alongside Text as the tool's
	// structured output. Tools that return only prose leave it nil.
	Structured any
}

// Tool is one capability the server exposes. Every tool in this codebase is an
// adapter implementing this interface; adding a tool means writing one and
// registering it.
//
// Execute takes the authenticated principal as an explicit parameter rather
// than pulling it from the context. That is a deliberate choice: it makes
// "every tool receives the caller's identity" a property the compiler enforces,
// instead of a convention a new tool can forget.
type Tool interface {
	// Name is the tool's MCP name. It must be unique across the registry.
	Name() string

	// Description is shown to the model.
	Description() string

	// RequiredScope is the scope a principal must hold to see or call this tool.
	// An empty string means the tool needs no scope.
	RequiredScope() string

	// InputSchema is the tool's JSON Schema for its arguments, as raw JSON.
	//
	// Raw JSON rather than a typed schema value keeps this package free of any
	// schema library, and JSON Schema is a wire format anyway. The MCP adapter
	// decodes it once at wiring time; the typed tool helper generates it.
	InputSchema() json.RawMessage

	// Execute runs the tool.
	//
	// It returns a *domain.ToolError for a failure the model should see and may
	// retry against, and any other error for an infrastructure failure the model
	// cannot act on. The core relies on that distinction.
	Execute(ctx context.Context, p domain.Principal, args json.RawMessage) (Result, error)
}

// NoteStore is the driven port behind the reference notes tool.
//
// It exists to demonstrate the pattern a real tool will follow: the tool adapter
// depends on a port, and the storage behind it is swappable. Two adapters
// implement it — in-memory and JSON file — which is the same swap the
// authenticator will make when it moves to a database.
type NoteStore interface {
	// Add stores a note owned by owner and returns it, populated with its
	// generated ID and creation time.
	Add(ctx context.Context, owner, body string) (domain.Note, error)

	// List returns owner's notes, oldest first. An owner with no notes yields an
	// empty slice and no error.
	List(ctx context.Context, owner string) ([]domain.Note, error)
}
