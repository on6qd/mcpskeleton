package http

import (
	"net/http"

	sdkauth "github.com/modelcontextprotocol/go-sdk/auth"
	"github.com/modelcontextprotocol/go-sdk/oauthex"

	"github.com/bartdelepeleer/mcpskeleton/internal/config"
)

// ProtectedResourceMetadata builds the RFC 9728 document that tells a client
// what this server is and where to authenticate for it.
//
// The document is assembled from configuration rather than hardcoded, which is
// the third swap-proofing constraint: pointing this server at a real identity
// provider is then a change to AuthorizationServers and an outbound adapter,
// not to any of this.
func ProtectedResourceMetadata(cfg config.Config, scopes []string) *oauthex.ProtectedResourceMetadata {
	return &oauthex.ProtectedResourceMetadata{
		// The resource identifier is the MCP endpoint itself. A token must be
		// audienced to it, which is what stops a token minted for some other
		// service being replayed here.
		Resource: cfg.ResourceURL(),

		// Empty until an identity provider is configured. An empty list is the
		// honest answer today: this server issues its own tokens and runs no
		// authorization server, so there is nowhere to send a client that wants
		// to start a flow.
		AuthorizationServers: cfg.AuthorizationServers,

		// Scope names only. Which tool requires which is not published.
		ScopesSupported: scopes,

		// Tokens travel in the Authorization header and nowhere else. Accepting
		// them in a query string would write credentials into every access log
		// between here and the client.
		BearerMethodsSupported: []string{"header"},

		ResourceName: "mcpskeleton",
	}
}

// MetadataHandler serves the protected-resource metadata document.
//
// It is deliberately unauthenticated. Its whole purpose is to tell a caller who
// has not authenticated yet how to do so, and a 401 pointing at a document that
// also answers 401 would be a loop.
func MetadataHandler(cfg config.Config, scopes []string) http.Handler {
	return sdkauth.ProtectedResourceMetadataHandler(ProtectedResourceMetadata(cfg, scopes))
}

// MetadataURL is the absolute URL of the metadata document, which is what the
// WWW-Authenticate header on a 401 points at.
func MetadataURL(cfg config.Config) string {
	return cfg.BaseURL.JoinPath(config.ProtectedResourceMetadataPath).String()
}
