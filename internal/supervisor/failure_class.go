package supervisor

import (
	"log"
	"net/http"
	"strings"
)

// Only these fixed classes cross the supervisor boundary; spawn errors may
// contain container output or local paths.
func runtimeFailureClass(err error) string {
	if err == nil {
		return "unavailable"
	}
	s := err.Error()
	switch {
	case strings.Contains(s, "start runtime"), strings.Contains(s, "prepare runtime launch"), strings.Contains(s, "runtime readiness"), strings.Contains(s, "managed topology"):
		return "spawn"
	case strings.Contains(s, "stale-policy"), strings.Contains(s, "policy rotated"), strings.Contains(s, "policy rotation"):
		return "policy_rotation"
	case strings.Contains(s, "administrative hold"):
		return "hold"
	case strings.Contains(s, "backoff"), strings.Contains(s, "crash budget"):
		return "backoff"
	default:
		return "unavailable"
	}
}

func writeRuntimeFailure(w http.ResponseWriter, status int, err error) {
	class := runtimeFailureClass(err)
	log.Printf("supervisor runtime unavailable class=%s status=%d", class, status)
	writeJSON(w, status, map[string]string{"error": "runtime unavailable", "class": class})
}
