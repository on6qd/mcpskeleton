package http_test

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	sdkauth "github.com/modelcontextprotocol/go-sdk/auth"

	adapterhttp "github.com/bartdelepeleer/mcpskeleton/internal/adapter/in/http"
	"github.com/bartdelepeleer/mcpskeleton/internal/domain"
	"github.com/bartdelepeleer/mcpskeleton/internal/port"
)

const metadataURL = "https://mcp.example.com/.well-known/oauth-protected-resource"

// fakeAuthenticator is a port.Authenticator driven by a table of tokens.
//
// It is called from concurrent HTTP handlers, so its recording is guarded. A
// fake that races is a fake that fails tests for reasons that have nothing to
// do with the code under test.
type fakeAuthenticator struct {
	principals map[string]domain.Principal
	failWith   error

	mu   sync.Mutex
	seen []string
}

func (f *fakeAuthenticator) Authenticate(_ context.Context, bearer string) (domain.Principal, error) {
	f.mu.Lock()
	f.seen = append(f.seen, bearer)
	f.mu.Unlock()

	if f.failWith != nil {
		return domain.Principal{}, f.failWith
	}
	p, ok := f.principals[bearer]
	if !ok {
		return domain.Principal{}, domain.NewAuthFailure("unknown token")
	}
	return p, nil
}

// credentialsSeen returns every credential presented so far.
func (f *fakeAuthenticator) credentialsSeen() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return slices.Clone(f.seen)
}

var _ port.Authenticator = (*fakeAuthenticator)(nil)

func TestVerifierMapsAPrincipalToTokenInfo(t *testing.T) {
	t.Parallel()

	expires := time.Date(2026, 12, 25, 0, 0, 0, 0, time.UTC)
	auth := &fakeAuthenticator{principals: map[string]domain.Principal{
		"good": {Subject: "alice", Scopes: []string{"notes:read"}, ExpiresAt: expires},
	}}

	info, err := adapterhttp.Verifier(auth, nil)(context.Background(), "good", nil)
	if err != nil {
		t.Fatalf("Verifier error = %v", err)
	}

	// UserID is what makes the transport pin a session to one user.
	if info.UserID != "alice" {
		t.Errorf("UserID = %q, want %q", info.UserID, "alice")
	}
	if len(info.Scopes) != 1 || info.Scopes[0] != "notes:read" {
		t.Errorf("Scopes = %v, want [notes:read]", info.Scopes)
	}
	if !info.Expiration.Equal(expires) {
		t.Errorf("Expiration = %v, want %v", info.Expiration, expires)
	}
}

// A Principal and a TokenInfo carry the same three facts, so nothing may be
// lost on the way through the SDK and back.
func TestPrincipalSurvivesTheRoundTrip(t *testing.T) {
	t.Parallel()

	expires := time.Date(2026, 12, 25, 0, 0, 0, 0, time.UTC)
	want := domain.Principal{
		Subject:   "alice",
		Scopes:    []string{"notes:read", "notes:write", "tools:echo"},
		ExpiresAt: expires,
	}
	auth := &fakeAuthenticator{principals: map[string]domain.Principal{"good": want}}

	var got domain.Principal
	handler := adapterhttp.RequireAuth(auth, metadataURL, nil)(http.HandlerFunc(
		func(w http.ResponseWriter, r *http.Request) {
			got, _ = adapterhttp.PrincipalFromContext(r.Context())
		}))

	req := httptest.NewRequest(http.MethodPost, "/mcp", nil)
	req.Header.Set("Authorization", "Bearer good")
	handler.ServeHTTP(httptest.NewRecorder(), req)

	if got.Subject != want.Subject {
		t.Errorf("Subject = %q, want %q", got.Subject, want.Subject)
	}
	if !got.ExpiresAt.Equal(want.ExpiresAt) {
		t.Errorf("ExpiresAt = %v, want %v", got.ExpiresAt, want.ExpiresAt)
	}
	if len(got.Scopes) != len(want.Scopes) {
		t.Fatalf("Scopes = %v, want %v", got.Scopes, want.Scopes)
	}
	for _, scope := range want.Scopes {
		if !got.HasScope(scope) {
			t.Errorf("scope %q was lost in the round trip", scope)
		}
	}
}

func TestVerifierPropagatesRejections(t *testing.T) {
	t.Parallel()

	auth := &fakeAuthenticator{principals: map[string]domain.Principal{}}

	_, err := adapterhttp.Verifier(auth, nil)(context.Background(), "nope", nil)
	if err == nil {
		t.Fatal("Verifier error = nil, want a rejection")
	}
	if !errors.Is(err, domain.ErrUnauthenticated) {
		t.Errorf("error = %v, want one matching ErrUnauthenticated", err)
	}
	// The SDK decides between 401 and 500 by testing for its own sentinel, so a
	// rejection has to match both.
	if !errors.Is(err, sdkauth.ErrInvalidToken) {
		t.Error("a rejection does not match auth.ErrInvalidToken, so it would be served as a 500")
	}
}

// The 401 body is the verifier's error message, so it must say nothing about
// which credential was presented or why it failed.
func TestUnauthorizedBodyRevealsNothing(t *testing.T) {
	t.Parallel()

	auth := &fakeAuthenticator{principals: map[string]domain.Principal{}}
	handler := adapterhttp.RequireAuth(auth, metadataURL, nil)(protectedHandler(t))

	req := httptest.NewRequest(http.MethodPost, "/mcp", nil)
	req.Header.Set("Authorization", "Bearer some-secret-token")
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	body := rec.Body.String()
	for _, forbidden := range []string{"unknown", "expired", "revoked", "disabled", "some-secret-token"} {
		if strings.Contains(strings.ToLower(body), forbidden) {
			t.Errorf("the 401 body %q reveals %q", body, forbidden)
		}
	}
}

// The reason a credential was refused belongs in the log and nowhere else.
func TestVerifierLogsTheReasonButDoesNotReturnIt(t *testing.T) {
	t.Parallel()

	var buf strings.Builder
	log := slog.New(slog.NewTextHandler(&buf, &slog.HandlerOptions{Level: slog.LevelDebug}))
	auth := &fakeAuthenticator{principals: map[string]domain.Principal{}}

	_, err := adapterhttp.Verifier(auth, log)(context.Background(), "nope", httptest.NewRequest(http.MethodPost, "/mcp", nil))
	if err == nil {
		t.Fatal("Verifier error = nil, want a rejection")
	}

	logged := buf.String()
	if !strings.Contains(logged, "unknown token") {
		t.Errorf("the reason was not logged:\n%s", logged)
	}
	if strings.Contains(err.Error(), "unknown token") {
		t.Errorf("the reason leaked into the error: %q", err.Error())
	}
	// The credential itself must never be written down.
	if strings.Contains(logged, "nope") {
		t.Errorf("the presented credential was logged:\n%s", logged)
	}
}

// A broken users file is not a rejected credential, and must not be logged as
// one — a 500 mislabelled as a 401 sends an operator hunting the wrong thing.
func TestVerifierDistinguishesInfrastructureFailures(t *testing.T) {
	t.Parallel()

	var buf strings.Builder
	log := slog.New(slog.NewTextHandler(&buf, &slog.HandlerOptions{Level: slog.LevelDebug}))
	auth := &fakeAuthenticator{failWith: errors.New("users file is corrupt")}

	_, err := adapterhttp.Verifier(auth, log)(context.Background(), "anything", httptest.NewRequest(http.MethodPost, "/mcp", nil))
	if err == nil {
		t.Fatal("Verifier error = nil, want the infrastructure failure")
	}
	if errors.Is(err, domain.ErrUnauthenticated) {
		t.Error("an infrastructure failure was reported as a rejected credential")
	}

	logged := buf.String()
	if !strings.Contains(logged, "level=ERROR") {
		t.Errorf("an infrastructure failure was not logged at ERROR:\n%s", logged)
	}
}

func TestVerifierToleratesANilRequest(t *testing.T) {
	t.Parallel()

	auth := &fakeAuthenticator{principals: map[string]domain.Principal{}}
	if _, err := adapterhttp.Verifier(auth, nil)(context.Background(), "nope", nil); err == nil {
		t.Error("Verifier error = nil, want a rejection")
	}
}

func TestVerifierPanicsOnANilAuthenticator(t *testing.T) {
	t.Parallel()

	defer func() {
		if recover() == nil {
			t.Error("Verifier(nil, nil) did not panic")
		}
	}()
	adapterhttp.Verifier(nil, nil)
}

func TestRequireAuthRejectsWithoutACredential(t *testing.T) {
	t.Parallel()

	auth := &fakeAuthenticator{principals: map[string]domain.Principal{
		"good": {Subject: "alice"},
	}}
	handler := adapterhttp.RequireAuth(auth, metadataURL, nil)(protectedHandler(t))

	tests := []struct {
		name   string
		header string
	}{
		{"no header", ""},
		{"empty bearer", "Bearer "},
		{"unknown token", "Bearer nope"},
		{"wrong scheme", "Basic dXNlcjpwYXNz"},
		{"bare token with no scheme", "good"},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			req := httptest.NewRequest(http.MethodPost, "/mcp", nil)
			if tc.header != "" {
				req.Header.Set("Authorization", tc.header)
			}
			rec := httptest.NewRecorder()
			handler.ServeHTTP(rec, req)

			if rec.Code != http.StatusUnauthorized {
				t.Errorf("status = %d, want %d", rec.Code, http.StatusUnauthorized)
			}
		})
	}
}

// A client that has never seen this server must be able to discover where to
// authenticate from the 401 alone. That is what RFC 9728 is for.
func TestUnauthorizedPointsAtTheMetadataDocument(t *testing.T) {
	t.Parallel()

	auth := &fakeAuthenticator{principals: map[string]domain.Principal{}}
	handler := adapterhttp.RequireAuth(auth, metadataURL, nil)(protectedHandler(t))

	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/mcp", nil))

	challenge := rec.Header().Get("WWW-Authenticate")
	if challenge == "" {
		t.Fatal("no WWW-Authenticate header on a 401")
	}
	if !strings.Contains(strings.ToLower(challenge), "bearer") {
		t.Errorf("challenge %q does not name the bearer scheme", challenge)
	}
	if !strings.Contains(challenge, metadataURL) {
		t.Errorf("challenge %q does not point at %q", challenge, metadataURL)
	}
}

func TestRequireAuthPassesTheCallerThrough(t *testing.T) {
	t.Parallel()

	want := domain.Principal{Subject: "alice", Scopes: []string{"notes:read", "tools:echo"}}
	auth := &fakeAuthenticator{principals: map[string]domain.Principal{"good": want}}

	var got domain.Principal
	var found bool
	handler := adapterhttp.RequireAuth(auth, metadataURL, nil)(http.HandlerFunc(
		func(w http.ResponseWriter, r *http.Request) {
			got, found = adapterhttp.PrincipalFromContext(r.Context())
			w.WriteHeader(http.StatusOK)
		}))

	req := httptest.NewRequest(http.MethodPost, "/mcp", nil)
	req.Header.Set("Authorization", "Bearer good")
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusOK)
	}
	if !found {
		t.Fatal("the handler found no principal in the context")
	}
	if got.Subject != want.Subject {
		t.Errorf("Subject = %q, want %q", got.Subject, want.Subject)
	}
	for _, scope := range want.Scopes {
		if !got.HasScope(scope) {
			t.Errorf("the handler's principal is missing scope %q", scope)
		}
	}
}

// Tokens with no expiry are legitimate here, and the middleware would otherwise
// reject any token that does not carry one. Expiry is the authenticator's
// decision, not the transport's.
func TestTokensWithoutAnExpiryAreAccepted(t *testing.T) {
	t.Parallel()

	auth := &fakeAuthenticator{principals: map[string]domain.Principal{
		"good": {Subject: "alice"}, // zero ExpiresAt
	}}
	handler := adapterhttp.RequireAuth(auth, metadataURL, nil)(protectedHandler(t))

	req := httptest.NewRequest(http.MethodPost, "/mcp", nil)
	req.Header.Set("Authorization", "Bearer good")
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Errorf("status = %d, want %d; a non-expiring token was refused", rec.Code, http.StatusOK)
	}
}

func TestPrincipalFromContextWithoutAuthentication(t *testing.T) {
	t.Parallel()

	if _, ok := adapterhttp.PrincipalFromContext(context.Background()); ok {
		t.Error("PrincipalFromContext found a caller in an unauthenticated context")
	}

}

func protectedHandler(t *testing.T) http.Handler {
	t.Helper()
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if _, ok := adapterhttp.PrincipalFromContext(r.Context()); !ok {
			t.Error("a request reached the protected handler with no principal")
		}
		w.WriteHeader(http.StatusOK)
	})
}
