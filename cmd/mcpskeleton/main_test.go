package main

import (
	"context"
	"os"
	"strings"
	"testing"
	"time"
)

// devNull stands in for stdout and stderr so command output does not pollute
// the test log.
func devNull(t *testing.T) *os.File {
	t.Helper()
	f, err := os.OpenFile(os.DevNull, os.O_WRONLY, 0)
	if err != nil {
		t.Fatalf("opening %s: %v", os.DevNull, err)
	}
	t.Cleanup(func() { _ = f.Close() })
	return f
}

func TestRunRejectsUnknownCommands(t *testing.T) {
	t.Parallel()

	null := devNull(t)
	env := func(string) string { return "" }

	tests := []struct {
		name string
		args []string
		want string
	}{
		{"no command", nil, "no command"},
		{"unknown command", []string{"srve"}, "unknown command"},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			err := run(context.Background(), tc.args, env, null, null, time.Now)
			if err == nil {
				t.Fatalf("run(%v) succeeded, want an error", tc.args)
			}
			if !strings.Contains(err.Error(), tc.want) {
				t.Errorf("error = %q, want it to mention %q", err, tc.want)
			}
		})
	}
}

func TestRunHelpSucceeds(t *testing.T) {
	t.Parallel()

	null := devNull(t)
	if err := run(context.Background(), []string{"help"}, func(string) string { return "" }, null, null, time.Now); err != nil {
		t.Errorf("run(help) error = %v", err)
	}
}

// A misconfigured server must refuse to start rather than start and fail later.
func TestRunReportsBadConfiguration(t *testing.T) {
	t.Parallel()

	null := devNull(t)
	env := func(name string) string {
		if name == "MCPSKELETON_LOG_LEVEL" {
			return "verbose"
		}
		return ""
	}

	err := run(context.Background(), []string{"serve"}, env, null, null, time.Now)
	if err == nil {
		t.Fatal("run(serve) succeeded with an invalid log level")
	}
	if !strings.Contains(err.Error(), "LOG_LEVEL") {
		t.Errorf("error = %q, want it to name the bad variable", err)
	}
}

// Cancelling the context must bring the server down rather than hang.
func TestServeStopsOnCancellation(t *testing.T) {
	t.Parallel()

	null := devNull(t)
	dir := t.TempDir()
	env := func(name string) string {
		switch name {
		case "MCPSKELETON_ADDR":
			return "127.0.0.1:0"
		case "MCPSKELETON_USERS_FILE":
			return dir + "/users.json"
		}
		return ""
	}

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- run(ctx, []string{"serve"}, env, null, null, time.Now) }()

	cancel()

	select {
	case err := <-done:
		if err != nil {
			t.Errorf("run(serve) error = %v, want nil after cancellation", err)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("run(serve) did not return within 10s of cancellation")
	}
}
