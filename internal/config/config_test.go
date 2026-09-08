package config_test

import (
	"log/slog"
	"strings"
	"testing"

	"github.com/bartdelepeleer/mcpskeleton/internal/config"
)

// env builds a Getenv from a map, which keeps these tests parallel-safe in a
// way os.Setenv does not.
func env(vars map[string]string) config.Getenv {
	return func(name string) string { return vars[name] }
}

func TestLoadDefaults(t *testing.T) {
	t.Parallel()

	cfg, err := config.Load(env(nil))
	if err != nil {
		t.Fatalf("Load error = %v", err)
	}

	if cfg.Addr != ":8080" {
		t.Errorf("Addr = %q, want %q", cfg.Addr, ":8080")
	}
	if got := cfg.BaseURL.String(); got != "http://localhost:8080" {
		t.Errorf("BaseURL = %q, want %q", got, "http://localhost:8080")
	}
	if cfg.UsersFile != "users.json" {
		t.Errorf("UsersFile = %q, want %q", cfg.UsersFile, "users.json")
	}
	if cfg.NotesStore != config.NotesStoreMemory {
		t.Errorf("NotesStore = %q, want %q", cfg.NotesStore, config.NotesStoreMemory)
	}
	if cfg.LogLevel != slog.LevelInfo {
		t.Errorf("LogLevel = %v, want info", cfg.LogLevel)
	}
	// Empty today, and that is the correct default: this server runs no
	// authorization server.
	if len(cfg.AuthorizationServers) != 0 {
		t.Errorf("AuthorizationServers = %v, want empty", cfg.AuthorizationServers)
	}
}

func TestLoadOverrides(t *testing.T) {
	t.Parallel()

	cfg, err := config.Load(env(map[string]string{
		"MCPSKELETON_ADDR":        "127.0.0.1:9000",
		"MCPSKELETON_BASE_URL":    "https://mcp.example.com",
		"MCPSKELETON_USERS_FILE":  "/etc/mcpskeleton/users.json",
		"MCPSKELETON_NOTES_STORE": "file",
		"MCPSKELETON_NOTES_FILE":  "/var/lib/mcpskeleton/notes.json",
		"MCPSKELETON_LOG_LEVEL":   "debug",
	}))
	if err != nil {
		t.Fatalf("Load error = %v", err)
	}

	if cfg.Addr != "127.0.0.1:9000" {
		t.Errorf("Addr = %q", cfg.Addr)
	}
	if cfg.BaseURL.String() != "https://mcp.example.com" {
		t.Errorf("BaseURL = %q", cfg.BaseURL)
	}
	if cfg.UsersFile != "/etc/mcpskeleton/users.json" {
		t.Errorf("UsersFile = %q", cfg.UsersFile)
	}
	if cfg.NotesStore != config.NotesStoreFile {
		t.Errorf("NotesStore = %q", cfg.NotesStore)
	}
	if cfg.NotesFile != "/var/lib/mcpskeleton/notes.json" {
		t.Errorf("NotesFile = %q", cfg.NotesFile)
	}
	if cfg.LogLevel != slog.LevelDebug {
		t.Errorf("LogLevel = %v, want debug", cfg.LogLevel)
	}
}

// An empty or whitespace variable is the same as an unset one: a deployment
// that exports an empty string meant to leave it alone.
func TestEmptyValuesFallBackToDefaults(t *testing.T) {
	t.Parallel()

	cfg, err := config.Load(env(map[string]string{
		"MCPSKELETON_ADDR":       "",
		"MCPSKELETON_USERS_FILE": "   ",
	}))
	if err != nil {
		t.Fatalf("Load error = %v", err)
	}
	if cfg.Addr != ":8080" {
		t.Errorf("Addr = %q, want the default", cfg.Addr)
	}
	if cfg.UsersFile != "users.json" {
		t.Errorf("UsersFile = %q, want the default", cfg.UsersFile)
	}
}

func TestLogLevels(t *testing.T) {
	t.Parallel()

	tests := map[string]slog.Level{
		"debug": slog.LevelDebug,
		"INFO":  slog.LevelInfo,
		"warn":  slog.LevelWarn,
		"Error": slog.LevelError,
	}

	for in, want := range tests {
		t.Run(in, func(t *testing.T) {
			t.Parallel()
			cfg, err := config.Load(env(map[string]string{"MCPSKELETON_LOG_LEVEL": in}))
			if err != nil {
				t.Fatalf("Load error = %v", err)
			}
			if cfg.LogLevel != want {
				t.Errorf("LogLevel = %v, want %v", cfg.LogLevel, want)
			}
		})
	}
}

func TestAuthorizationServers(t *testing.T) {
	t.Parallel()

	cfg, err := config.Load(env(map[string]string{
		"MCPSKELETON_AUTHORIZATION_SERVERS": "https://idp.example.com, https://other.example.com ,",
	}))
	if err != nil {
		t.Fatalf("Load error = %v", err)
	}

	want := []string{"https://idp.example.com", "https://other.example.com"}
	if len(cfg.AuthorizationServers) != len(want) {
		t.Fatalf("AuthorizationServers = %v, want %v", cfg.AuthorizationServers, want)
	}
	for i, issuer := range want {
		if cfg.AuthorizationServers[i] != issuer {
			t.Errorf("AuthorizationServers[%d] = %q, want %q", i, cfg.AuthorizationServers[i], issuer)
		}
	}
}

// A trailing slash would produce doubled separators wherever the base URL is
// joined with a path.
func TestBaseURLTrailingSlashIsNormalised(t *testing.T) {
	t.Parallel()

	cfg, err := config.Load(env(map[string]string{
		"MCPSKELETON_BASE_URL": "https://mcp.example.com/",
	}))
	if err != nil {
		t.Fatalf("Load error = %v", err)
	}
	if got := cfg.ResourceURL(); got != "https://mcp.example.com/mcp" {
		t.Errorf("ResourceURL = %q, want %q", got, "https://mcp.example.com/mcp")
	}
}

func TestResourceURLUnderAPathPrefix(t *testing.T) {
	t.Parallel()

	cfg, err := config.Load(env(map[string]string{
		"MCPSKELETON_BASE_URL": "https://example.com/services/mcpskeleton",
	}))
	if err != nil {
		t.Fatalf("Load error = %v", err)
	}
	if got, want := cfg.ResourceURL(), "https://example.com/services/mcpskeleton/mcp"; got != want {
		t.Errorf("ResourceURL = %q, want %q", got, want)
	}
}

func TestValidationFailures(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		vars map[string]string
		want string
	}{
		{
			name: "unknown notes store",
			vars: map[string]string{"MCPSKELETON_NOTES_STORE": "postgres"},
			want: "NOTES_STORE",
		},
		{
			name: "unknown log level",
			vars: map[string]string{"MCPSKELETON_LOG_LEVEL": "verbose"},
			want: "LOG_LEVEL",
		},
		{
			name: "base URL with no scheme",
			vars: map[string]string{"MCPSKELETON_BASE_URL": "mcp.example.com"},
			want: "BASE_URL",
		},
		{
			name: "base URL with the wrong scheme",
			vars: map[string]string{"MCPSKELETON_BASE_URL": "ftp://mcp.example.com"},
			want: "BASE_URL",
		},
		{
			name: "base URL with no host",
			vars: map[string]string{"MCPSKELETON_BASE_URL": "https:///path"},
			want: "BASE_URL",
		},
		{
			name: "relative authorization server",
			vars: map[string]string{"MCPSKELETON_AUTHORIZATION_SERVERS": "/oauth"},
			want: "AUTHORIZATION_SERVERS",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			_, err := config.Load(env(tc.vars))
			if err == nil {
				t.Fatalf("Load(%v) succeeded, want an error", tc.vars)
			}
			if !strings.Contains(err.Error(), tc.want) {
				t.Errorf("error %q does not name %s", err, tc.want)
			}
		})
	}
}

// The file store falls back to the default path like everything else, rather
// than demanding one be named. The emptiness check in Load guards against that
// default ever being removed; it is not reachable while one exists.
func TestFileNotesStoreFallsBackToTheDefaultPath(t *testing.T) {
	t.Parallel()

	cfg, err := config.Load(env(map[string]string{
		"MCPSKELETON_NOTES_STORE": "file",
		"MCPSKELETON_NOTES_FILE":  " ",
	}))
	if err != nil {
		t.Fatalf("Load error = %v", err)
	}
	if cfg.NotesFile != "notes.json" {
		t.Errorf("NotesFile = %q, want the default", cfg.NotesFile)
	}
}

// Fixing a misconfigured deployment one variable per restart is miserable, so
// everything wrong is reported at once.
func TestAllProblemsAreReportedTogether(t *testing.T) {
	t.Parallel()

	_, err := config.Load(env(map[string]string{
		"MCPSKELETON_NOTES_STORE": "postgres",
		"MCPSKELETON_LOG_LEVEL":   "verbose",
		"MCPSKELETON_BASE_URL":    "not a url at all",
	}))
	if err == nil {
		t.Fatal("Load succeeded, want an error")
	}

	for _, want := range []string{"NOTES_STORE", "LOG_LEVEL", "BASE_URL"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error does not mention %s:\n%v", want, err)
		}
	}
}

// The metadata document and the routes it describes must not be able to drift,
// so both read these.
func TestPathsAreAbsolute(t *testing.T) {
	t.Parallel()

	paths := map[string]string{
		"MCPPath":                       config.MCPPath,
		"HealthPath":                    config.HealthPath,
		"ReadyPath":                     config.ReadyPath,
		"ProtectedResourceMetadataPath": config.ProtectedResourceMetadataPath,
	}
	for name, path := range paths {
		if !strings.HasPrefix(path, "/") {
			t.Errorf("%s = %q, want it to start with /", name, path)
		}
	}
	if config.ProtectedResourceMetadataPath != "/.well-known/oauth-protected-resource" {
		t.Errorf("the metadata path is %q, which is not where RFC 9728 says clients look",
			config.ProtectedResourceMetadataPath)
	}
}

// PubliclyAddressed decides whether the MCP adapter keeps the SDK's DNS
// rebinding check, so it has to read "is this server reached by a name that is
// not this machine" and nothing looser.
func TestPubliclyAddressed(t *testing.T) {
	t.Parallel()

	tests := []struct {
		baseURL string
		want    bool
	}{
		{"http://localhost:8080", false},
		{"http://LOCALHOST:8080", false},
		{"http://127.0.0.1:8080", false},
		{"http://127.0.0.53:8080", false},
		{"http://[::1]:8080", false},
		{"https://mcpserver.example.com", true},
		{"https://mcpserver.example.com:8443", true},
		{"http://192.168.1.10:8080", true},
	}

	for _, tt := range tests {
		t.Run(tt.baseURL, func(t *testing.T) {
			t.Parallel()

			cfg, err := config.Load(env(map[string]string{"MCPSKELETON_BASE_URL": tt.baseURL}))
			if err != nil {
				t.Fatalf("Load error = %v", err)
			}
			if got := cfg.PubliclyAddressed(); got != tt.want {
				t.Errorf("PubliclyAddressed() with base URL %s = %v, want %v", tt.baseURL, got, tt.want)
			}
		})
	}
}

// A zero Config has no BaseURL, and asking it this question must not panic:
// it is asked while a handler is being built, which is exactly where a nil
// dereference would take down the process at boot.
func TestPubliclyAddressedWithoutABaseURL(t *testing.T) {
	t.Parallel()

	if (config.Config{}).PubliclyAddressed() {
		t.Error("a Config with no BaseURL reported itself publicly addressed")
	}
}
