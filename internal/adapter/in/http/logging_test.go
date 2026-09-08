package http_test

import (
	"encoding/json"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	adapterhttp "github.com/bartdelepeleer/mcpskeleton/internal/adapter/in/http"
	"github.com/bartdelepeleer/mcpskeleton/internal/domain"
)

// logLines captures structured log output as decoded records.
type logLines struct {
	buf *strings.Builder
}

func newLogger() (*slog.Logger, *logLines) {
	buf := &strings.Builder{}
	return slog.New(slog.NewJSONHandler(buf, &slog.HandlerOptions{Level: slog.LevelDebug})), &logLines{buf: buf}
}

func (l *logLines) records(t *testing.T) []map[string]any {
	t.Helper()
	var out []map[string]any
	for _, line := range strings.Split(strings.TrimSpace(l.buf.String()), "\n") {
		if line == "" {
			continue
		}
		var rec map[string]any
		if err := json.Unmarshal([]byte(line), &rec); err != nil {
			t.Fatalf("log line is not JSON: %v\n%s", err, line)
		}
		out = append(out, rec)
	}
	return out
}

func (l *logLines) find(t *testing.T, msg string) map[string]any {
	t.Helper()
	for _, rec := range l.records(t) {
		if rec["msg"] == msg {
			return rec
		}
	}
	t.Fatalf("no log record with msg %q:\n%s", msg, l.buf.String())
	return nil
}

func (l *logLines) raw() string { return l.buf.String() }

func TestRequestLoggerLogsOneLinePerRequest(t *testing.T) {
	t.Parallel()

	log, lines := newLogger()
	handler := adapterhttp.RequestLogger(log)(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusTeapot)
	}))

	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/mcp", nil))

	entry := lines.find(t, "request")
	if entry["method"] != "POST" {
		t.Errorf("method = %v, want POST", entry["method"])
	}
	if entry["path"] != "/mcp" {
		t.Errorf("path = %v, want /mcp", entry["path"])
	}
	if entry["status"] != float64(http.StatusTeapot) {
		t.Errorf("status = %v, want %d", entry["status"], http.StatusTeapot)
	}
	if entry["duration"] == nil {
		t.Error("no duration was logged")
	}
	if entry["request_id"] == "" || entry["request_id"] == nil {
		t.Error("no request id was logged")
	}
}

// A report of "it failed at 14:32" has to be turnable into a log line.
func TestRequestIDIsReturnedToTheClient(t *testing.T) {
	t.Parallel()

	log, lines := newLogger()
	handler := adapterhttp.RequestLogger(log)(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))

	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/healthz", nil))

	header := rec.Header().Get(adapterhttp.RequestIDHeader)
	if header == "" {
		t.Fatal("no request id header on the response")
	}
	if entry := lines.find(t, "request"); entry["request_id"] != header {
		t.Errorf("logged id %v does not match the header %q", entry["request_id"], header)
	}
}

func TestRequestIDsAreDistinct(t *testing.T) {
	t.Parallel()

	log, _ := newLogger()
	handler := adapterhttp.RequestLogger(log)(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))

	seen := make(map[string]bool)
	for range 20 {
		rec := httptest.NewRecorder()
		handler.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/healthz", nil))
		id := rec.Header().Get(adapterhttp.RequestIDHeader)
		if seen[id] {
			t.Fatalf("duplicate request id %q", id)
		}
		seen[id] = true
	}
}

// A server fault deserves a louder level than a client one, so raising the
// level to warn shows only what is actually wrong.
func TestLogLevelFollowsTheStatus(t *testing.T) {
	t.Parallel()

	tests := []struct {
		status int
		want   string
	}{
		{http.StatusOK, "INFO"},
		{http.StatusUnauthorized, "WARN"},
		{http.StatusForbidden, "WARN"},
		{http.StatusInternalServerError, "ERROR"},
	}

	for _, tc := range tests {
		t.Run(http.StatusText(tc.status), func(t *testing.T) {
			t.Parallel()

			log, lines := newLogger()
			handler := adapterhttp.RequestLogger(log)(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.WriteHeader(tc.status)
			}))
			handler.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodGet, "/x", nil))

			if got := lines.find(t, "request")["level"]; got != tc.want {
				t.Errorf("level = %v for status %d, want %v", got, tc.status, tc.want)
			}
		})
	}
}

// A handler that writes a body without calling WriteHeader has still returned
// 200, and the log must say so.
func TestImplicitOKIsLogged(t *testing.T) {
	t.Parallel()

	log, lines := newLogger()
	handler := adapterhttp.RequestLogger(log)(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte("hello"))
	}))
	handler.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodGet, "/x", nil))

	if got := lines.find(t, "request")["status"]; got != float64(http.StatusOK) {
		t.Errorf("status = %v, want 200", got)
	}
}

// The access log names who made the request, which is the field that makes it
// useful for anything but counting.
func TestAuthenticatedRequestsAreLoggedWithTheirSubject(t *testing.T) {
	t.Parallel()

	log, lines := newLogger()
	auth := &fakeAuthenticator{principals: map[string]domain.Principal{
		"good": {Subject: "alice", Scopes: []string{"tools:echo"}},
	}}

	handler := adapterhttp.RequestLogger(log)(
		adapterhttp.RequireAuth(auth, metadataURL, log)(
			http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(http.StatusOK) })))

	req := httptest.NewRequest(http.MethodPost, "/mcp", nil)
	req.Header.Set("Authorization", "Bearer good")
	handler.ServeHTTP(httptest.NewRecorder(), req)

	if got := lines.find(t, "request")["subject"]; got != "alice" {
		t.Errorf("subject = %v, want alice", got)
	}
}

// A rejected request is the one an operator most wants a line for, so the
// logger has to sit outside authentication rather than inside it.
func TestRejectedRequestsAreStillLogged(t *testing.T) {
	t.Parallel()

	log, lines := newLogger()
	auth := &fakeAuthenticator{principals: map[string]domain.Principal{}}

	handler := adapterhttp.RequestLogger(log)(
		adapterhttp.RequireAuth(auth, metadataURL, log)(
			http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(http.StatusOK) })))

	req := httptest.NewRequest(http.MethodPost, "/mcp", nil)
	req.Header.Set("Authorization", "Bearer nope")
	handler.ServeHTTP(httptest.NewRecorder(), req)

	entry := lines.find(t, "request")
	if entry["status"] != float64(http.StatusUnauthorized) {
		t.Errorf("status = %v, want 401", entry["status"])
	}
	// And the rejection line carries the same request id, so the two join up.
	rejection := lines.find(t, "authentication rejected")
	if rejection["request_id"] != entry["request_id"] {
		t.Errorf("the rejection and the request line have different ids: %v vs %v",
			rejection["request_id"], entry["request_id"])
	}
}

// The single rule that matters most: a credential must never reach a log file,
// however it arrived.
func TestCredentialsAreNeverLogged(t *testing.T) {
	t.Parallel()

	const secret = "mcps_supersecrettokenvalue"

	log, lines := newLogger()
	auth := &fakeAuthenticator{principals: map[string]domain.Principal{
		secret: {Subject: "alice"},
	}}

	handler := adapterhttp.RequestLogger(log)(
		adapterhttp.RequireAuth(auth, metadataURL, log)(
			http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(http.StatusOK) })))

	// Presented properly...
	authorized := httptest.NewRequest(http.MethodPost, "/mcp", nil)
	authorized.Header.Set("Authorization", "Bearer "+secret)
	handler.ServeHTTP(httptest.NewRecorder(), authorized)

	// ...and misplaced into a query string, which people do.
	misplaced := httptest.NewRequest(http.MethodPost, "/mcp?access_token="+secret, nil)
	misplaced.Header.Set("Authorization", "Bearer "+secret)
	handler.ServeHTTP(httptest.NewRecorder(), misplaced)

	if strings.Contains(lines.raw(), secret) {
		t.Errorf("a credential appears in the log:\n%s", lines.raw())
	}
	if strings.Contains(lines.raw(), "access_token") {
		t.Errorf("a query string was logged, which is where misplaced credentials end up:\n%s", lines.raw())
	}
}

func TestRequestIDOutsideARequest(t *testing.T) {
	t.Parallel()

	if got := adapterhttp.RequestID(t.Context()); got != "" {
		t.Errorf("RequestID outside a request = %q, want empty", got)
	}
}
