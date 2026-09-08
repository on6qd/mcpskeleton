// Package http is the driving adapter: it turns HTTP requests into calls on the
// core, and the core's answers back into HTTP.
//
// It is the only package that knows this server speaks MCP over HTTP. The core
// beneath it would work just as well behind a different transport, which is the
// property that makes the transport replaceable.
package http

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"slices"

	sdkauth "github.com/modelcontextprotocol/go-sdk/auth"

	"github.com/bartdelepeleer/mcpskeleton/internal/domain"
	"github.com/bartdelepeleer/mcpskeleton/internal/port"
)

// Verifier adapts a port.Authenticator to the shape the SDK's bearer-token
// middleware expects.
//
// This function is the entire coupling between our authentication design and
// the SDK's. It exists so that port.Authenticator never has to mention an SDK
// type — which is what keeps the SDK replaceable, and what keeps the eventual
// OIDC adapter a change to one outbound package rather than to the port.
func Verifier(authenticator port.Authenticator, log *slog.Logger) sdkauth.TokenVerifier {
	if authenticator == nil {
		panic("http: nil authenticator")
	}
	if log == nil {
		log = slog.New(slog.DiscardHandler)
	}

	return func(ctx context.Context, token string, req *http.Request) (*sdkauth.TokenInfo, error) {
		principal, err := authenticator.Authenticate(ctx, token)
		if err != nil {
			if errors.Is(err, domain.ErrUnauthenticated) {
				// The reason is logged here and nowhere else. It never reaches
				// the response, where it would let a caller tell an unknown
				// credential from a revoked one.
				log.LogAttrs(ctx, slog.LevelInfo, "authentication rejected",
					slog.String("reason", domain.FailureReason(err)),
					slog.String("path", requestPath(req)),
					slog.String("request_id", requestID(req)),
				)
				return nil, rejected(err)
			}

			// Not a rejected credential — something is wrong with the server
			// itself. It is logged louder, and returned unmarked so the
			// middleware answers 500 rather than 401.
			log.LogAttrs(ctx, slog.LevelError, "authentication failed",
				slog.String("error", err.Error()),
				slog.String("path", requestPath(req)),
				slog.String("request_id", requestID(req)),
			)
			return nil, err
		}

		// Tell the access log who this request turned out to belong to.
		if req != nil {
			recordSubject(req.Context(), principal.Subject)
		}

		return tokenInfo(principal), nil
	}
}

// rejected marks a refused credential so the SDK's middleware answers 401
// rather than 500. The SDK decides between the two by testing for its own
// ErrInvalidToken, so a rejection has to match both that and our
// domain.ErrUnauthenticated — hence the two-armed unwrap.
//
// The message is unchanged, which matters more than it looks: the middleware
// puts it in the 401 response body, so it has to stay the constant that tells a
// caller nothing.
func rejected(err error) error { return refusal{err: err} }

type refusal struct{ err error }

func (r refusal) Error() string   { return r.err.Error() }
func (r refusal) Unwrap() []error { return []error{r.err, sdkauth.ErrInvalidToken} }

// tokenInfo converts a Principal into the SDK's view of an authenticated
// caller.
//
// UserID is the important field: when it is set, the streamable-HTTP transport
// refuses requests that continue a session under a different user, which is
// session-hijacking protection we get from the transport rather than writing
// ourselves.
func tokenInfo(p domain.Principal) *sdkauth.TokenInfo {
	return &sdkauth.TokenInfo{
		UserID:     p.Subject,
		Scopes:     slices.Clone(p.Scopes),
		Expiration: p.ExpiresAt,
	}
}

// principalFrom converts back, for the handlers that need the caller's
// identity.
//
// The two types carry the same three facts, so the round trip is lossless. If
// Principal ever grows a field the SDK has no home for, this is where it would
// have to move into TokenInfo.Extra — and this comment is the reminder.
func principalFrom(info *sdkauth.TokenInfo) domain.Principal {
	if info == nil {
		return domain.Principal{}
	}
	return domain.Principal{
		Subject:   info.UserID,
		Scopes:    slices.Clone(info.Scopes),
		ExpiresAt: info.Expiration,
	}
}

// PrincipalFromContext returns the caller established by the bearer-token
// middleware, and whether there was one.
func PrincipalFromContext(ctx context.Context) (domain.Principal, bool) {
	info := sdkauth.TokenInfoFromContext(ctx)
	if info == nil || info.UserID == "" {
		return domain.Principal{}, false
	}
	return principalFrom(info), true
}

// RequireAuth returns middleware that rejects any request without a valid
// bearer token, answering 401 with a WWW-Authenticate header pointing at the
// protected-resource metadata.
//
// resourceMetadataURL is the absolute URL of that document. A client that has
// never seen this server discovers where to authenticate from the 401 alone,
// which is what RFC 9728 is for.
func RequireAuth(authenticator port.Authenticator, resourceMetadataURL string, log *slog.Logger) func(http.Handler) http.Handler {
	return sdkauth.RequireBearerToken(Verifier(authenticator, log), &sdkauth.RequireBearerTokenOptions{
		ResourceMetadataURL: resourceMetadataURL,

		// Our tokens may legitimately have no expiry, and the middleware
		// otherwise rejects any token that does not carry one. Expiry is the
		// authenticator's decision, not the transport's: it already refuses an
		// expired token, and it is the component that will know how an identity
		// provider expresses expiry once one is in play.
		AllowMissingExpiration: true,

		// Scopes are checked per tool by the core, not per request here. A
		// blanket scope requirement at the door would mean every tool needed the
		// same permission, which is the opposite of what the design is for.
		Scopes: nil,
	})
}

func requestPath(req *http.Request) string {
	if req == nil || req.URL == nil {
		return ""
	}
	return req.URL.Path
}

func requestID(req *http.Request) string {
	if req == nil {
		return ""
	}
	return RequestID(req.Context())
}
