package runtime

import (
	"encoding/json"
	"net/http"
	"os"
	"time"
)

// RuntimeHealth contains observations only, never credentials or native details.
// Native platform health does not certify lazy MCP/provider connections.
type RuntimeHealth struct {
	HermesReadiness     string `json:"hermes_readiness"`
	ConnectorHealth     string `json:"connector_health"`
	Connected           int    `json:"connected"`
	Degraded            int    `json:"degraded"`
	Unknown             int    `json:"unknown"`
	ExternalConnections string `json:"external_connections"`
	// SPEC-0053 R1.4-R1.6: the rendered manifest's expectation vs the served
	// surface, names nothing serves, per-surface reasons, and the provider
	// catalog verdict for the configured model. Observation only — a degraded
	// surface never restarts the runtime.
	ToolSurface  string            `json:"tool_surface"`
	MissingTools []string          `json:"missing_tools,omitempty"`
	ToolReasons  map[string]string `json:"tool_reasons,omitempty"`
	ModelStatus  string            `json:"model_status"`
}

func (s *runtimeHTTP) health(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet || !s.authorized(r) {
		writeRuntimeError(w, 401, "unauthorized")
		return
	}
	client := &http.Client{Timeout: 2 * time.Second}
	base := "http://" + env("HUB_HERMES_API_HOST", "127.0.0.1") + ":" + env("HUB_HERMES_API_PORT", "8642")
	var native struct {
		Readiness struct {
			Status string `json:"status"`
		} `json:"readiness"`
		Platforms map[string]struct {
			State  string `json:"state"`
			Status string `json:"status"`
		} `json:"platforms"`
	}
	request, err := http.NewRequestWithContext(r.Context(), http.MethodGet, base+"/health/detailed", nil)
	if err != nil {
		writeRuntimeError(w, 503, "Hermes health unavailable")
		return
	}
	request.Header.Set("Authorization", "Bearer "+env("API_SERVER_KEY", os.Getenv("HUB_RUNTIME_AUTH")))
	response, err := client.Do(request)
	if err != nil {
		writeRuntimeError(w, 503, "Hermes health unavailable")
		return
	}
	defer response.Body.Close()
	if response.StatusCode != 200 || json.NewDecoder(http.MaxBytesReader(w, response.Body, 128*1024)).Decode(&native) != nil || len(native.Platforms) > 64 {
		writeRuntimeError(w, 503, "Hermes health unavailable")
		return
	}
	health := RuntimeHealth{HermesReadiness: "unavailable", ConnectorHealth: "not_configured", ExternalConnections: "not_probed"}
	// Pass Hermes' own verdict through: "degraded" means serving with a
	// non-critical check failed (e.g. disk pressure), not "unavailable" —
	// collapsing it hid real state and made healthy runtimes look broken.
	switch native.Readiness.Status {
	case "ok":
		health.HermesReadiness = "ready"
	case "degraded":
		health.HermesReadiness = "degraded"
	}
	for _, platform := range native.Platforms {
		state := platform.State
		if state == "" {
			state = platform.Status
		}
		switch state {
		case "connected", "running", "ok":
			health.Connected++
		case "disconnected", "error", "failed", "stopped", "degraded":
			health.Degraded++
		case "disabled":
		default:
			health.Unknown++
		}
	}
	if health.Degraded > 0 {
		health.ConnectorHealth = "degraded"
	} else if health.Unknown > 0 {
		health.ConnectorHealth = "unknown"
	} else if health.Connected > 0 {
		health.ConnectorHealth = "healthy"
	}
	surface := surfaceProbe(r.Context())
	health.ToolSurface, health.MissingTools, health.ToolReasons, health.ModelStatus = surface.surface, surface.missing, surface.reasons, surface.model
	// R1.5/R1.6: a degraded surface or a missing configured model is a
	// readiness verdict, not a restart cause — the supervisor remediates only
	// "unavailable", which a degraded probe must never mask.
	if health.HermesReadiness == "ready" && (surface.surface == "degraded" || surface.model == "model_not_found") {
		health.HermesReadiness = "degraded"
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(health)
}
