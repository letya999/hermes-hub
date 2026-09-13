package toolhub

import (
	"context"
	"strings"
	"testing"

	"github.com/letya999/hermes-hub/internal/identity"
)

func TestEndpointHandlerUsesOneAuthenticatedIdentity(t *testing.T) {
	store, auth, _ := seededStore(t)
	config := EndpointConfig{Token: strings.Repeat("t", 32), Auth: auth, Backend: backendFunc(func(_ context.Context, _ EffectiveBinding, _ ToolSpec, _ map[string]any) (BackendResult, error) {
		return BackendResult{}, nil
	})}
	if _, err := NewEndpointHandler(config, store); err != nil {
		t.Fatal(err)
	}
	bad := config
	bad.Auth = identity.Envelope{Schema: identity.Schema}
	if _, err := NewEndpointHandler(bad, store); err == nil {
		t.Fatal("invalid endpoint identity accepted")
	}
	if _, err := NewEndpointHandler(config, nil); err == nil {
		t.Fatal("nil store accepted")
	}
	bad = config
	bad.Token = "short"
	if _, err := NewEndpointHandler(bad, store); err == nil {
		t.Fatal("short endpoint token accepted")
	}
	bad = config
	bad.Backend = nil
	if _, err := NewEndpointHandler(bad, store); err == nil {
		t.Fatal("nil endpoint backend accepted")
	}
}

func TestEndpointConfigFromEnvUsesRuntimeIdentityDefaults(t *testing.T) {
	t.Setenv("HUB_RUNTIME_AUTH", strings.Repeat("a", 32))
	t.Setenv("HUB_USER_ID", "alice")
	t.Setenv("HUB_CONTEXT_ID", "")
	t.Setenv("HUB_RUNTIME_ID", "")
	t.Setenv("HUB_POLICY_VERSION", "")
	t.Setenv("HUB_TOOLHUB_TOKEN_ENV", "")
	t.Setenv("HUB_TOOLHIVE_ADMISSION_ENDPOINT", "")
	config, err := EndpointConfigFromEnv()
	if err != nil {
		t.Fatal(err)
	}
	if config.Listen != "127.0.0.1:8090" || config.Auth.PrincipalID != "alice" || config.Auth.ContextID != "alice" || config.Auth.RuntimeID != "alice" || config.Backend == nil {
		t.Fatalf("unexpected default endpoint config: %+v", config)
	}
	if _, ok := config.Backend.(RoutingBackend); !ok {
		t.Fatalf("endpoint backend=%T", config.Backend)
	}
	t.Setenv("HUB_TOOLHUB_TOKEN_ENV", "CUSTOM_TOKEN")
	t.Setenv("CUSTOM_TOKEN", strings.Repeat("b", 32))
	t.Setenv("HUB_CONTEXT_ID", "context")
	t.Setenv("HUB_RUNTIME_ID", "runtime")
	t.Setenv("HUB_TOOLHUB_LISTEN", "127.0.0.1:9090")
	config, err = EndpointConfigFromEnv()
	if err != nil || config.Token != strings.Repeat("b", 32) || config.Auth.ContextID != "context" || config.Listen != "127.0.0.1:9090" {
		t.Fatalf("custom endpoint config failed: %+v %v", config, err)
	}
	t.Setenv("HUB_TOOLHUB_TOKEN_ENV", "bad-name")
	if _, err := EndpointConfigFromEnv(); err == nil {
		t.Fatal("invalid token environment name accepted")
	}
}

func TestNonLoopbackListenBoundary(t *testing.T) {
	for address, want := range map[string]bool{
		"127.0.0.1:8090": false,
		"localhost:8090": false,
		":8090":          true,
		"10.0.0.4:8090":  true,
		"toolhub:8090":   true,
		"bad-address":    false,
	} {
		if got := nonLoopbackListen(address); got != want {
			t.Fatalf("nonLoopbackListen(%q)=%v want %v", address, got, want)
		}
	}
}
