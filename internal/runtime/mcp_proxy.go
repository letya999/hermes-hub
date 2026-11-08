package runtime

import (
	"net/http"
	"net/url"
	"os"

	"github.com/letya999/hermes-hub/internal/toolhub/api"
)

// Hermes has static MCP headers. The runtime binds each forwarded request to
// the run it admitted; ToolHub still verifies that claim with the supervisor.
func (s *runtimeHTTP) proxyMCP(w http.ResponseWriter, r *http.Request) {
	if os.Getenv("HUB_CAPABILITY_MODE") != "managed" || !s.authorized(r) {
		writeRuntimeError(w, http.StatusUnauthorized, "unauthorized")
		return
	}
	s.proxyOnce.Do(func() {
		endpoint, err := url.Parse(os.Getenv("HUB_TOOLHUB_ENDPOINT"))
		if err != nil || endpoint.Scheme != "http" || endpoint.Host == "" || endpoint.Path != "/mcp" || endpoint.RawQuery != "" || endpoint.Fragment != "" || endpoint.User != nil {
			s.proxyErr = api.ErrInvalid
			return
		}
		s.proxy, s.proxyErr = toolHubRelayHandler(endpoint.Scheme + "://" + endpoint.Host)
	})
	if s.proxyErr != nil {
		writeRuntimeError(w, http.StatusServiceUnavailable, "ToolHub route unavailable")
		return
	}
	r = r.Clone(r.Context())
	r.URL.Path = "/mcp-run"
	r.Header.Del(api.HeaderJobID)
	r.Header.Del(api.HeaderRunID)
	s.runMu.Lock()
	if s.activeJob != "" && s.activeRun != "" {
		r.Header.Set(api.HeaderJobID, s.activeJob)
		r.Header.Set(api.HeaderRunID, s.activeRun)
	}
	s.runMu.Unlock()
	s.proxy.ServeHTTP(w, r)
}

func (s *runtimeHTTP) releaseRun(jobID, runID, status string) {
	switch status {
	case "completed", "failed", "cancelled", "interrupted":
	default:
		return
	}
	s.runMu.Lock()
	if s.activeJob == jobID && s.activeRun == runID {
		s.activeJob, s.activeRun = "", ""
	}
	s.runMu.Unlock()
}
