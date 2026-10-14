package diagnostics

import (
	"net/http"
	"strings"
	"time"

	"github.com/letya999/hermes-hub/internal/identity"
)

// HTTP records service boundary operations without request bodies, query
// strings, credentials, or unbounded path identifiers — one JSON record per
// request for `hubctl trace` (SPEC-0053 R3.5).
func HTTP(service string, next http.Handler) http.Handler {
	logger := Logger(service)
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()
		jobID := r.Header.Get("X-Hub-Job-ID")
		defer func() {
			fields := []any{"method", r.Method, "route", safeRoute(r.URL.Path), "duration_ms", time.Since(start).Milliseconds()}
			if identity.ValidID(jobID) {
				fields = append(fields, "job_id", jobID)
			}
			logger.Info("http", fields...)
		}()
		next.ServeHTTP(w, r)
	})
}

func safeRoute(path string) string {
	parts := strings.Split(strings.Trim(path, "/"), "/")
	if len(parts) == 0 || parts[0] == "" {
		return "/"
	}
	if parts[0] == "v1" && len(parts) > 1 {
		return "/v1/" + parts[1]
	}
	return "/" + parts[0]
}
