// Package config turns the environment into a validated struct.
//
// Everything is read once, at boot, and everything invalid is reported then —
// a server that starts and only later discovers it cannot write its notes file
// has failed in the least useful way possible.
//
// There is no config library here on purpose. A skeleton that ships with one
// has made a choice its users have to live with; twelve-factor environment
// variables plus a struct is the choice that is easiest to replace.
package config

import (
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/url"
	"strings"
)

// NotesStoreKind selects which NoteStore adapter is wired in.
//
// This field is the reference tools' half of the point of this codebase: the
// same tool runs against either adapter, chosen here, with nothing above the
// port aware of the difference.
type NotesStoreKind string

const (
	NotesStoreMemory NotesStoreKind = "memory"
	NotesStoreFile   NotesStoreKind = "file"
)

// Prefix is prepended to every environment variable this package reads.
const Prefix = "MCPSKELETON_"

// Config is the validated configuration of one process.
type Config struct {
	// Addr is the address the HTTP server listens on, as accepted by net.Listen.
	Addr string

	// BaseURL is how clients reach this server from outside.
	//
	// It is not derivable from Addr — behind a reverse proxy the two differ —
	// and it has to be right, because it is the resource identifier published in
	// the protected-resource metadata, and a token audienced to the wrong
	// resource is exactly what that metadata exists to prevent.
	BaseURL *url.URL

	// AuthorizationServers are the issuers a client should authenticate with,
	// published in the protected-resource metadata.
	//
	// Empty today: this server issues its own tokens and runs no authorization
	// server. The field exists now rather than later so that pointing at a real
	// identity provider is configuration rather than a code change — one of the
	// three constraints that keep the OIDC swap a single-adapter change.
	AuthorizationServers []string

	// UsersFile is the local authenticator's users file.
	UsersFile string

	// NotesStore selects the NoteStore adapter.
	NotesStore NotesStoreKind

	// NotesFile is where the file NoteStore writes. Ignored when NotesStore is
	// memory.
	NotesFile string

	// LogLevel is the minimum level that gets logged.
	LogLevel slog.Level
}

// Defaults, applied when a variable is unset or empty.
const (
	defaultAddr       = ":8080"
	defaultBaseURL    = "http://localhost:8080"
	defaultUsersFile  = "users.json"
	defaultNotesStore = NotesStoreMemory
	defaultNotesFile  = "notes.json"
	defaultLogLevel   = slog.LevelInfo
)

// Getenv looks up an environment variable. os.Getenv satisfies it; tests pass a
// map, which keeps them parallel-safe in a way os.Setenv does not.
type Getenv func(string) string

// Load reads and validates the configuration.
//
// Every problem found is reported together rather than one per run, because
// fixing a misconfigured deployment one variable at a time is miserable.
func Load(getenv Getenv) (Config, error) {
	get := func(name, fallback string) string {
		if v := strings.TrimSpace(getenv(Prefix + name)); v != "" {
			return v
		}
		return fallback
	}

	cfg := Config{
		Addr:      get("ADDR", defaultAddr),
		UsersFile: get("USERS_FILE", defaultUsersFile),
		NotesFile: get("NOTES_FILE", defaultNotesFile),
	}

	var problems []error
	note := func(format string, args ...any) {
		problems = append(problems, fmt.Errorf(format, args...))
	}

	base, err := url.Parse(get("BASE_URL", defaultBaseURL))
	switch {
	case err != nil:
		note("%sBASE_URL is not a valid URL: %v", Prefix, err)
	case base.Scheme != "http" && base.Scheme != "https":
		note("%sBASE_URL must be http or https, got %q", Prefix, base.Scheme)
	case base.Host == "":
		note("%sBASE_URL has no host", Prefix)
	default:
		// A trailing slash would produce doubled separators wherever this is
		// joined with a path, so it is normalised away once, here.
		base.Path = strings.TrimSuffix(base.Path, "/")
		cfg.BaseURL = base
	}

	if servers := get("AUTHORIZATION_SERVERS", ""); servers != "" {
		for _, raw := range strings.Split(servers, ",") {
			issuer := strings.TrimSpace(raw)
			if issuer == "" {
				continue
			}
			u, err := url.Parse(issuer)
			if err != nil || u.Scheme == "" || u.Host == "" {
				note("%sAUTHORIZATION_SERVERS contains %q, which is not an absolute URL", Prefix, issuer)
				continue
			}
			cfg.AuthorizationServers = append(cfg.AuthorizationServers, issuer)
		}
	}

	switch kind := NotesStoreKind(get("NOTES_STORE", string(defaultNotesStore))); kind {
	case NotesStoreMemory, NotesStoreFile:
		cfg.NotesStore = kind
	default:
		note("%sNOTES_STORE must be %q or %q, got %q", Prefix, NotesStoreMemory, NotesStoreFile, kind)
	}

	level, err := parseLevel(get("LOG_LEVEL", defaultLogLevel.String()))
	if err != nil {
		note("%sLOG_LEVEL: %v", Prefix, err)
	} else {
		cfg.LogLevel = level
	}

	if cfg.Addr == "" {
		note("%sADDR must not be empty", Prefix)
	}
	if cfg.UsersFile == "" {
		note("%sUSERS_FILE must not be empty", Prefix)
	}
	if cfg.NotesStore == NotesStoreFile && cfg.NotesFile == "" {
		note("%sNOTES_FILE must be set when %sNOTES_STORE is %q", Prefix, Prefix, NotesStoreFile)
	}

	if len(problems) > 0 {
		return Config{}, fmt.Errorf("config: %w", errors.Join(problems...))
	}
	return cfg, nil
}

// ResourceURL is the identifier this server publishes for itself: the MCP
// endpoint, which is the resource a token must be audienced to.
func (c Config) ResourceURL() string {
	return c.BaseURL.JoinPath(MCPPath).String()
}

// PubliclyAddressed reports whether BaseURL names this server by a host that is
// not loopback — the operator stating that clients reach it from somewhere
// other than this machine.
//
// It exists for one decision, made in the MCP adapter. The SDK refuses a
// request whose Host header is not loopback when the listener is, which is DNS
// rebinding protection for a server nothing but this machine can reach. A
// reverse proxy that terminates TLS and dials 127.0.0.1 trips that check on
// every request, because the Host it forwards is the public name it was asked
// for. BaseURL is where that name is declared, so a non-loopback BaseURL is the
// statement that the check no longer describes this deployment.
func (c Config) PubliclyAddressed() bool {
	if c.BaseURL == nil {
		return false
	}
	return !isLoopbackHost(c.BaseURL.Hostname())
}

// isLoopbackHost reports whether host names the local machine.
func isLoopbackHost(host string) bool {
	if strings.EqualFold(host, "localhost") {
		return true
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}

// Paths the HTTP adapter serves. They live here so that the metadata document
// and the routes it describes cannot drift apart.
const (
	MCPPath                       = "/mcp"
	HealthPath                    = "/healthz"
	ReadyPath                     = "/readyz"
	ProtectedResourceMetadataPath = "/.well-known/oauth-protected-resource"
)

func parseLevel(s string) (slog.Level, error) {
	var level slog.Level
	if err := level.UnmarshalText([]byte(s)); err != nil {
		return 0, fmt.Errorf("must be one of debug, info, warn, error; got %q", s)
	}
	return level, nil
}
