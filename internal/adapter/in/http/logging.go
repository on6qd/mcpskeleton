package http

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"log/slog"
	"net/http"
	"time"
)

// RequestIDHeader carries the request id back to the client, so a report of
// "it failed at 14:32" can be turned into a log line.
const RequestIDHeader = "X-Request-Id"

type contextKey int

const (
	requestIDKey contextKey = iota
	callerKey
)

// caller is a mutable holder for the subject a request turns out to belong to.
//
// It exists because the logger has to wrap the authenticator to log rejections
// at all, yet the subject is only known inside it. A context value set by an
// inner middleware cannot be seen by an outer one, so the outer logger puts
// this pointer in first and authentication fills it in.
type caller struct{ subject string }

// RequestLogger logs one line per request: what was asked for, who asked, what
// happened and how long it took.
//
// Nothing about credentials is logged. Headers are never recorded, so an
// Authorization header cannot leak, and only the path is taken from the URL so
// a token mistakenly put in a query string does not end up in the log either.
func RequestLogger(log *slog.Logger) func(http.Handler) http.Handler {
	if log == nil {
		log = slog.New(slog.DiscardHandler)
	}

	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			start := time.Now()

			id := newRequestID()
			who := &caller{}
			ctx := context.WithValue(r.Context(), requestIDKey, id)
			ctx = context.WithValue(ctx, callerKey, who)

			w.Header().Set(RequestIDHeader, id)
			recorder := &statusRecorder{ResponseWriter: w, status: http.StatusOK}

			next.ServeHTTP(recorder, r.WithContext(ctx))

			// A server fault deserves a louder level than a client one, so that
			// turning the level up to warn shows only what is actually wrong.
			level := slog.LevelInfo
			switch {
			case recorder.status >= 500:
				level = slog.LevelError
			case recorder.status >= 400:
				level = slog.LevelWarn
			}

			log.LogAttrs(ctx, level, "request",
				slog.String("request_id", id),
				slog.String("method", r.Method),
				slog.String("path", r.URL.Path),
				slog.Int("status", recorder.status),
				slog.String("subject", who.subject),
				slog.Duration("duration", time.Since(start)),
			)
		})
	}
}

// RequestID returns the id assigned to this request, or "" outside one.
func RequestID(ctx context.Context) string {
	id, _ := ctx.Value(requestIDKey).(string)
	return id
}

// recordSubject notes who a request turned out to belong to, so the access log
// line can name them. It does nothing outside a logged request.
func recordSubject(ctx context.Context, subject string) {
	if who, ok := ctx.Value(callerKey).(*caller); ok {
		who.subject = subject
	}
}

// statusRecorder remembers the status code, which net/http otherwise does not
// expose once written.
type statusRecorder struct {
	http.ResponseWriter
	status  int
	written bool
}

func (r *statusRecorder) WriteHeader(status int) {
	if !r.written {
		r.status = status
		r.written = true
	}
	r.ResponseWriter.WriteHeader(status)
}

func (r *statusRecorder) Write(b []byte) (int, error) {
	r.written = true
	return r.ResponseWriter.Write(b)
}

// Flush keeps server-sent events working. Without it the streamable transport's
// events would sit in a buffer until the response ended, which for a stream is
// forever.
func (r *statusRecorder) Flush() {
	if f, ok := r.ResponseWriter.(http.Flusher); ok {
		f.Flush()
	}
}

// Unwrap lets net/http reach the real ResponseWriter for anything this type
// does not implement, such as hijacking.
func (r *statusRecorder) Unwrap() http.ResponseWriter { return r.ResponseWriter }

func newRequestID() string {
	var b [8]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "unknown"
	}
	return hex.EncodeToString(b[:])
}
