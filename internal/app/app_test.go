package app_test

import (
	"context"
	"encoding/json"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/bartdelepeleer/mcpskeleton/internal/app"
	"github.com/bartdelepeleer/mcpskeleton/internal/config"
)

func testConfig(t *testing.T, extra map[string]string) config.Config {
	t.Helper()

	dir := t.TempDir()
	vars := map[string]string{
		"MCPSKELETON_ADDR":       "127.0.0.1:0",
		"MCPSKELETON_USERS_FILE": filepath.Join(dir, "users.json"),
		"MCPSKELETON_NOTES_FILE": filepath.Join(dir, "notes.json"),
	}
	for k, v := range extra {
		vars[k] = v
	}

	cfg, err := config.Load(func(name string) string { return vars[name] })
	if err != nil {
		t.Fatalf("config.Load error = %v", err)
	}
	return cfg
}

func TestBuildWiresBothNotesAdapters(t *testing.T) {
	t.Parallel()

	for _, kind := range []string{"memory", "file"} {
		t.Run(kind, func(t *testing.T) {
			t.Parallel()

			cfg := testConfig(t, map[string]string{"MCPSKELETON_NOTES_STORE": kind})
			wiring, err := app.Build(cfg, nil)
			if err != nil {
				t.Fatalf("Build error = %v", err)
			}
			if wiring.NotesStore == nil {
				t.Fatal("no notes store was wired")
			}
			// The same tool set either way: choosing an adapter changes nothing
			// above the port.
			if got := wiring.Registry.Names(); len(got) != 3 {
				t.Errorf("tools = %v, want three regardless of the store", got)
			}
		})
	}
}

func TestUnauthenticatedRoutes(t *testing.T) {
	t.Parallel()

	cfg := testConfig(t, nil)
	wiring, err := app.Build(cfg, nil)
	if err != nil {
		t.Fatalf("Build error = %v", err)
	}
	srv := httptest.NewServer(wiring.Handler)
	t.Cleanup(srv.Close)

	// A load balancer and a client discovering where to authenticate both need
	// to reach these without a credential.
	for _, path := range []string{config.HealthPath, config.ReadyPath, config.ProtectedResourceMetadataPath} {
		t.Run(path, func(t *testing.T) {
			res, err := srv.Client().Get(srv.URL + path)
			if err != nil {
				t.Fatalf("GET %s error = %v", path, err)
			}
			defer res.Body.Close()
			if res.StatusCode != http.StatusOK {
				t.Errorf("GET %s = %d, want %d", path, res.StatusCode, http.StatusOK)
			}
		})
	}
}

// The one route that is not open.
func TestMCPRouteRequiresACredential(t *testing.T) {
	t.Parallel()

	cfg := testConfig(t, nil)
	wiring, err := app.Build(cfg, nil)
	if err != nil {
		t.Fatalf("Build error = %v", err)
	}
	srv := httptest.NewServer(wiring.Handler)
	t.Cleanup(srv.Close)

	res, err := srv.Client().Post(srv.URL+config.MCPPath, "application/json", strings.NewReader("{}"))
	if err != nil {
		t.Fatalf("POST %s error = %v", config.MCPPath, err)
	}
	defer res.Body.Close()

	if res.StatusCode != http.StatusUnauthorized {
		t.Errorf("POST %s without a credential = %d, want %d", config.MCPPath, res.StatusCode, http.StatusUnauthorized)
	}
	if res.Header.Get("WWW-Authenticate") == "" {
		t.Error("the 401 carries no WWW-Authenticate header, so a client cannot discover where to authenticate")
	}
}

// The metadata a real client fetches must describe the server it is actually
// talking to.
func TestMetadataDescribesThisServer(t *testing.T) {
	t.Parallel()

	cfg := testConfig(t, map[string]string{"MCPSKELETON_BASE_URL": "https://mcp.example.com"})
	wiring, err := app.Build(cfg, nil)
	if err != nil {
		t.Fatalf("Build error = %v", err)
	}
	srv := httptest.NewServer(wiring.Handler)
	t.Cleanup(srv.Close)

	res, err := srv.Client().Get(srv.URL + config.ProtectedResourceMetadataPath)
	if err != nil {
		t.Fatalf("GET metadata error = %v", err)
	}
	defer res.Body.Close()

	var doc struct {
		Resource        string   `json:"resource"`
		ScopesSupported []string `json:"scopes_supported"`
	}
	if err := json.NewDecoder(res.Body).Decode(&doc); err != nil {
		t.Fatalf("decoding the metadata: %v", err)
	}
	if doc.Resource != "https://mcp.example.com/mcp" {
		t.Errorf("resource = %q, want the configured MCP endpoint", doc.Resource)
	}
	// The scopes the registered tools actually declare, not a hardcoded list.
	if len(doc.ScopesSupported) != 3 {
		t.Errorf("scopes_supported = %v, want the three the tools declare", doc.ScopesSupported)
	}
}

// Readiness must fail when the thing it checks is broken; a probe that always
// says yes is worse than no probe.
func TestReadinessFailsOnACorruptUsersFile(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	usersFile := filepath.Join(dir, "users.json")
	if err := os.WriteFile(usersFile, []byte(`{"users": [ broken`), 0o600); err != nil {
		t.Fatalf("seeding the users file: %v", err)
	}

	cfg := testConfig(t, map[string]string{"MCPSKELETON_USERS_FILE": usersFile})
	wiring, err := app.Build(cfg, nil)
	if err != nil {
		t.Fatalf("Build error = %v", err)
	}
	srv := httptest.NewServer(wiring.Handler)
	t.Cleanup(srv.Close)

	ready, err := srv.Client().Get(srv.URL + config.ReadyPath)
	if err != nil {
		t.Fatalf("GET %s error = %v", config.ReadyPath, err)
	}
	defer ready.Body.Close()
	if ready.StatusCode != http.StatusServiceUnavailable {
		t.Errorf("readiness = %d with a corrupt users file, want %d", ready.StatusCode, http.StatusServiceUnavailable)
	}

	// Liveness must still pass: the process is fine, its configuration is not,
	// and restarting it would not help.
	alive, err := srv.Client().Get(srv.URL + config.HealthPath)
	if err != nil {
		t.Fatalf("GET %s error = %v", config.HealthPath, err)
	}
	defer alive.Body.Close()
	if alive.StatusCode != http.StatusOK {
		t.Errorf("liveness = %d, want %d; restarting would not fix a corrupt file", alive.StatusCode, http.StatusOK)
	}
}

func TestServeListensAndShutsDownCleanly(t *testing.T) {
	t.Parallel()

	cfg := testConfig(t, nil)
	ctx, cancel := context.WithCancel(context.Background())

	addrs := make(chan net.Addr, 1)
	done := make(chan error, 1)
	go func() {
		done <- app.Serve(ctx, cfg, nil, func(a net.Addr) { addrs <- a })
	}()

	var addr net.Addr
	select {
	case addr = <-addrs:
	case err := <-done:
		t.Fatalf("Serve returned before listening: %v", err)
	case <-time.After(5 * time.Second):
		t.Fatal("Serve did not report an address within 5s")
	}

	res, err := http.Get("http://" + addr.String() + config.HealthPath)
	if err != nil {
		t.Fatalf("GET health error = %v", err)
	}
	_, _ = io.Copy(io.Discard, res.Body)
	res.Body.Close()
	if res.StatusCode != http.StatusOK {
		t.Errorf("health = %d, want %d", res.StatusCode, http.StatusOK)
	}

	cancel()

	select {
	case err := <-done:
		if err != nil {
			t.Errorf("Serve error on shutdown = %v, want nil", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("Serve did not return within 5s of cancellation")
	}

	// And the port is actually released.
	if _, err := http.Get("http://" + addr.String() + config.HealthPath); err == nil {
		t.Error("the server still answers after shutdown")
	}
}

func TestServeReportsAnUnusableAddress(t *testing.T) {
	t.Parallel()

	cfg := testConfig(t, map[string]string{"MCPSKELETON_ADDR": "256.256.256.256:99999"})

	err := app.Serve(context.Background(), cfg, nil, nil)
	if err == nil {
		t.Fatal("Serve succeeded on an unusable address")
	}
	if !strings.Contains(err.Error(), "listening") {
		t.Errorf("error = %v, want it to say it could not listen", err)
	}
}
