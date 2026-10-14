package runtime

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/letya999/hermes-hub/internal/agenttools"
	"github.com/letya999/hermes-hub/internal/stack"
	"gopkg.in/yaml.v3"
)

// Tool surface probing is observation, never authorization: the rendered
// manifest is the operator-computed expectation and the ToolHub connector
// view answers with the caller's own envelope. SPEC-0053 R1.4-R1.6.

type surfaceManifest struct {
	Projected []string `json:"projected"`
	Direct    []string `json:"direct"`
}

type surfaceConfig struct {
	Model struct {
		Default string `yaml:"default"`
		BaseURL string `yaml:"base_url"`
		APIKey  string `yaml:"api_key"`
	} `yaml:"model"`
	MCPServers map[string]struct {
		Command string            `yaml:"command"`
		Args    []string          `yaml:"args"`
		URL     string            `yaml:"url"`
		Env     map[string]string `yaml:"env"`
		Headers map[string]string `yaml:"headers"`
	} `yaml:"mcp_servers"`
}

type connectorView struct {
	DefinitionID string   `json:"definition_id"`
	Tools        []string `json:"tools"`
}

// probeResult is one readiness observation. surface is ok|degraded|not_probed,
// missing carries manifest names nothing serves, reasons name the cause per
// surface (never secrets or payloads), model is ok|model_not_found|
// unavailable|not_configured.
type probeResult struct {
	surface string
	missing []string
	reasons map[string]string
	model   string
}

// surfaceProbe is replaceable in tests; the default walks the same files and
// HTTP endpoints the live runtime serves.
var surfaceProbe = defaultSurfaceProbe

var errSurfaceUnavailable = errors.New("tool surface unavailable")

func defaultSurfaceProbe(ctx context.Context) probeResult {
	result := probeResult{surface: "ok", reasons: map[string]string{}, model: "not_configured"}
	config := readSurfaceConfig(filepath.Join(env("HERMES_HOME", filepath.Join(state, "hermes")), "config.yaml"))
	result.model = probeModel(ctx, config)
	views, toolsErr := probeConnectors(ctx, config)

	manifest, manifestErr := readSurfaceManifest(env("HUB_TOOL_MANIFEST", filepath.Join(state, "tool-manifest.json")))
	if manifestErr != nil || manifest == nil {
		result.surface = "not_probed"
		return result
	}
	served := map[string]bool{}
	if toolsErr != nil {
		result.surface = "degraded"
		result.reasons["toolhub"] = "connectors_unavailable"
	}
	for _, view := range views {
		if view.DefinitionID == agenttools.HubToolsDefinitionID {
			for _, family := range servedAgentFamilies(view.Tools) {
				served[family] = true
			}
			continue
		}
		served[normalizeConnectorName(view.DefinitionID)] = true
	}
	if missingEnvs := unresolvedHubEnv(config); len(missingEnvs) > 0 {
		result.surface = "degraded"
		result.reasons["hub"] = "child_env_unresolved:" + strings.Join(missingEnvs, ",")
	} else if reason := hubChildCheck(config); reason != "" {
		result.surface = "degraded"
		result.reasons["hub"] = reason
	}
	for _, name := range manifest.Projected {
		if !servedSurfaceName(served, name) {
			result.missing = append(result.missing, name)
			result.surface = "degraded"
		}
	}
	return result
}

func readSurfaceManifest(path string) (*surfaceManifest, error) {
	body, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var manifest surfaceManifest
	if err := json.Unmarshal(body, &manifest); err != nil {
		return nil, err
	}
	return &manifest, nil
}

func readSurfaceConfig(path string) surfaceConfig {
	var config surfaceConfig
	if body, err := os.ReadFile(path); err == nil {
		_ = yaml.Unmarshal(body, &config)
	}
	return config
}

// servedAgentFamilies maps agent-tools catalog names back to their capability
// families so a manifest name like "terminal" is covered by code_exec.
func servedAgentFamilies(tools []string) []string {
	byName := map[string]string{}
	for _, spec := range agenttools.HubToolsDefinition().Tools {
		family, _, _ := strings.Cut(spec.CapabilityID, ".")
		if family != "" {
			byName[spec.Name] = family
		}
	}
	families := map[string]bool{}
	for _, name := range tools {
		if family, ok := byName[name]; ok {
			families[family] = true
		}
	}
	out := make([]string, 0, len(families))
	for family := range families {
		out = append(out, family)
	}
	return out
}

// normalizeConnectorName folds operator-chosen definition ids
// (gitlab-mcp, github-mcp-server, mcp-atlassian) down to the capability
// family the settings vocabulary uses.
func normalizeConnectorName(id string) string {
	name := strings.ToLower(strings.TrimSpace(id))
	name = strings.TrimPrefix(name, "mcp-")
	for _, suffix := range []string{"-mcp-server", "-mcp", "-server"} {
		name = strings.TrimSuffix(name, suffix)
	}
	return strings.ReplaceAll(name, "-", "_")
}

func servedSurfaceName(served map[string]bool, name string) bool {
	if family := stack.ToolHubCapabilityID(name); family != "" && served[family] {
		return true
	}
	normalized := normalizeConnectorName(name)
	for id := range served {
		if id == normalized || strings.HasPrefix(id, normalized+"_") {
			return true
		}
	}
	return false
}

var surfaceEnvName = regexp.MustCompile(`^[A-Z][A-Z0-9_]{0,63}$`)

// unresolvedHubEnv lists the ${VAR} references inside mcp_servers.hub.env that
// resolve to empty. Hub-owned (HUB_*) vars are the identity/plumbing contract
// — an unset one kills the stdio child silently. Optional pieces stay exempt:
// secrets never carry the HUB_ prefix, media/broker env may be off, and
// HUB_DEFER_* is set only inside tools-exec children.
func unresolvedHubEnv(config surfaceConfig) []string {
	hub, ok := config.MCPServers["hub"]
	if !ok {
		return nil
	}
	missing := map[string]bool{}
	for _, value := range hub.Env {
		name := envReference(value)
		if name == "" || os.Getenv(name) != "" {
			continue
		}
		if !strings.HasPrefix(name, "HUB_") || strings.HasPrefix(name, "HUB_DEFER") || strings.HasPrefix(name, "HUB_MEDIA") || strings.HasPrefix(name, "HUB_CREDENTIAL_BROKER") {
			continue
		}
		missing[name] = true
	}
	out := make([]string, 0, len(missing))
	for name := range missing {
		out = append(out, name)
	}
	slices.Sort(out)
	return out
}

func envReference(value string) string {
	value = strings.TrimSpace(value)
	if strings.HasPrefix(value, "${") && strings.HasSuffix(value, "}") && len(value) > 3 {
		if name := value[2 : len(value)-1]; surfaceEnvName.MatchString(name) {
			return name
		}
	}
	return ""
}

// hubChildResults memoizes the spawn self-test per rendered command: it is a
// startup proof (R1.5), so one spawn per process per command signature, not
// one fork per health tick.
var hubChildResults sync.Map

// hubChildCheck proves the hub stdio child actually starts — the failure
// classes env resolution cannot see (missing or broken binary). It runs only
// when the env check passed; unresolved plumbing already names the cause more
// precisely than a spawn error would.
func hubChildCheck(config surfaceConfig) string {
	hub, ok := config.MCPServers["hub"]
	if !ok || strings.TrimSpace(hub.Command) == "" {
		return ""
	}
	key := hub.Command + "\x00" + strings.Join(hub.Args, "\x00")
	if cached, ok := hubChildResults.Load(key); ok {
		return cached.(string)
	}
	reason := spawnHubChild(hub.Command, hub.Args)
	hubChildResults.Store(key, reason)
	return reason
}

// spawnHubChild starts the configured child; "" means it stayed up past the
// grace window like a stdio server awaiting input, a short reason names the
// failure stage.
func spawnHubChild(name string, args []string) string {
	cmd := exec.Command(name, args...)
	if err := cmd.Start(); err != nil {
		return "child_spawn_failed"
	}
	// A live child stays up waiting on stdio; one that exits inside the grace
	// window died on init (unresolved env, missing store, crash) — the same
	// signature the pinned loader surfaces as a silent disconnect. The window
	// is generous because a dying child still has to boot first; this runs
	// once per process per command signature, not per health tick.
	done := make(chan error, 1)
	go func() { done <- cmd.Wait() }()
	select {
	case <-done:
		return "child_init_failed"
	case <-time.After(1500 * time.Millisecond):
	}
	_ = cmd.Process.Kill()
	<-done
	return ""
}

// probeConnectors lists the caller's own projected tools through the same
// endpoint Hermes uses; a ToolHub outage degrades the surface report instead
// of blocking health.
func probeConnectors(ctx context.Context, config surfaceConfig) ([]connectorView, error) {
	server, configured := config.MCPServers["toolhub"]
	endpoint := server.URL
	if endpoint == "" {
		endpoint = toolHubEndpoint()
	}
	tokenEnv := strings.TrimSpace(os.Getenv("HUB_TOOLHUB_TOKEN_ENV"))
	if tokenEnv == "" && configured {
		tokenEnv = envReference(strings.TrimPrefix(server.Headers["Authorization"], "Bearer "))
	}
	if tokenEnv == "" {
		tokenEnv = "HUB_RUNTIME_AUTH"
	}
	token := os.Getenv(tokenEnv)
	if endpoint == "" || token == "" {
		return nil, nil
	}
	base, err := url.Parse(endpoint)
	if err != nil || base.Host == "" {
		return nil, nil
	}
	base.Path, base.RawQuery, base.Fragment = "/v1/connectors", "", ""
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, base.String(), nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+token)
	client := &http.Client{Timeout: 1200 * time.Millisecond, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	response, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		return nil, errSurfaceUnavailable
	}
	var listed struct {
		Connectors []connectorView `json:"connectors"`
	}
	if json.NewDecoder(response.Body).Decode(&listed) != nil {
		return nil, errSurfaceUnavailable
	}
	return listed.Connectors, nil
}

// probeModel checks the configured model against the provider catalog, the
// same contract `hubctl doctor --live` verifies host-side.
func probeModel(ctx context.Context, config surfaceConfig) string {
	model := os.Getenv("HUB_MANAGED_MODEL_ID")
	base := os.Getenv("HUB_MANAGED_MODEL_URL")
	if model == "" {
		model = config.Model.Default
	}
	if base == "" {
		base = resolveEnvValue(config.Model.BaseURL)
	}
	key := os.Getenv("OPENAI_API_KEY")
	if key == "" {
		key = resolveEnvValue(config.Model.APIKey)
	}
	if model == "" || base == "" {
		return "not_configured"
	}
	if key == "" {
		return "unavailable"
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, strings.TrimRight(base, "/")+"/models", nil)
	if err != nil {
		return "unavailable"
	}
	req.Header.Set("Authorization", "Bearer "+key)
	client := &http.Client{Timeout: 1500 * time.Millisecond, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	response, err := client.Do(req)
	if err != nil {
		return "unavailable"
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		return "unavailable"
	}
	var catalog struct {
		Data []struct {
			ID string `json:"id"`
		} `json:"data"`
	}
	if json.NewDecoder(response.Body).Decode(&catalog) != nil {
		return "unavailable"
	}
	for _, entry := range catalog.Data {
		if entry.ID == model {
			return "ok"
		}
	}
	return "model_not_found"
}

func resolveEnvValue(value string) string {
	if name := envReference(value); name != "" {
		return os.Getenv(name)
	}
	return strings.TrimSpace(value)
}
