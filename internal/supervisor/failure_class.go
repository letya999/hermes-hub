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
	stage := "other"
	reason := "other"
	if err != nil {
		lowered := strings.ToLower(err.Error())
		for _, candidate := range []string{"prepare runtime launch", "managed topology", "start runtime", "runtime readiness"} {
			if strings.Contains(lowered, candidate) {
				stage = strings.ReplaceAll(candidate, " ", "_")
				break
			}
		}
		for _, candidate := range []string{
			"unable to find image", "pull access denied", "manifest unknown", "start control relay", "attach control relay", "runtime is not registered", "runtime recovery backoff", "runtime state is not safely replaceable", "runtime start state unavailable", "restored runtime health unavailable", "runtime disappeared during restore", "runtime identity mismatch", "per-principal runtime ceiling reached", "context selected the legacy executor", "managed capability mode requires supervisor execution selection", "context settings unavailable", "context data directory unavailable", "runtime env file is unavailable", "runtime env file escapes context", "runtime input file unavailable", "runtime input file is not an owned regular file", "context path is unavailable", "context path escapes spaces root", "context path is not the bound context", "organization path is unavailable", "organization path escapes spaces root", "platform skills dir", "effective skills view", "materialize tool policy", "managed runtime passport activation changed during spawn", "managed agent network", "managed control network", "owned orphan runtimes", "invalid runtime binding", "invalid runtime mode", "readiness timeout", "permission denied", "operation not permitted", "read-only file system",
		} {
			if strings.Contains(lowered, candidate) {
				reason = strings.ReplaceAll(candidate, " ", "_")
				break
			}
		}
	}
	log.Printf("supervisor runtime unavailable class=%s stage=%s reason=%s status=%d", class, stage, reason, status)
	writeJSON(w, status, map[string]string{"error": "runtime unavailable", "class": class})
}
