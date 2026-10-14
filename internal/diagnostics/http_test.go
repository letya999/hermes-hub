package diagnostics

import (
	"bytes"
	"log"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func captureHTTP(t *testing.T, jobID string) string {
	t.Helper()
	var buf bytes.Buffer
	prev := log.Writer()
	log.SetOutput(&buf)
	defer log.SetOutput(prev)
	handler := HTTP("supervisor", http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(http.StatusNoContent) }))
	req := httptest.NewRequest(http.MethodGet, "/v1/execute", nil)
	if jobID != "" {
		req.Header.Set("X-Hub-Job-ID", jobID)
	}
	handler.ServeHTTP(httptest.NewRecorder(), req)
	return buf.String()
}

// A valid correlation ID on an inbound request lands in the access log so a
// job is traceable across gateway, supervisor and runtime boundaries.
func TestHTTPLogsJobIDWhenValid(t *testing.T) {
	out := captureHTTP(t, "routine-abc123")
	if !strings.Contains(out, `"job_id":"routine-abc123"`) {
		t.Fatalf("valid job id not logged: %q", out)
	}
	if !strings.Contains(out, `"component":"supervisor"`) || !strings.Contains(out, `"msg":"http"`) {
		t.Fatalf("access log lost: %q", out)
	}
}

// Header injection must never reach the log line: newlines, oversized values
// and malformed IDs are dropped, not escaped into the stream.
func TestHTTPLogDropsInvalidJobID(t *testing.T) {
	for _, bad := range []string{"line\ninjection", strings.Repeat("x", 10*1024), "with space", "UPPER", ""} {
		out := captureHTTP(t, bad)
		if strings.Contains(out, "job_id") {
			t.Fatalf("invalid job id %q leaked into log: %q", bad, out)
		}
		if strings.Contains(out, "\ninjection") || strings.Contains(out, strings.Repeat("x", 100)) {
			t.Fatalf("header bytes leaked into log: %q", out)
		}
	}
}
