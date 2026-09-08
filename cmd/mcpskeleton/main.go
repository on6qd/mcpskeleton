// Command mcpskeleton runs the MCP server and manages its credentials.
package main

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"os/signal"
	"syscall"

	"github.com/bartdelepeleer/mcpskeleton/internal/app"
	"github.com/bartdelepeleer/mcpskeleton/internal/config"
)

func main() {
	// Cancelled on the first SIGINT or SIGTERM, which is what starts a graceful
	// shutdown. A second signal is left to the default handler, so an operator
	// who has lost patience can still kill the process.
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	if err := run(ctx, os.Args[1:], os.Getenv, os.Stdout, os.Stderr); err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)
		os.Exit(1)
	}
}

// run is main without the process, so it can be tested.
func run(ctx context.Context, args []string, getenv config.Getenv, stdout, stderr *os.File) error {
	cfg, err := config.Load(getenv)
	if err != nil {
		return err
	}

	log := slog.New(slog.NewJSONHandler(stderr, &slog.HandlerOptions{Level: cfg.LogLevel}))

	if len(args) == 0 {
		usage(stderr)
		return fmt.Errorf("no command given")
	}

	switch args[0] {
	case "serve":
		return app.Serve(ctx, cfg, log, nil)

	case "help", "-h", "--help":
		usage(stdout)
		return nil

	default:
		usage(stderr)
		return fmt.Errorf("unknown command %q", args[0])
	}
}

func usage(w *os.File) {
	fmt.Fprintf(w, `mcpskeleton — an MCP server over streamable HTTP

Usage:
  mcpskeleton serve      Run the server
  mcpskeleton help       Show this message

Configuration comes from the environment:
  %sADDR                    listen address (default :8080)
  %sBASE_URL                how clients reach this server (default http://localhost:8080)
  %sUSERS_FILE              credentials file (default users.json)
  %sNOTES_STORE             memory or file (default memory)
  %sNOTES_FILE              notes file when NOTES_STORE=file (default notes.json)
  %sLOG_LEVEL               debug, info, warn or error (default info)
  %sAUTHORIZATION_SERVERS   comma-separated issuer URLs (default none)
`, config.Prefix, config.Prefix, config.Prefix, config.Prefix, config.Prefix, config.Prefix, config.Prefix)
}
