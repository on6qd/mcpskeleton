package http_test

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	adapterhttp "github.com/bartdelepeleer/mcpskeleton/internal/adapter/in/http"
	"github.com/bartdelepeleer/mcpskeleton/internal/config"
)

func TestHealthIsAlwaysOK(t *testing.T) {
	t.Parallel()

	rec := httptest.NewRecorder()
	adapterhttp.HealthHandler().ServeHTTP(rec, httptest.NewRequest(http.MethodGet, config.HealthPath, nil))

	if rec.Code != http.StatusOK {
		t.Errorf("status = %d, want %d", rec.Code, http.StatusOK)
	}
	// A cached liveness answer is worse than none.
	if got := rec.Header().Get("Cache-Control"); got != "no-store" {
		t.Errorf("Cache-Control = %q, want no-store", got)
	}
}

// Liveness that consults a dependency is a way to have one broken database
// restart every replica you own.
func TestHealthDoesNotDependOnAnything(t *testing.T) {
	t.Parallel()

	// There is no way to inject a failing dependency, and that is the assertion.
	rec := httptest.NewRecorder()
	adapterhttp.HealthHandler().ServeHTTP(rec, httptest.NewRequest(http.MethodGet, config.HealthPath, nil))
	if rec.Code != http.StatusOK {
		t.Errorf("status = %d, want %d", rec.Code, http.StatusOK)
	}
}

func TestReadyWithNoChecks(t *testing.T) {
	t.Parallel()

	rec := httptest.NewRecorder()
	adapterhttp.ReadyHandler(nil, nil).ServeHTTP(rec, httptest.NewRequest(http.MethodGet, config.ReadyPath, nil))

	if rec.Code != http.StatusOK {
		t.Errorf("status = %d, want %d", rec.Code, http.StatusOK)
	}
}

func TestReadyRunsEveryCheck(t *testing.T) {
	t.Parallel()

	ran := map[string]bool{}
	checks := map[string]adapterhttp.ReadyCheck{
		"users": func(context.Context) error { ran["users"] = true; return nil },
		"notes": func(context.Context) error { ran["notes"] = true; return nil },
	}

	rec := httptest.NewRecorder()
	adapterhttp.ReadyHandler(nil, checks).ServeHTTP(rec, httptest.NewRequest(http.MethodGet, config.ReadyPath, nil))

	if rec.Code != http.StatusOK {
		t.Errorf("status = %d, want %d", rec.Code, http.StatusOK)
	}
	for name := range checks {
		if !ran[name] {
			t.Errorf("check %q did not run", name)
		}
	}
}

func TestReadyFailsWhenACheckFails(t *testing.T) {
	t.Parallel()

	var buf strings.Builder
	log := slog.New(slog.NewTextHandler(&buf, nil))
	checks := map[string]adapterhttp.ReadyCheck{
		"users": func(context.Context) error { return errors.New("users file is corrupt") },
	}

	rec := httptest.NewRecorder()
	adapterhttp.ReadyHandler(log, checks).ServeHTTP(rec, httptest.NewRequest(http.MethodGet, config.ReadyPath, nil))

	if rec.Code != http.StatusServiceUnavailable {
		t.Errorf("status = %d, want %d", rec.Code, http.StatusServiceUnavailable)
	}
	// An operator reading a probe failure needs to know which dependency broke.
	if !strings.Contains(rec.Body.String(), "users") {
		t.Errorf("body %q does not name the failing check", rec.Body.String())
	}
	if !strings.Contains(buf.String(), "users file is corrupt") {
		t.Errorf("the failure was not logged:\n%s", buf.String())
	}
}

// The endpoint is unauthenticated, because a load balancer cannot hold a
// credential — so a check's error message must not carry anything sensitive
// into the response. The body names the check, never the error.
func TestReadyResponseNamesTheCheckNotTheError(t *testing.T) {
	t.Parallel()

	checks := map[string]adapterhttp.ReadyCheck{
		"users": func(context.Context) error {
			return errors.New("open /etc/secrets/users.json: permission denied")
		},
	}

	rec := httptest.NewRecorder()
	adapterhttp.ReadyHandler(nil, checks).ServeHTTP(rec, httptest.NewRequest(http.MethodGet, config.ReadyPath, nil))

	if strings.Contains(rec.Body.String(), "/etc/secrets") {
		t.Errorf("the response leaked a path from a check's error: %q", rec.Body.String())
	}
}
