package http

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/bartdelepeleer/mcpskeleton/internal/config"
	"github.com/bartdelepeleer/mcpskeleton/internal/core"
	"github.com/bartdelepeleer/mcpskeleton/internal/domain"
)

// methodListTools is the MCP method whose result is filtered per caller.
const methodListTools = "tools/list"

// ServerInfo identifies this server to clients.
type ServerInfo struct {
	Name    string
	Version string
}

// MCPHandler serves the MCP endpoint over streamable HTTP.
//
// It must be mounted behind RequireAuth: it reads the caller from the request
// context and has no way to establish one itself.
func MCPHandler(svc *core.Service, cfg config.Config, info ServerInfo, log *slog.Logger) http.Handler {
	if svc == nil {
		panic("http: nil service")
	}
	if log == nil {
		log = slog.New(slog.DiscardHandler)
	}

	// One server, shared by every session. Everything that varies per caller is
	// decided per request instead: which tools are advertised, and whether a
	// call is allowed. That keeps the advertised list current — a token whose
	// scopes changed is reflected on the next request rather than at the next
	// reconnect — and it keeps every tool routable, which is what lets a
	// forbidden call be answered with the scope it needs instead of a
	// misleading "no such tool".
	server := newServer(svc, info, log)

	opts := &mcp.StreamableHTTPOptions{
		// The SDK rejects any request whose Host header is not loopback while
		// the listener is, reasoning that such a server is reachable only from
		// this machine, so a foreign Host can only be DNS rebinding. A reverse
		// proxy that terminates TLS and dials 127.0.0.1 breaks that reasoning:
		// the Host it forwards is the public name clients were told to use, and
		// every legitimate request carries it.
		//
		// Deferring to BaseURL keeps the protection wherever it still describes
		// the deployment — a server left on the default localhost BaseURL is
		// precisely the one the check was written for — and drops it only where
		// the operator has declared another name. Little is given up by
		// dropping it there: /mcp sits behind RequireAuth, and a rebinding
		// attack has no bearer token to send.
		DisableLocalhostProtection: cfg.PubliclyAddressed(),
	}

	return mcp.NewStreamableHTTPHandler(func(r *http.Request) *mcp.Server {
		if _, ok := PrincipalFromContext(r.Context()); !ok {
			// Unreachable behind RequireAuth. Returning nil makes the SDK answer
			// 400, which is the right answer to a request that arrived without
			// the middleware that was supposed to be in front of it.
			log.LogAttrs(r.Context(), slog.LevelError,
				"MCP request reached the handler with no authenticated caller")
			return nil
		}
		return server
	}, opts)
}

// newServer builds the MCP server, carrying every tool.
func newServer(svc *core.Service, info ServerInfo, log *slog.Logger) *mcp.Server {
	server := mcp.NewServer(&mcp.Implementation{
		Name:    info.Name,
		Version: info.Version,
	}, nil)

	for _, tool := range svc.AllTools() {
		server.AddTool(&mcp.Tool{
			Name:        tool.Name,
			Description: tool.Description,
			// The SDK accepts raw JSON here, which is why port.Tool can publish
			// its schema as json.RawMessage and no conversion is needed.
			InputSchema: tool.InputSchema,
		}, toolHandler(svc, tool.Name, log))
	}

	server.AddReceivingMiddleware(filterListedTools(svc, log))

	return server
}

// filterListedTools removes from tools/list the tools the caller may not use.
//
// Filtering the advertised list rather than only the call keeps the model from
// planning around a capability it will be refused. It runs on the result rather
// than at registration so that the answer reflects the token presented on this
// request.
func filterListedTools(svc *core.Service, log *slog.Logger) mcp.Middleware {
	return func(next mcp.MethodHandler) mcp.MethodHandler {
		return func(ctx context.Context, method string, req mcp.Request) (mcp.Result, error) {
			result, err := next(ctx, method, req)
			if err != nil || method != methodListTools {
				return result, err
			}

			listed, ok := result.(*mcp.ListToolsResult)
			if !ok {
				return result, err
			}

			principal, ok := PrincipalFromContext(ctx)
			if !ok {
				// No caller means no tools. Failing closed here matters: this is
				// the only thing standing between an unauthenticated request
				// that somehow reached the server and the full tool list.
				log.LogAttrs(ctx, slog.LevelError, "tools/list with no authenticated caller")
				listed.Tools = nil
				return listed, nil
			}

			allowed := make(map[string]bool)
			for _, tool := range svc.ListTools(principal) {
				allowed[tool.Name] = true
			}

			kept := make([]*mcp.Tool, 0, len(listed.Tools))
			for _, tool := range listed.Tools {
				if allowed[tool.Name] {
					kept = append(kept, tool)
				}
			}
			listed.Tools = kept

			return listed, nil
		}
	}
}

// toolHandler adapts one core tool call to the SDK's handler shape.
func toolHandler(svc *core.Service, name string, log *slog.Logger) mcp.ToolHandler {
	return func(ctx context.Context, req *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		// Authentication runs per request, so this is the token presented now,
		// not the one that opened the session.
		principal, ok := PrincipalFromContext(ctx)
		if !ok {
			log.LogAttrs(ctx, slog.LevelError, "tool call with no authenticated caller",
				slog.String("tool", name))
			return nil, errors.New("unauthenticated")
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

		return &mcp.CallToolResult{
			Content:           []mcp.Content{&mcp.TextContent{Text: outcome.Result.Text}},
			StructuredContent: outcome.Result.Structured,
		}, nil
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
