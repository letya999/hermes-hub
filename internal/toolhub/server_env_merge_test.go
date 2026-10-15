package toolhub

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"encoding/json"

	"github.com/letya999/hermes-hub/internal/identity"
)

// EndpointConfigFromEnv validates the token env name, the principal and the
// managed capability identity before any listener exists; durationEnv honors
// Go durations and bare seconds, and the auxiliary token file maps long
// tokens to validated envelopes.
func TestEndpointConfigFromEnv(t *testing.T) {
	for _, key := range []string{"HUB_TOOLHUB_TOKEN_ENV", "HUB_RUNTIME_AUTH", "HUB_USER_ID", "HUB_CONTEXT_ID", "HUB_RUNTIME_ID", "HUB_POLICY_VERSION", "HUB_CAPABILITY_MODE", "HUB_CAPABILITY_GENERATION", "HUB_CAPABILITY_PROFILE_ID", "HUB_CAPABILITY_ENVIRONMENT", "HUB_CREDENTIAL_BROKER_CONTROL_URL", "HUB_CREDENTIAL_BROKER_RUNTIME_URL", "HUB_AGENT_EXEC_CONTAINER", "HUB_AGENT_EXEC_MODE", "HUB_TOOLHUB_STORE", "HUB_DOCKER_SCOPE", "HUB_DOCKER_AGENT_NET"} {
		t.Setenv(key, "")
	}

	if _, err := EndpointConfigFromEnv(); err == nil || !strings.Contains(err.Error(), "empty") {
		t.Fatalf("missing token accepted: %v", err)
	}
	t.Setenv("HUB_RUNTIME_AUTH", strings.Repeat("a", 32))
	if _, err := EndpointConfigFromEnv(); err == nil || !strings.Contains(err.Error(), "HUB_USER_ID") {
		t.Fatalf("missing principal accepted: %v", err)
	}
	t.Setenv("HUB_USER_ID", "alice")
	cfg, err := EndpointConfigFromEnv()
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Auth.PrincipalID != "alice" || cfg.Auth.ContextID != "alice" || cfg.Auth.PolicyVersion != "policy-1" || cfg.Listen == "" {
		t.Fatalf("endpoint config: %+v", cfg)
	}
	t.Setenv("HUB_CAPABILITY_MODE", "bogus")
	if _, err := EndpointConfigFromEnv(); err == nil {
		t.Fatal("bad capability mode accepted")
	}
	t.Setenv("HUB_CAPABILITY_MODE", "managed")
	if _, err := EndpointConfigFromEnv(); err == nil || !strings.Contains(err.Error(), "incomplete") {
		t.Fatalf("incomplete managed identity accepted: %v", err)
	}
	t.Setenv("HUB_CAPABILITY_PROFILE_ID", "profile-1")
	t.Setenv("HUB_CAPABILITY_ENVIRONMENT", "dev")
	t.Setenv("HUB_CAPABILITY_GENERATION", "7")
	cfg, err = EndpointConfigFromEnv()
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Auth.Generation != 7 || cfg.Auth.CapabilityProfile != "profile-1" || cfg.Auth.Environment != "dev" {
		t.Fatalf("managed identity: %+v", cfg.Auth)
	}

	// The auxiliary token file maps long tokens to validated envelopes.
	dir := t.TempDir()
	path := filepath.Join(dir, "tokens.json")
	if _, err := loadTokenEnvelopes(filepath.Join(dir, "missing.json")); err == nil {
		t.Fatal("missing tokens file loaded")
	}
	if err := os.WriteFile(path, []byte(`{"short":{"principal_id":"a"}}`), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := loadTokenEnvelopes(path); err == nil {
		t.Fatal("short token accepted")
	}
	env := identity.TelegramEnvelope("alice", 42, "alice", "policy-1")
	body, _ := json.Marshal(map[string]identity.Envelope{strings.Repeat("t", 40): env})
	if err := os.WriteFile(path, body, 0600); err != nil {
		t.Fatal(err)
	}
	entries, err := loadTokenEnvelopes(path)
	if err != nil || len(entries) != 1 {
		t.Fatalf("token envelopes: %v %v", entries, err)
	}
}
