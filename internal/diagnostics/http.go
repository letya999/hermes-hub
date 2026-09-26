package diagnostics

import (
	"log"
	"net/http"
	"strings"
	"time"
)

// HTTP records service boundary operations without request bodies, query
// strings, credentials, or unbounded path identifiers.
func HTTP(service string, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()
		defer func() {
			log.Printf("%s http method=%s route=%s duration_ms=%d", service, r.Method, safeRoute(r.URL.Path), time.Since(start).Milliseconds())
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
