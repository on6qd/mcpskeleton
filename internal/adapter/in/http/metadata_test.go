package http_test

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	adapterhttp "github.com/bartdelepeleer/mcpskeleton/internal/adapter/in/http"
	"github.com/bartdelepeleer/mcpskeleton/internal/config"
)

func loadConfig(t *testing.T, vars map[string]string) config.Config {
	t.Helper()
	cfg, err := config.Load(func(name string) string { return vars[name] })
	if err != nil {
		t.Fatalf("config.Load error = %v", err)
	}
	return cfg
}

func TestProtectedResourceMetadata(t *testing.T) {
	t.Parallel()

	cfg := loadConfig(t, map[string]string{"MCPSKELETON_BASE_URL": "https://mcp.example.com"})
	meta := adapterhttp.ProtectedResourceMetadata(cfg, []string{"notes:read", "tools:echo"})

	// The resource identifier is the MCP endpoint: a token must be audienced to
	// it, which is what stops a token minted for another service being replayed
	// here.
	if want := "https://mcp.example.com/mcp"; meta.Resource != want {
		t.Errorf("Resource = %q, want %q", meta.Resource, want)
	}
	if len(meta.ScopesSupported) != 2 {
		t.Errorf("ScopesSupported = %v, want two scopes", meta.ScopesSupported)
	}
	// Credentials in a query string end up in every access log on the way.
	if len(meta.BearerMethodsSupported) != 1 || meta.BearerMethodsSupported[0] != "header" {
		t.Errorf("BearerMethodsSupported = %v, want [header]", meta.BearerMethodsSupported)
	}
	if meta.ResourceName == "" {
		t.Error("ResourceName is empty")
	}
}

// Today there is nowhere to send a client that wants to start a flow, and
// saying so is more honest than inventing an endpoint.
func TestMetadataAdvertisesNoAuthorizationServerByDefault(t *testing.T) {
	t.Parallel()

	cfg := loadConfig(t, nil)
	meta := adapterhttp.ProtectedResourceMetadata(cfg, nil)

	if len(meta.AuthorizationServers) != 0 {
		t.Errorf("AuthorizationServers = %v, want empty", meta.AuthorizationServers)
	}
}

// The swap-proofing constraint, checked rather than asserted: pointing at an
// identity provider is configuration, not a code change.
func TestMetadataAdvertisesConfiguredAuthorizationServers(t *testing.T) {
	t.Parallel()

	cfg := loadConfig(t, map[string]string{
		"MCPSKELETON_AUTHORIZATION_SERVERS": "https://idp.example.com,https://other.example.com",
	})
	meta := adapterhttp.ProtectedResourceMetadata(cfg, nil)

	want := []string{"https://idp.example.com", "https://other.example.com"}
	if len(meta.AuthorizationServers) != len(want) {
		t.Fatalf("AuthorizationServers = %v, want %v", meta.AuthorizationServers, want)
	}
	for i, issuer := range want {
		if meta.AuthorizationServers[i] != issuer {
			t.Errorf("AuthorizationServers[%d] = %q, want %q", i, meta.AuthorizationServers[i], issuer)
		}
	}
}

func TestMetadataHandlerServesTheDocument(t *testing.T) {
	t.Parallel()

	cfg := loadConfig(t, map[string]string{"MCPSKELETON_BASE_URL": "https://mcp.example.com"})
	handler := adapterhttp.MetadataHandler(cfg, []string{"notes:read"})

	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, config.ProtectedResourceMetadataPath, nil))

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusOK)
	}

	var doc map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &doc); err != nil {
		t.Fatalf("the metadata document is not valid JSON: %v\n%s", err, rec.Body.String())
	}
	// The field names are the wire contract from RFC 9728, not ours to rename.
	if doc["resource"] != "https://mcp.example.com/mcp" {
		t.Errorf(`doc["resource"] = %v, want the MCP endpoint`, doc["resource"])
	}
	if _, ok := doc["scopes_supported"]; !ok {
		t.Error("the document has no scopes_supported")
	}
}

// Its whole purpose is to tell a caller who has not authenticated how to do so.
// A 401 pointing at a document that also answers 401 would be a loop.
func TestMetadataHandlerIsUnauthenticated(t *testing.T) {
	t.Parallel()

	cfg := loadConfig(t, nil)
	rec := httptest.NewRecorder()
	adapterhttp.MetadataHandler(cfg, nil).ServeHTTP(rec,
		httptest.NewRequest(http.MethodGet, config.ProtectedResourceMetadataPath, nil))

	if rec.Code != http.StatusOK {
		t.Errorf("status = %d without a credential, want %d", rec.Code, http.StatusOK)
	}
}

// The URL in the WWW-Authenticate header must be where the document actually
// is, or discovery leads nowhere.
func TestMetadataURLMatchesTheServedPath(t *testing.T) {
	t.Parallel()

	cfg := loadConfig(t, map[string]string{"MCPSKELETON_BASE_URL": "https://mcp.example.com"})

	got := adapterhttp.MetadataURL(cfg)
	if want := "https://mcp.example.com" + config.ProtectedResourceMetadataPath; got != want {
		t.Errorf("MetadataURL = %q, want %q", got, want)
	}
	if !strings.HasSuffix(got, config.ProtectedResourceMetadataPath) {
		t.Errorf("MetadataURL %q does not end at the path the document is served from", got)
	}
}
