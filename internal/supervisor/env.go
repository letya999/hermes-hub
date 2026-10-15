package supervisor

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	hubruntime "github.com/letya999/hermes-hub/internal/runtime"
	"github.com/letya999/hermes-hub/internal/selfsettings"
	"github.com/letya999/hermes-hub/internal/stack"
	"io"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

func (m *Manager) selfEnv(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeJSON(w, http.StatusMethodNotAllowed, map[string]string{"error": "method not allowed"})
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, 128*1024)
	defer r.Body.Close()
	body, err := io.ReadAll(r.Body)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid request"})
		return
	}
	defer clear(body)
	var request hubruntime.SelfEnvRequest
	if json.Unmarshal(body, &request) != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid request"})
		return
	}
	job := hubruntime.ExecuteRequest{Envelope: request.Envelope, OrganizationID: request.OrganizationID, UserID: request.UserID, ActorID: request.ActorID, ScopeID: request.ScopeID, Channel: "communication", Trigger: "credential-form", JobID: "credential-form", IdempotencyKey: "credential-form", Text: "protected credential form"}
	binding, err := m.bindingFor(job)
	if err != nil {
		writeJSON(w, http.StatusConflict, map[string]string{"error": "invalid runtime scope"})
		return
	}
	lease, runtime, err := m.Acquire(r.Context(), binding, LeaseLifecycle)
	if err != nil {
		writeJSON(w, http.StatusServiceUnavailable, map[string]string{"error": "runtime unavailable"})
		return
	}
	defer m.ReleaseLease(lease.ID)
	upstream, err := http.NewRequestWithContext(r.Context(), http.MethodPost, runtime.Address+"/v1/self-env", bytes.NewReader(body))
	if err != nil {
		writeJSON(w, http.StatusBadGateway, map[string]string{"error": "runtime unavailable"})
		return
	}
	upstream.Header.Set("Authorization", "Bearer "+binding.runtimeAuth)
	upstream.Header.Set("Content-Type", "application/json")
	upstream.Header.Set("X-Hub-Job-ID", "credential-form")
	response, err := m.cfg.HTTP.Do(upstream)
	if err != nil {
		writeJSON(w, http.StatusBadGateway, map[string]string{"error": "runtime unavailable"})
		return
	}
	defer response.Body.Close()
	result, err := io.ReadAll(io.LimitReader(response.Body, 128*1024+1))
	if err != nil || len(result) > 128*1024 {
		writeJSON(w, http.StatusBadGateway, map[string]string{"error": "runtime response unavailable"})
		return
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(response.StatusCode)
	_, _ = w.Write(result)
}
func (m *Manager) environment() string {
	if m != nil {
		switch m.cfg.Environment {
		case "dev", "prod":
			return m.cfg.Environment
		}
	}
	return envOr("HUB_ENV", "prod")
}

func envOr(name, fallback string) string {
	if value := os.Getenv(name); value != "" {
		return value
	}
	return fallback
}

// durationEnv reads a Go duration ("130s", "15m") or a bare seconds value.
// Invalid or empty values fall back so a typo cannot disable the cap.
func durationEnv(name string, fallback time.Duration) time.Duration {
	raw := strings.TrimSpace(os.Getenv(name))
	if raw == "" {
		return fallback
	}
	if d, err := time.ParseDuration(raw); err == nil && d > 0 {
		return d
	}
	if seconds, err := strconv.Atoi(raw); err == nil && seconds > 0 {
		return time.Duration(seconds) * time.Second
	}
	return fallback
}

func envFileAuth(path string) (string, error) {
	body, err := os.ReadFile(path)
	if err != nil {
		return "", errors.New("runtime env file is unreadable")
	}
	for _, line := range strings.Split(string(body), "\n") {
		if strings.HasPrefix(line, "HUB_RUNTIME_AUTH=") {
			value := strings.TrimSpace(strings.TrimPrefix(line, "HUB_RUNTIME_AUTH="))
			if value != "" && !strings.ContainsAny(value, "\r\n") {
				return value, nil
			}
		}
	}
	return "", errors.New("HUB_RUNTIME_AUTH missing from runtime env file")
}

// prepareSpawnFiles heals an Init SOUL stub from the repo template and, for
// the infra owner, enrolls sibling runtime tokens before the container mount
// is built. A missing template or a skipped enrollment does not block spawn.
func (m *Manager) prepareSpawnFiles(binding Binding, env string) {
	template := filepath.Join(filepath.Dir(filepath.Dir(binding.ContextRoot)), "config", "SOUL.md")
	if info, err := os.Stat(template); err == nil && info.Mode().IsRegular() {
		if err := stack.HealUserSoul(binding.ContextRoot, template); err != nil {
			log.Printf("supervisor soul heal skipped")
		}
	}
	settings, err := stack.ReadEnvironment(binding.ContextRoot, env)
	if err != nil || !settings.RendersInfra() {
		return
	}
	if err := stack.EnrollSiblingRuntimeTokens(binding.ContextRoot, env); err != nil {
		log.Printf("supervisor token enrollment skipped")
	}
}

// materializeHermesConfig renders the effective Hermes config on the host so
// the spawned runtime mounts it read-only. Inputs mirror the rendered
// runtime contract: secrets.<env>.env endpoint override and self-services.
func (m *Manager) materializeHermesConfig(binding Binding, env string) (string, error) {
	settings := stack.Settings{}
	hasConfig := false
	for _, name := range []string{"settings.yaml", "agent.yaml", "workspace.yaml"} {
		if _, err := os.Stat(filepath.Join(binding.ContextRoot, name)); err == nil {
			hasConfig = true
		} else if !os.IsNotExist(err) {
			return "", err
		}
	}
	if hasConfig {
		var err error
		settings, err = stack.ReadEnvironment(binding.ContextRoot, env)
		if err != nil {
			return "", err
		}
	}
	source := filepath.Join(binding.ContextRoot, "hermes."+env+".yaml")
	dest := filepath.Join(binding.ContextRoot, "generated", "hermes-effective."+env+".yaml")
	envName := "runtime." + env + ".env"
	if settings.CapabilityMode == "managed" {
		envName = "managed-runtime." + env + ".env"
	}
	secrets, _ := stack.ReadSecrets(filepath.Join(binding.ContextRoot, envName))
	tokenEnv := strings.TrimSpace(secrets["HUB_TOOLHUB_TOKEN_ENV"])
	if tokenEnv == "" {
		tokenEnv = "HUB_RUNTIME_AUTH"
	}
	authPresent := strings.TrimSpace(secrets[tokenEnv]) != ""
	envFile := binding.EnvFile
	if envFile == "" {
		envFile = filepath.Join(binding.ContextRoot, "runtime.auth")
	}
	if !authPresent {
		if envSecrets, err := stack.ReadSecrets(envFile); err == nil && strings.TrimSpace(envSecrets[tokenEnv]) != "" {
			authPresent = true
		}
	}
	opts := stack.MaterializeOptions{
		Managed:            settings.CapabilityMode == "managed",
		ToolHubEndpoint:    strings.TrimSpace(secrets["HUB_TOOLHUB_ENDPOINT"]),
		ToolHubTokenEnv:    tokenEnv,
		RuntimeAuthPresent: authPresent,
		ToolHubReconnect:   !strings.EqualFold(strings.TrimSpace(secrets["HUB_TOOLHUB_RECONNECT"]), "false"),
		SelfServicesPath:   filepath.Join(binding.ContextRoot, "runtime", "self-services.json"),
		NativeToolsets:     settings.NativeCarveouts(),
		Web:                settings.Web,
		ImageGen:           settings.ManagedImageGenGrant(),
	}
	if settings.CapabilityMode == "managed" {
		opts.SelfSettingsPath = filepath.Join(binding.ContextRoot, "managed", env, "runtime", selfsettings.FileName)
	}
	if _, present := secrets["HUB_TOOLHUB_ENDPOINT"]; !present {
		opts.ToolHubEndpoint = "http://toolhub:8090/mcp"
	}
	if err := stack.MaterializeHermesConfig(source, dest, opts); err != nil {
		return "", fmt.Errorf("materialize Hermes config: %w", err)
	}
	return dest, nil
}
