package app_test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/bartdelepeleer/mcpskeleton/internal/adapter/out/auth/local"
	"github.com/bartdelepeleer/mcpskeleton/internal/adapter/tool/echo"
	notestool "github.com/bartdelepeleer/mcpskeleton/internal/adapter/tool/notes"
	"github.com/bartdelepeleer/mcpskeleton/internal/app"
	"github.com/bartdelepeleer/mcpskeleton/internal/config"
)

// These are the acceptance tests. Everything below the client is production
// code — the real local authenticator reading a real users file, real tokens
// minted the way the CLI mints them, the real registry, the real tools. Nothing
// is faked, so a pass here means the assembled server works.

type harness struct {
	server *httptest.Server
	users  *local.Store
	cfg    config.Config
}

func newHarness(t *testing.T, extra map[string]string) *harness {
	t.Helper()

	cfg := testConfig(t, extra)
	wiring, err := app.Build(cfg, nil)
	if err != nil {
		t.Fatalf("Build error = %v", err)
	}

	srv := httptest.NewServer(wiring.Handler)
	t.Cleanup(srv.Close)

	return &harness{server: srv, users: wiring.Users, cfg: cfg}
}

// issue mints a real token through the real store, exactly as the CLI does.
func (h *harness) issue(t *testing.T, subject string, scopes []string, expires *time.Time) local.IssuedToken {
	t.Helper()

	issued, err := h.users.IssueToken(subject, local.IssueOptions{
		Scopes:     scopes,
		ExpiresAt:  expires,
		CreateUser: true,
	})
	if err != nil {
		t.Fatalf("issuing a token for %q: %v", subject, err)
	}
	return issued
}

func (h *harness) connect(t *testing.T, token string) *mcp.ClientSession {
	t.Helper()

	client := mcp.NewClient(&mcp.Implementation{Name: "acceptance", Version: "0.0.0"}, nil)
	session, err := client.Connect(context.Background(), &mcp.StreamableClientTransport{
		Endpoint:   h.server.URL + config.MCPPath,
		HTTPClient: withBearer(h.server.Client(), token),
	}, nil)
	if err != nil {
		t.Fatalf("Connect error = %v", err)
	}
	t.Cleanup(func() { _ = session.Close() })
	return session
}

func withBearer(base *http.Client, token string) *http.Client {
	c := *base
	c.Transport = bearerRoundTripper{token: token, base: base.Transport}
	return &c
}

type bearerRoundTripper struct {
	token string
	base  http.RoundTripper
}

func (b bearerRoundTripper) RoundTrip(req *http.Request) (*http.Response, error) {
	if b.token != "" {
		req = req.Clone(req.Context())
		req.Header.Set("Authorization", "Bearer "+b.token)
	}
	rt := b.base
	if rt == nil {
		rt = http.DefaultTransport
	}
	return rt.RoundTrip(req)
}

func resultText(res *mcp.CallToolResult) string {
	var parts []string
	for _, c := range res.Content {
		if text, ok := c.(*mcp.TextContent); ok {
			parts = append(parts, text.Text)
		}
	}
	return strings.Join(parts, "\n")
}

// The whole thing working: a real token, a real session, a real tool call, and
// the caller's own identity coming back out of it.
func TestAcceptanceHappyPath(t *testing.T) {
	t.Parallel()

	h := newHarness(t, nil)
	issued := h.issue(t, "alice", []string{echo.Scope, notestool.ScopeRead, notestool.ScopeWrite}, nil)
	session := h.connect(t, issued.Token)
	ctx := context.Background()

	tools, err := session.ListTools(ctx, nil)
	if err != nil {
		t.Fatalf("ListTools error = %v", err)
	}
	if len(tools.Tools) != 3 {
		t.Fatalf("tools = %d, want 3", len(tools.Tools))
	}

	saved, err := session.CallTool(ctx, &mcp.CallToolParams{
		Name: "notes_add", Arguments: map[string]any{"body": "acceptance note"},
	})
	if err != nil {
		t.Fatalf("notes_add error = %v", err)
	}
	if saved.IsError {
		t.Fatalf("notes_add returned an error result: %s", resultText(saved))
	}

	listed, err := session.CallTool(ctx, &mcp.CallToolParams{
		Name: "notes_list", Arguments: map[string]any{},
	})
	if err != nil {
		t.Fatalf("notes_list error = %v", err)
	}
	if !strings.Contains(resultText(listed), "acceptance note") {
		t.Errorf("the note was not listed back: %q", resultText(listed))
	}

	echoed, err := session.CallTool(ctx, &mcp.CallToolParams{
		Name: "echo", Arguments: map[string]any{"message": "hello"},
	})
	if err != nil {
		t.Fatalf("echo error = %v", err)
	}
	structured, ok := echoed.StructuredContent.(map[string]any)
	if !ok {
		t.Fatalf("StructuredContent = %T, want an object", echoed.StructuredContent)
	}
	if structured["caller"] != "alice" {
		t.Errorf("the tool saw caller %v, want alice", structured["caller"])
	}
}

func TestAcceptanceUnauthenticated(t *testing.T) {
	t.Parallel()

	h := newHarness(t, nil)

	tests := []struct {
		name  string
		token string
	}{
		{"no credential", ""},
		{"not a token", "hunter2"},
		{"well formed but never issued", mustToken(t)},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			client := mcp.NewClient(&mcp.Implementation{Name: "acceptance", Version: "0.0.0"}, nil)
			_, err := client.Connect(context.Background(), &mcp.StreamableClientTransport{
				Endpoint:   h.server.URL + config.MCPPath,
				HTTPClient: withBearer(h.server.Client(), tc.token),
			}, nil)
			if err == nil {
				t.Fatal("Connect succeeded without a valid credential")
			}
		})
	}
}

func TestAcceptanceWrongScope(t *testing.T) {
	t.Parallel()

	h := newHarness(t, nil)
	issued := h.issue(t, "bob", []string{notestool.ScopeRead}, nil)
	session := h.connect(t, issued.Token)
	ctx := context.Background()

	// What bob may not use, he is not shown.
	tools, err := session.ListTools(ctx, nil)
	if err != nil {
		t.Fatalf("ListTools error = %v", err)
	}
	if len(tools.Tools) != 1 || tools.Tools[0].Name != "notes_list" {
		names := make([]string, len(tools.Tools))
		for i, tool := range tools.Tools {
			names[i] = tool.Name
		}
		t.Fatalf("tools = %v, want only notes_list", names)
	}

	// And calling it anyway is refused, with the reason.
	_, err = session.CallTool(ctx, &mcp.CallToolParams{
		Name: "notes_add", Arguments: map[string]any{"body": "nope"},
	})
	if err == nil {
		t.Fatal("notes_add succeeded without notes:write")
	}
	if !strings.Contains(err.Error(), notestool.ScopeWrite) {
		t.Errorf("error = %q, want it to name the missing scope", err)
	}
}

// Revocation takes effect on the next request, not at the next reconnect. This
// is the test that would fail if authentication were ever cached per session.
func TestAcceptanceRevokedTokenStopsWorkingImmediately(t *testing.T) {
	t.Parallel()

	h := newHarness(t, nil)
	issued := h.issue(t, "alice", []string{echo.Scope}, nil)
	session := h.connect(t, issued.Token)
	ctx := context.Background()

	if _, err := session.CallTool(ctx, &mcp.CallToolParams{
		Name: "echo", Arguments: map[string]any{"message": "before"},
	}); err != nil {
		t.Fatalf("the call before revocation failed: %v", err)
	}

	if err := h.users.RevokeToken(issued.TokenID); err != nil {
		t.Fatalf("RevokeToken error = %v", err)
	}

	// Same open session, same client, next request.
	if _, err := session.CallTool(ctx, &mcp.CallToolParams{
		Name: "echo", Arguments: map[string]any{"message": "after"},
	}); err == nil {
		t.Error("a revoked token still worked on an open session")
	}
}

func TestAcceptanceExpiredToken(t *testing.T) {
	t.Parallel()

	h := newHarness(t, nil)
	past := time.Now().Add(-time.Hour)
	issued := h.issue(t, "alice", []string{echo.Scope}, &past)

	client := mcp.NewClient(&mcp.Implementation{Name: "acceptance", Version: "0.0.0"}, nil)
	if _, err := client.Connect(context.Background(), &mcp.StreamableClientTransport{
		Endpoint:   h.server.URL + config.MCPPath,
		HTTPClient: withBearer(h.server.Client(), issued.Token),
	}, nil); err == nil {
		t.Error("Connect succeeded with an expired token")
	}
}

// A tool-level failure reaches the model as something it can read and act on,
// rather than as a transport error it cannot.
func TestAcceptanceToolFailureIsReadable(t *testing.T) {
	t.Parallel()

	h := newHarness(t, nil)
	issued := h.issue(t, "alice", []string{notestool.ScopeWrite, notestool.ScopeRead}, nil)
	session := h.connect(t, issued.Token)

	res, err := session.CallTool(context.Background(), &mcp.CallToolParams{
		Name: "notes_add", Arguments: map[string]any{"body": "   "},
	})
	if err != nil {
		t.Fatalf("a tool-level failure arrived as a protocol error: %v", err)
	}
	if !res.IsError {
		t.Fatal("IsError = false, want true")
	}
	if !strings.Contains(resultText(res), "must not be empty") {
		t.Errorf("the model was told %q, which does not say what to fix", resultText(res))
	}
}

// Two users, one server, no leakage — through the whole stack.
func TestAcceptanceUsersAreIsolated(t *testing.T) {
	t.Parallel()

	h := newHarness(t, nil)
	scopes := []string{notestool.ScopeRead, notestool.ScopeWrite}
	alice := h.connect(t, h.issue(t, "alice", scopes, nil).Token)
	bob := h.connect(t, h.issue(t, "bob", scopes, nil).Token)
	ctx := context.Background()

	if _, err := alice.CallTool(ctx, &mcp.CallToolParams{
		Name: "notes_add", Arguments: map[string]any{"body": "alice private"},
	}); err != nil {
		t.Fatalf("alice notes_add error = %v", err)
	}

	res, err := bob.CallTool(ctx, &mcp.CallToolParams{Name: "notes_list", Arguments: map[string]any{}})
	if err != nil {
		t.Fatalf("bob notes_list error = %v", err)
	}
	if strings.Contains(resultText(res), "alice private") {
		t.Errorf("bob can see alice's note: %q", resultText(res))
	}
}

// The point of having two NoteStore adapters: with the file one, notes survive
// a restart, and nothing above the port changed to make that true.
func TestAcceptanceNotesSurviveARestartWithTheFileStore(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	vars := map[string]string{
		"MCPSKELETON_USERS_FILE":  filepath.Join(dir, "users.json"),
		"MCPSKELETON_NOTES_FILE":  filepath.Join(dir, "notes.json"),
		"MCPSKELETON_NOTES_STORE": "file",
	}

	first := newHarness(t, vars)
	scopes := []string{notestool.ScopeRead, notestool.ScopeWrite}
	issued := first.issue(t, "alice", scopes, nil)

	session := first.connect(t, issued.Token)
	if _, err := session.CallTool(context.Background(), &mcp.CallToolParams{
		Name: "notes_add", Arguments: map[string]any{"body": "durable note"},
	}); err != nil {
		t.Fatalf("notes_add error = %v", err)
	}

	// The session must be closed before the server is. A streamable-HTTP
	// session holds a long-lived event stream open, and httptest.Server.Close
	// waits for outstanding requests, so closing in the other order hangs
	// forever rather than failing.
	if err := session.Close(); err != nil {
		t.Fatalf("closing the session: %v", err)
	}
	first.server.Close()

	// A completely new server over the same files, as after a restart. The token
	// still works and the note is still there.
	second := newHarness(t, vars)
	revived := second.connect(t, issued.Token)

	res, err := revived.CallTool(context.Background(), &mcp.CallToolParams{
		Name: "notes_list", Arguments: map[string]any{},
	})
	if err != nil {
		t.Fatalf("notes_list after restart error = %v", err)
	}
	if !strings.Contains(resultText(res), "durable note") {
		t.Errorf("the note did not survive the restart: %q", resultText(res))
	}
}

func mustToken(t *testing.T) string {
	t.Helper()
	token, err := local.NewToken()
	if err != nil {
		t.Fatalf("NewToken error = %v", err)
	}
	return token
}
