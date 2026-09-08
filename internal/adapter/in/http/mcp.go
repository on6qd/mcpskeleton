package http

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/bartdelepeleer/mcpskeleton/internal/core"
	"github.com/bartdelepeleer/mcpskeleton/internal/domain"
)

// ServerInfo identifies this server to clients.
type ServerInfo struct {
	Name    string
	Version string
}

// MCPHandler serves the MCP endpoint over streamable HTTP.
//
// It must be mounted behind RequireAuth: it reads the caller from the request
// context and has no way to establish one itself.
func MCPHandler(svc *core.Service, info ServerInfo, log *slog.Logger) http.Handler {
	if svc == nil {
		panic("http: nil service")
	}
	if log == nil {
		log = slog.New(slog.DiscardHandler)
	}

	// The SDK calls getServer once per session, after the auth middleware has
	// run, so the caller is known here. Each session therefore gets a server
	// carrying only the tools that caller may use, and tools/list is filtered
	// without a line of listing code.
	//
	// A session's tool list is fixed when the session opens. That is a listing
	// concern only: every tools/call is authorized again, against the principal
	// on that request, so a narrower token presented later cannot call anything
	// the check would refuse.
	return mcp.NewStreamableHTTPHandler(func(r *http.Request) *mcp.Server {
		principal, ok := PrincipalFromContext(r.Context())
		if !ok {
			// Unreachable behind RequireAuth. Returning nil makes the SDK answer
			// 400, which is the right answer to a request that arrived without
			// the middleware that was supposed to be in front of it.
			log.LogAttrs(r.Context(), slog.LevelError,
				"MCP request reached the handler with no authenticated caller")
			return nil
		}
		return newServer(svc, principal, info, log)
	}, nil)
}

// newServer builds the MCP server one session sees.
func newServer(svc *core.Service, sessionPrincipal domain.Principal, info ServerInfo, log *slog.Logger) *mcp.Server {
	server := mcp.NewServer(&mcp.Implementation{
		Name:    info.Name,
		Version: info.Version,
	}, nil)

	for _, tool := range svc.ListTools(sessionPrincipal) {
		server.AddTool(&mcp.Tool{
			Name:        tool.Name,
			Description: tool.Description,
			// The SDK accepts raw JSON here, which is why port.Tool can publish
			// its schema as json.RawMessage and no conversion is needed.
			InputSchema: tool.InputSchema,
		}, toolHandler(svc, tool.Name, sessionPrincipal, log))
	}

	return server
}

// toolHandler adapts one core tool call to the SDK's handler shape.
func toolHandler(svc *core.Service, name string, sessionPrincipal domain.Principal, log *slog.Logger) mcp.ToolHandler {
	return func(ctx context.Context, req *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		// Prefer the caller established on this request: authentication runs
		// per request, so this reflects the token presented now rather than the
		// one that opened the session. The session's principal is the fallback
		// for a transport that does not carry request context this far.
		principal, ok := PrincipalFromContext(ctx)
		if !ok {
			principal = sessionPrincipal
		}

		var args json.RawMessage
		if req != nil && req.Params != nil {
			args = req.Params.Arguments
		}

		outcome, err := svc.CallTool(ctx, principal, name, args)
		if err != nil {
			return nil, protocolError(ctx, err, name, principal, log)
		}

		if outcome.Failure != nil {
			// A tool-level failure the model should read and may retry against.
			// It is a successful protocol exchange carrying an error result,
			// not a protocol error.
			return &mcp.CallToolResult{
				IsError: true,
				Content: []mcp.Content{&mcp.TextContent{Text: outcome.Failure.Message}},
			}, nil
		}

		result := &mcp.CallToolResult{
			Content:           []mcp.Content{&mcp.TextContent{Text: outcome.Result.Text}},
			StructuredContent: outcome.Result.Structured,
		}
		return result, nil
	}
}

// protocolError turns a failure the model cannot act on into an error the
// client sees, without letting internal detail escape.
func protocolError(ctx context.Context, err error, name string, p domain.Principal, log *slog.Logger) error {
	switch {
	case errors.Is(err, domain.ErrForbidden):
		// The caller's own permissions are not a secret from the caller, and
		// naming the missing scope is the difference between a fixable error
		// and a mystery.
		log.LogAttrs(ctx, slog.LevelInfo, "tool call forbidden",
			slog.String("tool", name),
			slog.String("subject", p.Subject),
		)
		return err

	case errors.Is(err, domain.ErrToolNotFound):
		// Reachable when a client calls a tool it was never listed, which is a
		// client bug worth stating plainly.
		log.LogAttrs(ctx, slog.LevelInfo, "unknown tool called",
			slog.String("tool", name),
			slog.String("subject", p.Subject),
		)
		return err

	default:
		// Everything else is this server's fault. The detail goes to the log,
		// where an operator can see it; the client gets a message that says
		// what happened without describing our internals.
		log.LogAttrs(ctx, slog.LevelError, "tool call failed",
			slog.String("tool", name),
			slog.String("subject", p.Subject),
			slog.String("error", err.Error()),
		)
		return errors.New("internal error")
	}
}
