package http

import (
	"context"
	"log/slog"
	"net/http"
)

// ReadyCheck reports whether one dependency is usable. A nil error means ready.
type ReadyCheck func(ctx context.Context) error

// HealthHandler answers whether the process is alive.
//
// It checks nothing. Liveness that consults a dependency is a way to have one
// broken database restart every replica you own; that question is readiness'
// job, and the two are separate for exactly this reason.
func HealthHandler() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		writePlain(w, http.StatusOK, "ok")
	})
}

// ReadyHandler answers whether the process can serve traffic, by running every
// check.
//
// The response says which check failed, because this endpoint is for operators
// and load balancers rather than for callers. It is unauthenticated for the same
// reason a load balancer cannot hold a credential, so checks must not put
// anything sensitive in their error messages.
func ReadyHandler(log *slog.Logger, checks map[string]ReadyCheck) http.Handler {
	if log == nil {
		log = slog.New(slog.DiscardHandler)
	}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		for name, check := range checks {
			if err := check(r.Context()); err != nil {
				log.LogAttrs(r.Context(), slog.LevelWarn, "readiness check failed",
					slog.String("check", name),
					slog.String("error", err.Error()),
				)
				writePlain(w, http.StatusServiceUnavailable, "not ready: "+name)
				return
			}
		}
		writePlain(w, http.StatusOK, "ready")
	})
}

func writePlain(w http.ResponseWriter, status int, body string) {
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	// These answers are a moment in time; a cached one is worse than none.
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(status)
	_, _ = w.Write([]byte(body + "\n"))
}
