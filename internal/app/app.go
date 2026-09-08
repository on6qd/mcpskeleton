// Package app wires the adapters to the core and runs the server.
//
// It is the only package that imports adapters, which is what keeps the
// dependency rule honest: everything below it depends on ports, and the choice
// of which adapter satisfies a port is made here and nowhere else.
package app

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net"
	stdhttp "net/http"
	"time"

	adapterhttp "github.com/bartdelepeleer/mcpskeleton/internal/adapter/in/http"
	"github.com/bartdelepeleer/mcpskeleton/internal/adapter/out/auth/local"
	notesfile "github.com/bartdelepeleer/mcpskeleton/internal/adapter/out/notes/file"
	notesmemory "github.com/bartdelepeleer/mcpskeleton/internal/adapter/out/notes/memory"
	"github.com/bartdelepeleer/mcpskeleton/internal/config"
	"github.com/bartdelepeleer/mcpskeleton/internal/core"
	"github.com/bartdelepeleer/mcpskeleton/internal/port"
	"github.com/bartdelepeleer/mcpskeleton/internal/registry"
)

// Version identifies this build to clients.
const Version = "0.1.0-dev"

// Name is how this server introduces itself over MCP.
const Name = "mcpskeleton"

// shutdownGrace is how long in-flight requests get to finish once a shutdown
// has been asked for.
const shutdownGrace = 15 * time.Second

// Wiring is everything built from a configuration: the HTTP handler, plus the
// pieces the CLI and the tests need to reach directly.
type Wiring struct {
	Handler    stdhttp.Handler
	Users      *local.Store
	NotesStore port.NoteStore
	Registry   *core.Registry
}

// Build assembles the application from cfg.
//
// Every choice of adapter is made here. Reading this function tells you the
// whole shape of the running system.
func Build(cfg config.Config, log *slog.Logger) (*Wiring, error) {
	if log == nil {
		log = slog.New(slog.DiscardHandler)
	}

	users, err := local.NewStore(cfg.UsersFile)
	if err != nil {
		return nil, fmt.Errorf("app: users store: %w", err)
	}
	authenticator := local.NewAuthenticator(users)

	notesStore, err := buildNotesStore(cfg)
	if err != nil {
		return nil, err
	}

	reg := registry.New(notesStore)
	svc := core.NewService(reg, core.ScopeAuthorizer{})

	mux := stdhttp.NewServeMux()

	// The MCP endpoint is the only authenticated route. Everything else exists
	// to be reachable without a credential.
	mux.Handle(config.MCPPath,
		adapterhttp.RequireAuth(authenticator, adapterhttp.MetadataURL(cfg), log)(
			adapterhttp.MCPHandler(svc, adapterhttp.ServerInfo{Name: Name, Version: Version}, log)))

	// Discovery: how a caller who has not authenticated learns where to.
	mux.Handle(config.ProtectedResourceMetadataPath, adapterhttp.MetadataHandler(cfg, reg.Scopes()))

	// Probes: a load balancer cannot hold a credential.
	mux.Handle(config.HealthPath, adapterhttp.HealthHandler())
	mux.Handle(config.ReadyPath, adapterhttp.ReadyHandler(log, map[string]adapterhttp.ReadyCheck{
		// If the users file cannot be read, every request will fail
		// authentication for a reason that is nobody's credential's fault. That
		// is exactly what readiness is for.
		"users": func(context.Context) error {
			_, err := users.Users()
			return err
		},
		// The same for storage the tools depend on.
		"notes": func(ctx context.Context) error {
			_, err := notesStore.List(ctx, "")
			return err
		},
	}))

	return &Wiring{
		Handler:    mux,
		Users:      users,
		NotesStore: notesStore,
		Registry:   reg,
	}, nil
}

func buildNotesStore(cfg config.Config) (port.NoteStore, error) {
	switch cfg.NotesStore {
	case config.NotesStoreMemory:
		return notesmemory.New(), nil
	case config.NotesStoreFile:
		store, err := notesfile.New(cfg.NotesFile)
		if err != nil {
			return nil, fmt.Errorf("app: notes store: %w", err)
		}
		return store, nil
	default:
		// Unreachable: config validates this. Kept so that adding a kind and
		// forgetting to wire it fails loudly rather than silently defaulting.
		return nil, fmt.Errorf("app: unknown notes store %q", cfg.NotesStore)
	}
}

// Serve runs the HTTP server until ctx is cancelled, then shuts down gracefully.
//
// onListen, when set, is called with the address actually bound. That matters
// because a test asks for port 0 and needs to be told what it got.
func Serve(ctx context.Context, cfg config.Config, log *slog.Logger, onListen func(net.Addr)) error {
	if log == nil {
		log = slog.New(slog.DiscardHandler)
	}

	wiring, err := Build(cfg, log)
	if err != nil {
		return err
	}

	listener, err := net.Listen("tcp", cfg.Addr)
	if err != nil {
		return fmt.Errorf("app: listening on %s: %w", cfg.Addr, err)
	}
	if onListen != nil {
		onListen(listener.Addr())
	}

	server := &stdhttp.Server{
		Handler: wiring.Handler,
		// A slow or absent client must not be able to hold a connection open
		// indefinitely. The read header timeout in particular is what closes off
		// a trivial slowloris.
		ReadHeaderTimeout: 10 * time.Second,
		IdleTimeout:       120 * time.Second,
		// There is deliberately no WriteTimeout: MCP's streamable transport
		// holds a response open for server-sent events, and a write deadline
		// would cut long-running streams off mid-flight.
		BaseContext: func(net.Listener) context.Context { return ctx },
	}

	log.LogAttrs(ctx, slog.LevelInfo, "listening",
		slog.String("addr", listener.Addr().String()),
		slog.String("resource", cfg.ResourceURL()),
		slog.String("notes_store", string(cfg.NotesStore)),
		slog.String("users_file", cfg.UsersFile),
	)

	errs := make(chan error, 1)
	go func() {
		if err := server.Serve(listener); err != nil && !errors.Is(err, stdhttp.ErrServerClosed) {
			errs <- err
			return
		}
		errs <- nil
	}()

	select {
	case err := <-errs:
		return err

	case <-ctx.Done():
		log.LogAttrs(context.WithoutCancel(ctx), slog.LevelInfo, "shutting down",
			slog.Duration("grace", shutdownGrace))

		// The shutdown context is deliberately detached from ctx: ctx is already
		// cancelled, and reusing it would abandon in-flight requests instantly
		// rather than giving them the grace period.
		shutdownCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), shutdownGrace)
		defer cancel()

		if err := server.Shutdown(shutdownCtx); err != nil {
			// Report the failure, but still wait for Serve to return so the
			// caller is not left with goroutines running after this returns.
			<-errs
			return fmt.Errorf("app: shutdown: %w", err)
		}
		return <-errs
	}
}
