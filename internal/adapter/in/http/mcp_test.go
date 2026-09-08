package http_test

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	adapterhttp "github.com/bartdelepeleer/mcpskeleton/internal/adapter/in/http"
	"github.com/bartdelepeleer/mcpskeleton/internal/adapter/out/notes/memory"
	"github.com/bartdelepeleer/mcpskeleton/internal/adapter/tool/echo"
	notestool "github.com/bartdelepeleer/mcpskeleton/internal/adapter/tool/notes"
	"github.com/bartdelepeleer/mcpskeleton/internal/config"
	"github.com/bartdelepeleer/mcpskeleton/internal/core"
	"github.com/bartdelepeleer/mcpskeleton/internal/domain"
)

// These tests drive the real handler with the SDK's own client over a real HTTP
// connection. Anything below the transport is the production wiring: the same
// registry, the same core service, the same tools. Only the authenticator is a
// fake, so that tokens can be assigned to principals in one line.

var serverInfo = adapterhttp.ServerInfo{Name: "mcpskeleton-test", Version: "0.0.0-test"}

// baseURLConfig is the only part of the configuration this handler reads: how
// clients are told to address the server.
func baseURLConfig(t *testing.T, baseURL string) config.Config {
	t.Helper()

	u, err := url.Parse(baseURL)
	if err != nil {
		t.Fatalf("parsing %q: %v", baseURL, err)
	}
	return config.Config{BaseURL: u}
}

// localConfig is the default deployment: reached as localhost and nothing else.
func localConfig(t *testing.T) config.Config {
	t.Helper()
	return baseURLConfig(t, "http://localhost:8080")
}

// toolSet is the production tool set, wired to an in-memory note store.
func toolSet(t *testing.T) *core.Registry {
	t.Helper()
	return core.NewRegistry(append(notestool.All(memory.New()), echo.New())...)
}

func newTestServer(t *testing.T, principals map[string]domain.Principal) *httptest.Server {
	t.Helper()

	registry := toolSet(t)
	svc := core.NewService(registry, core.ScopeAuthorizer{})
	auth := &fakeAuthenticator{principals: principals}

	mux := http.NewServeMux()
	mux.Handle(config.MCPPath, adapterhttp.RequireAuth(auth, metadataURL, nil)(
		adapterhttp.MCPHandler(svc, localConfig(t), serverInfo, nil)))

	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return srv
}

// connect opens an MCP session using the given bearer token.
func connect(t *testing.T, srv *httptest.Server, token string) *mcp.ClientSession {
	t.Helper()

	client := mcp.NewClient(&mcp.Implementation{Name: "test-client", Version: "0.0.0"}, nil)
	transport := &mcp.StreamableClientTransport{
		Endpoint:   srv.URL + config.MCPPath,
		HTTPClient: bearerClient(srv.Client(), token),
	}

	session, err := client.Connect(context.Background(), transport, nil)
	if err != nil {
		t.Fatalf("Connect error = %v", err)
	}
	t.Cleanup(func() { _ = session.Close() })
	return session
}

// bearerClient attaches an Authorization header to every request, which is
// exactly what `claude mcp add --header` does.
func bearerClient(base *http.Client, token string) *http.Client {
	c := *base
	c.Transport = bearerTransport{token: token, base: base.Transport}
	return &c
}

type bearerTransport struct {
	token string
	base  http.RoundTripper
}

func (b bearerTransport) RoundTrip(req *http.Request) (*http.Response, error) {
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

func TestSessionListsOnlyPermittedTools(t *testing.T) {
	t.Parallel()

	srv := newTestServer(t, map[string]domain.Principal{
		"full":     {Subject: "alice", Scopes: []string{echo.Scope, notestool.ScopeRead, notestool.ScopeWrite}},
		"readonly": {Subject: "bob", Scopes: []string{notestool.ScopeRead}},
		"none":     {Subject: "carol"},
	})

	tests := []struct {
		token string
		want  []string
	}{
		{"full", []string{"echo", "notes_add", "notes_list"}},
		{"readonly", []string{"notes_list"}},
		{"none", nil},
	}

	for _, tc := range tests {
		t.Run(tc.token, func(t *testing.T) {
			t.Parallel()

			session := connect(t, srv, tc.token)
			res, err := session.ListTools(context.Background(), nil)
			if err != nil {
				t.Fatalf("ListTools error = %v", err)
			}

			var got []string
			for _, tool := range res.Tools {
				got = append(got, tool.Name)
			}
			if len(got) != len(tc.want) {
				t.Fatalf("tools = %v, want %v", got, tc.want)
			}
			for _, want := range tc.want {
				if !contains(got, want) {
					t.Errorf("tools = %v, missing %q", got, want)
				}
			}
		})
	}
}

func TestCallToolRoundTrip(t *testing.T) {
	t.Parallel()

	srv := newTestServer(t, map[string]domain.Principal{
		"full": {Subject: "alice", Scopes: []string{echo.Scope}},
	})
	session := connect(t, srv, "full")

	res, err := session.CallTool(context.Background(), &mcp.CallToolParams{
		Name:      "echo",
		Arguments: map[string]any{"message": "hello", "times": 2},
	})
	if err != nil {
		t.Fatalf("CallTool error = %v", err)
	}
	if res.IsError {
		t.Fatalf("IsError = true: %v", textOf(res))
	}
	if got := textOf(res); got != "hello\nhello" {
		t.Errorf("text = %q, want %q", got, "hello\nhello")
	}
}

// The property the whole design exists to guarantee, verified end to end over a
// real connection: the identity that authenticated is the identity the tool
// sees.
func TestTheAuthenticatedCallerReachesTheTool(t *testing.T) {
	t.Parallel()

	srv := newTestServer(t, map[string]domain.Principal{
		"alice-token": {Subject: "alice", Scopes: []string{echo.Scope}},
		"bob-token":   {Subject: "bob", Scopes: []string{echo.Scope}},
	})

	for token, want := range map[string]string{"alice-token": "alice", "bob-token": "bob"} {
		t.Run(want, func(t *testing.T) {
			t.Parallel()

			session := connect(t, srv, token)
			res, err := session.CallTool(context.Background(), &mcp.CallToolParams{
				Name:      "echo",
				Arguments: map[string]any{"message": "who am I"},
			})
			if err != nil {
				t.Fatalf("CallTool error = %v", err)
			}

			structured, ok := res.StructuredContent.(map[string]any)
			if !ok {
				t.Fatalf("StructuredContent = %T, want an object", res.StructuredContent)
			}
			if structured["caller"] != want {
				t.Errorf("the tool saw caller %v, want %q", structured["caller"], want)
			}
		})
	}
}

// A tool's own driven port must see the caller too, so one user's notes stay
// invisible to another across real sessions.
func TestNotesAreIsolatedBetweenSessions(t *testing.T) {
	t.Parallel()

	srv := newTestServer(t, map[string]domain.Principal{
		"alice-token": {Subject: "alice", Scopes: []string{notestool.ScopeRead, notestool.ScopeWrite}},
		"bob-token":   {Subject: "bob", Scopes: []string{notestool.ScopeRead, notestool.ScopeWrite}},
	})
	ctx := context.Background()

	alice := connect(t, srv, "alice-token")
	if _, err := alice.CallTool(ctx, &mcp.CallToolParams{
		Name:      "notes_add",
		Arguments: map[string]any{"body": "alice's secret"},
	}); err != nil {
		t.Fatalf("alice notes_add error = %v", err)
	}

	bob := connect(t, srv, "bob-token")
	res, err := bob.CallTool(ctx, &mcp.CallToolParams{Name: "notes_list", Arguments: map[string]any{}})
	if err != nil {
		t.Fatalf("bob notes_list error = %v", err)
	}
	if strings.Contains(textOf(res), "alice's secret") {
		t.Errorf("bob's listing contains alice's note: %q", textOf(res))
	}

	own, err := alice.CallTool(ctx, &mcp.CallToolParams{Name: "notes_list", Arguments: map[string]any{}})
	if err != nil {
		t.Fatalf("alice notes_list error = %v", err)
	}
	if !strings.Contains(textOf(own), "alice's secret") {
		t.Errorf("alice cannot see her own note: %q", textOf(own))
	}
}

// A tool-level failure is a successful protocol exchange carrying an error
// result, so the model reads it and can retry.
func TestToolFailureArrivesAsAnErrorResultNotAProtocolError(t *testing.T) {
	t.Parallel()

	srv := newTestServer(t, map[string]domain.Principal{
		"full": {Subject: "alice", Scopes: []string{echo.Scope}},
	})
	session := connect(t, srv, "full")

	res, err := session.CallTool(context.Background(), &mcp.CallToolParams{
		Name:      "echo",
		Arguments: map[string]any{"message": ""},
	})
	if err != nil {
		t.Fatalf("CallTool returned a protocol error for a tool-level failure: %v", err)
	}
	if !res.IsError {
		t.Fatal("IsError = false, want true")
	}
	if text := textOf(res); !strings.Contains(text, "must not be empty") {
		t.Errorf("the model was told %q, which does not say what to fix", text)
	}
}

// Calling a tool the caller was never listed is refused, not quietly run.
func TestCallingAForbiddenToolIsRefused(t *testing.T) {
	t.Parallel()

	srv := newTestServer(t, map[string]domain.Principal{
		"readonly": {Subject: "bob", Scopes: []string{notestool.ScopeRead}},
	})
	session := connect(t, srv, "readonly")

	_, err := session.CallTool(context.Background(), &mcp.CallToolParams{
		Name:      "notes_add",
		Arguments: map[string]any{"body": "should not be saved"},
	})
	if err == nil {
		t.Fatal("CallTool succeeded on a tool the caller lacks the scope for")
	}
	// The caller is told what they lack. A "no such tool" here would send
	// someone hunting a typo instead of asking for a scope.
	if !strings.Contains(err.Error(), notestool.ScopeWrite) {
		t.Errorf("error = %q, want it to name the missing scope %q", err, notestool.ScopeWrite)
	}

	// And nothing was written.
	res, listErr := session.CallTool(context.Background(), &mcp.CallToolParams{
		Name: "notes_list", Arguments: map[string]any{},
	})
	if listErr != nil {
		t.Fatalf("notes_list error = %v", listErr)
	}
	if strings.Contains(textOf(res), "should not be saved") {
		t.Error("the refused call still wrote a note")
	}
}

func TestUnauthenticatedSessionIsRefused(t *testing.T) {
	t.Parallel()

	srv := newTestServer(t, map[string]domain.Principal{
		"good": {Subject: "alice", Scopes: []string{echo.Scope}},
	})

	client := mcp.NewClient(&mcp.Implementation{Name: "test-client", Version: "0.0.0"}, nil)
	transport := &mcp.StreamableClientTransport{
		Endpoint:   srv.URL + config.MCPPath,
		HTTPClient: bearerClient(srv.Client(), ""),
	}

	if _, err := client.Connect(context.Background(), transport, nil); err == nil {
		t.Fatal("Connect succeeded with no credential")
	}
}

// Unlisted and non-existent are different answers, and a caller needs to be
// able to tell them apart.
func TestForbiddenAndUnknownAreDistinguishable(t *testing.T) {
	t.Parallel()

	srv := newTestServer(t, map[string]domain.Principal{
		"readonly": {Subject: "bob", Scopes: []string{notestool.ScopeRead}},
	})
	session := connect(t, srv, "readonly")
	ctx := context.Background()

	_, forbidden := session.CallTool(ctx, &mcp.CallToolParams{
		Name: "notes_add", Arguments: map[string]any{"body": "x"},
	})
	_, missing := session.CallTool(ctx, &mcp.CallToolParams{
		Name: "no_such_tool", Arguments: map[string]any{},
	})

	if forbidden == nil || missing == nil {
		t.Fatalf("both calls should fail: forbidden=%v missing=%v", forbidden, missing)
	}
	if forbidden.Error() == missing.Error() {
		t.Errorf("a forbidden tool and a missing one give the same message %q", forbidden)
	}
}

func TestUnknownToolIsRefused(t *testing.T) {
	t.Parallel()

	srv := newTestServer(t, map[string]domain.Principal{
		"full": {Subject: "alice", Scopes: []string{echo.Scope}},
	})
	session := connect(t, srv, "full")

	if _, err := session.CallTool(context.Background(), &mcp.CallToolParams{
		Name: "no_such_tool", Arguments: map[string]any{},
	}); err == nil {
		t.Fatal("CallTool succeeded for a tool that does not exist")
	}
}

// The published schema is what the model plans against, so it has to survive
// the trip intact.
func TestToolSchemasReachTheClient(t *testing.T) {
	t.Parallel()

	srv := newTestServer(t, map[string]domain.Principal{
		"full": {Subject: "alice", Scopes: []string{echo.Scope}},
	})
	session := connect(t, srv, "full")

	res, err := session.ListTools(context.Background(), nil)
	if err != nil {
		t.Fatalf("ListTools error = %v", err)
	}
	if len(res.Tools) != 1 {
		t.Fatalf("tools = %d, want 1", len(res.Tools))
	}

	tool := res.Tools[0]
	if tool.Description == "" {
		t.Error("the tool arrived with no description")
	}
	encoded, err := json.Marshal(tool.InputSchema)
	if err != nil {
		t.Fatalf("marshalling the schema: %v", err)
	}
	for _, want := range []string{"message", "times", "required"} {
		if !strings.Contains(string(encoded), want) {
			t.Errorf("the schema that reached the client has no %q:\n%s", want, encoded)
		}
	}
}

func textOf(res *mcp.CallToolResult) string {
	var parts []string
	for _, c := range res.Content {
		if text, ok := c.(*mcp.TextContent); ok {
			parts = append(parts, text.Text)
		}
	}
	return strings.Join(parts, "\n")
}

func contains(haystack []string, needle string) bool {
	for _, s := range haystack {
		if s == needle {
			return true
		}
	}
	return false
}

// Authentication runs on every request, not once when the session opens. That
// is what makes revoking a token take effect immediately rather than whenever
// the client happens to reconnect.
func TestEveryRequestIsAuthenticated(t *testing.T) {
	t.Parallel()

	auth := &fakeAuthenticator{principals: map[string]domain.Principal{
		"full": {Subject: "alice", Scopes: []string{echo.Scope}},
	}}
	svc := core.NewService(toolSet(t), core.ScopeAuthorizer{})

	mux := http.NewServeMux()
	mux.Handle(config.MCPPath, adapterhttp.RequireAuth(auth, metadataURL, nil)(
		adapterhttp.MCPHandler(svc, localConfig(t), serverInfo, nil)))
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)

	session := connect(t, srv, "full")
	ctx := context.Background()

	before := len(auth.credentialsSeen())
	for range 3 {
		if _, err := session.CallTool(ctx, &mcp.CallToolParams{
			Name:      "echo",
			Arguments: map[string]any{"message": "hi"},
		}); err != nil {
			t.Fatalf("CallTool error = %v", err)
		}
	}
	after := len(auth.credentialsSeen())

	if after-before < 3 {
		t.Errorf("the authenticator saw %d credentials across 3 tool calls; "+
			"a revoked token would keep working until the client reconnected", after-before)
	}
}

// The transport pins a session to the user that opened it, so a session id
// cannot be reused with someone else's credential.
func TestASessionCannotBeContinuedByAnotherUser(t *testing.T) {
	t.Parallel()

	srv := newTestServer(t, map[string]domain.Principal{
		"alice-token": {Subject: "alice", Scopes: []string{echo.Scope}},
		"bob-token":   {Subject: "bob", Scopes: []string{echo.Scope}},
	})

	// Open a session as alice and learn its id.
	client := mcp.NewClient(&mcp.Implementation{Name: "test-client", Version: "0.0.0"}, nil)
	transport := &mcp.StreamableClientTransport{
		Endpoint:   srv.URL + config.MCPPath,
		HTTPClient: bearerClient(srv.Client(), "alice-token"),
	}
	session, err := client.Connect(context.Background(), transport, nil)
	if err != nil {
		t.Fatalf("Connect error = %v", err)
	}
	t.Cleanup(func() { _ = session.Close() })

	sessionID := session.ID()
	if sessionID == "" {
		t.Skip("the transport is running without session ids; there is nothing to hijack")
	}

	// The same replayed request, sent with alice's own credential, must succeed.
	// Without this the test would pass for any reason a replay is rejected —
	// a missing header, a rejected content type — rather than because of who
	// sent it.
	if status := replay(t, srv, sessionID, "alice-token"); status != http.StatusOK {
		t.Fatalf("alice replaying her own session got %d; the test cannot tell why bob is refused", status)
	}

	if status := replay(t, srv, sessionID, "bob-token"); status == http.StatusOK {
		t.Errorf("bob continued alice's session (status %d)", status)
	}
}

// replay sends a tools/list directly, continuing an existing session, with the
// given credential. It returns the status code.
func replay(t *testing.T, srv *httptest.Server, sessionID, token string) int {
	t.Helper()

	req, err := http.NewRequest(http.MethodPost, srv.URL+config.MCPPath,
		strings.NewReader(`{"jsonrpc":"2.0","id":1,"method":"tools/list","params":{}}`))
	if err != nil {
		t.Fatalf("building the request: %v", err)
	}
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json, text/event-stream")
	req.Header.Set("Mcp-Session-Id", sessionID)

	res, err := srv.Client().Do(req)
	if err != nil {
		t.Fatalf("replaying session %s as %s: %v", sessionID, token, err)
	}
	defer res.Body.Close()
	_, _ = io.Copy(io.Discard, res.Body)
	return res.StatusCode
}

// The SDK refuses a non-loopback Host header when the listener is on loopback.
// A reverse proxy terminating TLS and dialling 127.0.0.1 sends exactly that on
// every request, so a server that cannot be told it lives behind one is a
// server that cannot be deployed. BaseURL is what tells it.
func TestBaseURLDecidesWhichHostHeadersAreAccepted(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		baseURL string
		want    int
	}{
		{
			name:    "addressed by a public name",
			baseURL: "https://mcp.example.com",
			want:    http.StatusOK,
		},
		{
			// The deployment the SDK's check was written for, where a foreign
			// Host really is somebody else's DNS pointed at this machine.
			name:    "left on the default localhost base URL",
			baseURL: "http://localhost:8080",
			want:    http.StatusForbidden,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			auth := &fakeAuthenticator{principals: map[string]domain.Principal{
				"full": {Subject: "alice", Scopes: []string{echo.Scope}},
			}}
			svc := core.NewService(toolSet(t), core.ScopeAuthorizer{})

			mux := http.NewServeMux()
			mux.Handle(config.MCPPath, adapterhttp.RequireAuth(auth, metadataURL, nil)(
				adapterhttp.MCPHandler(svc, baseURLConfig(t, tt.baseURL), serverInfo, nil)))
			srv := httptest.NewServer(mux)
			t.Cleanup(srv.Close)

			body := `{"jsonrpc":"2.0","id":1,"method":"initialize","params":` +
				`{"protocolVersion":"2025-06-18","capabilities":{},` +
				`"clientInfo":{"name":"test-client","version":"0.0.0"}}}`

			req, err := http.NewRequest(http.MethodPost, srv.URL+config.MCPPath, strings.NewReader(body))
			if err != nil {
				t.Fatalf("NewRequest error = %v", err)
			}
			// What a reverse proxy forwards: the name the client asked for,
			// not the address it dialled.
			req.Host = "mcp.example.com"
			req.Header.Set("Authorization", "Bearer full")
			req.Header.Set("Content-Type", "application/json")
			req.Header.Set("Accept", "application/json, text/event-stream")

			res, err := srv.Client().Do(req)
			if err != nil {
				t.Fatalf("Do error = %v", err)
			}
			defer res.Body.Close()
			_, _ = io.Copy(io.Discard, res.Body)

			if res.StatusCode != tt.want {
				t.Errorf("POST %s with Host %q and base URL %s = %d, want %d",
					config.MCPPath, req.Host, tt.baseURL, res.StatusCode, tt.want)
			}
		})
	}
}
