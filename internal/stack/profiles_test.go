package stack

import (
	"fmt"
	"gopkg.in/yaml.v3"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

func TestProfilesOverlayDefaultGroupOwner(t *testing.T) {
	root := t.TempDir()
	spaces := filepath.Join(root, "spaces")
	for _, user := range []string{"alice", "bob", "carol"} {
		if err := InitEnvironment(filepath.Join(spaces, user), user, "prod"); err != nil {
			t.Fatal(err)
		}
	}
	// carol keeps her own choice: owner fields override every profile layer.
	carol, err := ReadEnvironment(filepath.Join(spaces, "carol"), "prod")
	if err != nil {
		t.Fatal(err)
	}
	carol.Model = "own-model"
	if err := WriteSpace(filepath.Join(spaces, "carol"), carol); err != nil {
		t.Fatal(err)
	}
	// alice and bob carry no model of their own; profiles supply it. alice is
	// the secondary space: dedicated cliproxy is only renderable off-infra.
	noInfra := false
	for _, user := range []string{"alice", "bob"} {
		s, err := ReadEnvironment(filepath.Join(spaces, user), "prod")
		if err != nil {
			t.Fatal(err)
		}
		s.Model, s.ModelURL = "", ""
		if user == "alice" {
			s.Infra = &noInfra
		}
		if err := WriteSpace(filepath.Join(spaces, user), s); err != nil {
			t.Fatal(err)
		}
	}
	saveYAML(t, filepath.Join(spaces, "profiles.yaml"), map[string]any{
		"profiles": map[string]any{
			"default": map[string]any{"model": "shared-model", "model_url": "http://shared-upstream/v1"},
			"power":   map[string]any{"owners": []string{"alice"}, "model": "power-model", "cliproxy": "dedicated"},
		},
	})

	alice, err := ReadEnvironment(filepath.Join(spaces, "alice"), "prod")
	if err != nil {
		t.Fatal(err)
	}
	if alice.Model != "power-model" || alice.Cliproxy != "dedicated" {
		t.Fatalf("named profile did not apply: %+v", alice)
	}
	bob, err := ReadEnvironment(filepath.Join(spaces, "bob"), "prod")
	if err != nil {
		t.Fatal(err)
	}
	if bob.Model != "shared-model" || bob.Cliproxy != "" || bob.ModelURL != "http://shared-upstream/v1" {
		t.Fatalf("default profile did not apply: %+v", bob)
	}
	carol, err = ReadEnvironment(filepath.Join(spaces, "carol"), "prod")
	if err != nil {
		t.Fatal(err)
	}
	if carol.Model != "own-model" {
		t.Fatalf("owner override lost to profile: %+v", carol)
	}
}

func TestValidateCliproxy(t *testing.T) {
	base := func() Settings {
		return Settings{Schema: 3, User: "alice", Timezone: "UTC", Model: "m", ModelURL: "http://model-relay:8318/v1",
			CapabilityMode: "managed", CapabilityProfileID: "p", CapabilityGeneration: 1,
			Environment: "prod", BrowserPort: 6080, OAuthPort: 8000}
	}
	bad := []func(*Settings){
		func(s *Settings) { s.Cliproxy = "bogus" },
		func(s *Settings) {
			infra := true
			s.Infra = &infra
			s.Cliproxy = "dedicated"
		},
	}
	for i, mutate := range bad {
		s := base()
		mutate(&s)
		if err := s.Validate(); err == nil {
			t.Fatalf("case %d accepted", i)
		}
	}
	good := base()
	noInfra := false
	good.Infra = &noInfra
	good.Cliproxy = "dedicated"
	if err := good.Validate(); err != nil {
		t.Fatalf("valid topology rejected: %v", err)
	}
}

func TestSharedProxyRejectsDeclaredPrivateAccounts(t *testing.T) {
	s := Settings{Schema: 1, User: "alice", Environment: "prod", Timezone: "UTC", Model: "m", ModelURL: "https://provider.invalid/v1", BrowserPort: 6080, OAuthPort: 8000}
	if err := s.Validate(); err != nil {
		t.Fatalf("invalid baseline: %v", err)
	}
	body, _ := yaml.Marshal(s)
	body = append(body, []byte("cliproxy: shared\ncliproxy_credentials: private\n")...)
	if err := yaml.Unmarshal(body, &s); err != nil {
		t.Fatal(err)
	}
	if err := s.Validate(); err == nil {
		t.Error("shared proxy accepted private accounts without proven identity routing")
	}
	noInfra := false
	s.Infra = &noInfra
	s.Cliproxy = "dedicated"
	if err := s.Validate(); err != nil {
		t.Fatalf("isolated private proxy rejected: %v", err)
	}
	s.CliproxyCredentials = "bogus"
	if err := s.Validate(); err == nil {
		t.Error("unknown credential scope accepted")
	}
}

func TestProxyCredentialScopeFollowsProfilesAndOwnerOverride(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "spaces", "alice")
	if err := os.MkdirAll(dir, 0700); err != nil {
		t.Fatal(err)
	}
	saveYAML(t, filepath.Join(filepath.Dir(dir), "profiles.yaml"), map[string]any{"profiles": map[string]any{
		"default": map[string]any{"cliproxy": "shared", "cliproxy_credentials": "shared"},
		"private": map[string]any{"owners": []string{"alice"}, "cliproxy": "dedicated", "cliproxy_credentials": "private"},
	}})
	alice := Settings{User: "alice"}
	if err := applyProfiles(&alice, dir); err != nil {
		t.Fatal(err)
	}
	if alice.Cliproxy != "dedicated" || alice.CliproxyCredentials != "private" {
		t.Error("group lost its independent credential scope")
	}
	bob := Settings{User: "bob"}
	if err := applyProfiles(&bob, dir); err != nil {
		t.Fatal(err)
	}
	if bob.Cliproxy != "shared" || bob.CliproxyCredentials != "shared" {
		t.Error("another owner inherited a private account profile")
	}
	owner := Settings{User: "alice", Cliproxy: "shared", CliproxyCredentials: "shared"}
	if err := applyProfiles(&owner, dir); err != nil {
		t.Fatal(err)
	}
	if owner.CliproxyCredentials != "shared" {
		t.Error("group replaced explicit owner credential scope")
	}
}

func TestModelRelayRendersRegistry(t *testing.T) {
	s := Settings{Schema: 1, Environment: "prod", User: "alice", Timezone: "UTC", BrowserPort: 6080, OAuthPort: 8000,
		CapabilityMode: "managed", CapabilityProfileID: "p", CapabilityGeneration: 1,
		Model: "m", ModelURL: "http://model-relay:8318/v1"}
	relay := Compose(s, "/source", "/space")["services"].(M)["model-relay"].(M)
	env := relay["environment"].(M)
	if env["HUB_MODEL_RELAY_REGISTRY"] != "/run/model-config/models.yaml" || env["HUB_MODEL_RELAY_OWNER"] != "alice" {
		t.Fatalf("registry env not rendered: %v", env)
	}
	if env["HUB_CREDENTIAL_BROKER_MODEL_KEY_ID"] != "model" || env["HUB_CREDENTIAL_BROKER_MODEL_ISSUER"] != "hermes-model-relay" || env["HUB_CREDENTIAL_BROKER_MODEL_CA_FILE"] != "/run/broker-secrets/server.crt" {
		t.Error("relay lacks its own signed Broker identity")
	}
	brokerMount := false
	for _, raw := range relay["volumes"].([]any) {
		mount := raw.(M)
		if mount["source"] == "broker-secrets-model" && mount["target"] == "/run/broker-secrets" && mount["read_only"] == true {
			brokerMount = true
		}
	}
	if !brokerMount {
		t.Error("relay lacks isolated read-only Broker key volume")
	}
	if _, ok := Compose(s, "/source", "/space")["volumes"].(M)["broker-secrets-model"]; !ok {
		t.Error("model Broker volume not declared")
	}
	// The merged models.yaml mounts read-only; the selection state gets a
	// writable dir so chat-side switching survives relay restarts.
	mounts, _ := relay["volumes"].([]any)
	var regMount, stateMount bool
	for _, m := range mounts {
		mv, _ := m.(M)
		if mv["target"] == "/run/model-config" && mv["read_only"] == true {
			regMount = strings.HasSuffix(fmt.Sprint(mv["source"]), "generated/model-registry/prod")
		}
		if mv["target"] == "/run/model-state" && mv["read_only"] != true {
			stateMount = strings.Contains(fmt.Sprint(mv["source"]), "managed/prod/model-relay")
		}
	}
	if !regMount || !stateMount {
		t.Fatalf("registry mounts missing: %v", mounts)
	}
	// Hop key_env names resolve from the space's own secrets env file; the
	// admin bearer comes from model-relay.auth shared with the gateway.
	var secretsFile, authFile bool
	files, _ := relay["env_file"].([]any)
	for _, f := range files {
		if m, ok := f.(M); ok {
			if strings.HasSuffix(fmt.Sprint(m["path"]), "runtime.prod.env") {
				secretsFile = true
			}
			if strings.HasSuffix(fmt.Sprint(m["path"]), "model-relay.auth") {
				authFile = true
			}
		}
	}
	if !secretsFile || !authFile {
		t.Fatalf("model-relay env files missing: %v", files)
	}
}

func TestDedicatedCliproxyRenderedForSecondarySpace(t *testing.T) {
	noInfra := false
	s := Settings{Schema: 1, Environment: "prod", User: "alice", Timezone: "UTC", BrowserPort: 6080, OAuthPort: 8000, Infra: &noInfra,
		CapabilityMode: "managed", CapabilityProfileID: "p", CapabilityGeneration: 1,
		Model: "m", ModelURL: "http://model-relay:8318/v1", Cliproxy: "dedicated"}
	services := Compose(s, "/source", "/space")["services"].(M)
	cliproxy, ok := services["cliproxy"].(M)
	if !ok {
		t.Fatal("dedicated cliproxy was not rendered")
	}
	// Its alias carries the owner id on both networks so the space-local
	// "cliproxy" name can never shadow the shared infra instance.
	for _, net := range []string{"default", sharedNetworkName} {
		entry, ok := cliproxy["networks"].(M)[net].(M)
		if !ok || !slices.Contains(entry["aliases"].([]string), "cliproxy-alice") {
			t.Fatalf("dedicated cliproxy misses alias on %s: %v", net, cliproxy["networks"])
		}
	}
	env := services["model-relay"].(M)["environment"].(M)
	if env["HUB_MODEL_RELAY_CLIPROXY"] != "http://cliproxy-alice:8317" {
		t.Fatalf("relay does not point at the dedicated proxy: %v", env["HUB_MODEL_RELAY_CLIPROXY"])
	}
}

func TestModelRelaySharedAliasAndGatewayWiring(t *testing.T) {
	s := Settings{Schema: 1, Environment: "prod", User: "alice", Timezone: "UTC", BrowserPort: 6080, OAuthPort: 8000,
		CapabilityMode: "managed", CapabilityProfileID: "p", CapabilityGeneration: 1,
		ExecutionMode: "supervisor", SupervisorURL: "http://supervisor:8876",
		Model: "m", ModelURL: "http://model-relay:8318/v1", Ingress: []string{"telegram"}}
	services := Compose(s, "/source", "/space")["services"].(M)
	relay := services["model-relay"].(M)
	shared, ok := relay["networks"].(M)[sharedNetworkName].(M)
	if !ok || !slices.Contains(shared["aliases"].([]string), "model-relay-alice") {
		t.Fatalf("relay misses the per-space shared alias: %v", relay["networks"])
	}
	gateway := services["communication-hub"].(M)
	env := gateway["environment"].(M)
	if env["HUB_MODEL_RELAY_URL"] != "http://model-relay-alice:8318" {
		t.Fatalf("gateway cannot reach the owner relay: %v", env)
	}
	var authMounted bool
	for _, f := range gateway["env_file"].([]any) {
		if strings.HasSuffix(fmt.Sprint(f.(M)["path"]), "model-relay.auth") {
			authMounted = true
		}
	}
	if !authMounted {
		t.Fatal("gateway lacks the model-relay bearer")
	}
}
