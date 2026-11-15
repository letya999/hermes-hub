package stack

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"slices"
	"testing"
	"time"

	"github.com/letya999/hermes-hub/internal/memory/memorytest"
	"github.com/letya999/hermes-hub/internal/toolhub/api"
)

// R11.1: the external memory provider is a deny-default capability — an
// explicit tools wish fails closed until a per-space governance grant
// admits native:memory_provider; only then does provider config render.
func TestProviderGrantGated(t *testing.T) {
	d := t.TempDir()
	root := t.TempDir()
	if err := os.Mkdir(filepath.Join(root, "config"), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "config", "SOUL.md"), []byte("soul"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := Init(d, "alice"); err != nil {
		t.Fatal(err)
	}
	publishManagedRevision(t, d, "alice", "r-0000000000000001")
	seedGovernance(t, d, func(doc *api.Governance) {
		activeToolGrant(t, doc, "alice", "native:memory_vault", time.Hour)
	})
	settings, err := ReadEffective(d, "prod")
	if err != nil {
		t.Fatal(err)
	}
	settings.Model = "test"
	settings.ModelURL = "http://model.invalid/v1"
	settings.Tools, settings.Ingress = testTools("workspace", "memory_vault", "memory_provider"), testIngress("workspace")
	settings.Memory = true
	settings.MemoryProvider = "hindsight"
	settings.MemoryProviderURL = "http://hindsight.local:8888"
	if err := saveSpace(t, d, settings); err != nil {
		t.Fatal(err)
	}
	if _, err := ReadEffective(d, "prod"); err == nil {
		t.Fatal("provider wish without a native:memory_provider grant must fail closed")
	}
	seedGovernance(t, d, func(doc *api.Governance) {
		activeToolGrant(t, doc, "alice", "native:memory_vault", time.Hour)
		activeToolGrant(t, doc, "alice", "native:memory_provider", time.Hour)
	})
	if _, err := ReadEffective(d, "prod"); err != nil {
		t.Fatalf("granted provider wish must read: %v", err)
	}
	if err := RenderEnvironment(d, root, "prod"); err != nil {
		t.Fatal(err)
	}
	cfg, err := os.ReadFile(filepath.Join(d, "vault", "runtime", "memory", "provider.json"))
	if err != nil {
		t.Fatalf("granted provider renders no config: %v", err)
	}
	var got struct {
		Provider string `json:"provider"`
		URL      string `json:"url"`
	}
	if err := json.Unmarshal(cfg, &got); err != nil {
		t.Fatalf("provider config is not json: %v", err)
	}
	if got.Provider != "hindsight" || got.URL != "http://hindsight.local:8888" {
		t.Fatalf("provider config drifted: %s", cfg)
	}
}

// R11.1: revoking the capability removes the rendered record — a re-render
// without the wish leaves no provider config that could drive replay or
// egress (deny-wins, no stale material).
func TestProviderDisableRemovesConfig(t *testing.T) {
	d := t.TempDir()
	root := t.TempDir()
	if err := os.Mkdir(filepath.Join(root, "config"), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "config", "SOUL.md"), []byte("soul"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := Init(d, "alice"); err != nil {
		t.Fatal(err)
	}
	publishManagedRevision(t, d, "alice", "r-0000000000000001")
	seedGovernance(t, d, func(doc *api.Governance) {
		activeToolGrant(t, doc, "alice", "native:memory_vault", time.Hour)
		activeToolGrant(t, doc, "alice", "native:memory_provider", time.Hour)
	})
	settings, err := ReadEffective(d, "prod")
	if err != nil {
		t.Fatal(err)
	}
	settings.Model = "test"
	settings.ModelURL = "http://model.invalid/v1"
	settings.Tools, settings.Ingress = testTools("workspace", "memory_vault", "memory_provider"), testIngress("workspace")
	settings.Memory = true
	settings.MemoryProvider = "hindsight"
	settings.MemoryProviderURL = "http://hindsight.local:8888"
	if err := saveSpace(t, d, settings); err != nil {
		t.Fatal(err)
	}
	if err := RenderEnvironment(d, root, "prod"); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(d, "vault", "runtime", "memory", "provider.json")
	if _, err := os.Stat(path); err != nil {
		t.Fatalf("enabled provider must render config: %v", err)
	}
	// Revoke: remove the wish and its fields — the record must vanish.
	settings.Tools = testTools("workspace", "memory_vault")
	settings.MemoryProvider = ""
	settings.MemoryProviderURL = ""
	if err := saveSpace(t, d, settings); err != nil {
		t.Fatal(err)
	}
	if err := RenderEnvironment(d, root, "prod"); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatalf("disabled provider left rendered config behind: %v", err)
	}
}

// R11.1/R11.4: provider config can never be guessed or silently swapped —
// an unknown adapter kind, an orphan config field, a non-HTTP endpoint and
// a credential-typed token field all fail validation outright.
func TestProviderNoSilentFallback(t *testing.T) {
	base := Settings{Schema: 1, Environment: "prod", User: "alice", Timezone: "UTC", BrowserPort: 6080, OAuthPort: 8000,
		Model: "test", ModelURL: "http://model.invalid/v1",
		Tools:   testTools("workspace", "memory_vault", "memory_provider"),
		Ingress: testIngress("workspace")}

	unknown := base
	unknown.MemoryProvider = "honcho"
	unknown.MemoryProviderURL = "http://hindsight.local:8888"
	if err := unknown.Validate(); err == nil {
		t.Fatal("unknown adapter kind must fail validation, never fall back")
	}

	orphan := base
	orphan.Tools = testTools("workspace", "memory_vault")
	orphan.MemoryProvider = "hindsight"
	orphan.MemoryProviderURL = "http://hindsight.local:8888"
	if err := orphan.Validate(); err == nil {
		t.Fatal("provider fields without the capability wish must fail")
	}

	missing := base
	if err := missing.Validate(); err == nil {
		t.Fatal("capability wish without memory_provider fields must fail")
	}

	badURL := base
	badURL.MemoryProvider = "hindsight"
	badURL.MemoryProviderURL = "hindsight.local:8888"
	if err := badURL.Validate(); err == nil {
		t.Fatal("non-absolute endpoint must fail validation")
	}

	tokenValue := base
	tokenValue.MemoryProvider = "hindsight"
	tokenValue.MemoryProviderURL = "https://api.hindsight.vectorize.io"
	tokenValue.MemoryProviderTokenEnv = "sk-live-secret-value"
	if err := tokenValue.Validate(); err == nil {
		t.Fatal("token_env holding a value (not an env name) must fail")
	}

	ok := base
	ok.MemoryProvider = "hindsight"
	ok.MemoryProviderURL = "https://api.hindsight.vectorize.io"
	ok.MemoryProviderTokenEnv = "HINDSIGHT_API_KEY"
	if err := ok.Validate(); err != nil {
		t.Fatalf("valid provider config rejected: %v", err)
	}
}

// R11.2: under managed capability mode the provider endpoint joins the
// egress allowlist — and drops out cleanly when the capability is off, so
// disabling revokes egress at the relay rather than only at render.
func TestProviderManagedEgress(t *testing.T) {
	dir := t.TempDir()
	on := Settings{CapabilityMode: "managed", Environment: "prod",
		Tools:             testTools("memory_vault", "memory_provider"),
		MemoryProvider:    "hindsight",
		MemoryProviderURL: "https://api.hindsight.vectorize.io"}
	if hosts := managedEgressHosts(on, dir); !slices.Contains(hosts, "api.hindsight.vectorize.io") {
		t.Fatalf("enabled provider host missing from egress allowlist: %v", hosts)
	}

	off := Settings{CapabilityMode: "managed", Environment: "prod",
		Tools: testTools("memory_vault")}
	if hosts := managedEgressHosts(off, dir); slices.Contains(hosts, "api.hindsight.vectorize.io") {
		t.Fatalf("disabled provider kept egress: %v", hosts)
	}

	// A governance deny on the section removes egress even with the wish —
	// deny-wins at the relay, not just at load.
	denied := on
	denied.gov = &toolGovernance{decisions: map[string]api.PolicyDecision{
		"native:memory_provider": {Effect: api.RuleDeny},
	}}
	if hosts := managedEgressHosts(denied, dir); slices.Contains(hosts, "api.hindsight.vectorize.io") {
		t.Fatalf("denied provider kept egress: %v", hosts)
	}
}

// R11.1/R11.4: the rendered record instantiates the seam adapter and
// resolves the token by secrets-env name — an absent record fails closed
// and an unknown kind never falls back to another adapter.
func TestOpenProvider(t *testing.T) {
	d := t.TempDir()
	s := Settings{Environment: "prod"}
	if _, err := OpenProvider(d, s); err == nil {
		t.Fatal("provider open without a rendered record must fail")
	}

	fx, srv := memorytest.NewHindsightFixture(t)
	if err := os.MkdirAll(filepath.Join(d, "vault", "runtime", "memory"), 0o700); err != nil {
		t.Fatal(err)
	}
	cfg, _ := json.Marshal(map[string]string{
		"provider": "hindsight", "url": srv.URL, "token_env": "HINDSIGHT_API_KEY",
	})
	if err := os.WriteFile(filepath.Join(d, "vault", "runtime", "memory", "provider.json"), append(cfg, '\n'), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(d, "runtime.prod.env"), []byte("HINDSIGHT_API_KEY=tok-1\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	p, err := OpenProvider(d, s)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	if err := p.Health(context.Background()); err != nil {
		t.Fatalf("provider must reach the configured endpoint: %v", err)
	}
	fx.SetDown(true)
	if err := p.Health(context.Background()); err == nil {
		t.Fatal("degraded endpoint must report through the opened provider")
	}

	// Managed mode resolves the managed secrets file, not the legacy one.
	managed := Settings{Environment: "prod", CapabilityMode: "managed"}
	if _, err := OpenProvider(d, managed); err == nil {
		t.Fatal("managed mode must read managed-runtime.prod.env, not runtime.prod.env")
	}
	if err := os.WriteFile(filepath.Join(d, "managed-runtime.prod.env"), []byte("HINDSIGHT_API_KEY=tok-2\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := OpenProvider(d, managed); err != nil {
		t.Fatalf("managed open: %v", err)
	}

	// A forged config naming an unknown adapter fails closed.
	if err := os.WriteFile(filepath.Join(d, "vault", "runtime", "memory", "provider.json"),
		[]byte(`{"provider":"honcho","url":"`+srv.URL+`"}`+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := OpenProvider(d, s); err == nil {
		t.Fatal("unknown adapter kind must fail closed at open")
	}
}

// Carry guard (M20.10 precedent): a native section absent from SeedInventory
// can never be granted in a real space — the wish denies as "section not in
// the tool inventory". memory_provider must seed deny-default like
// memory_vault.
func TestProviderInventoried(t *testing.T) {
	for _, s := range api.SeedInventory() {
		if s.ID == "native:memory_provider" {
			return
		}
	}
	t.Fatal("native:memory_provider missing from SeedInventory — the wish can never be admitted")
}

// R11.1: the provider capability stands on the vault — a provider without
// memory_vault has no prov.db spine to replay and must refuse at load,
// like knowledge_ui.
func TestProviderRequiresVault(t *testing.T) {
	s := Settings{Schema: 1, Environment: "prod", User: "alice", Timezone: "UTC",
		BrowserPort: 6080, OAuthPort: 8000,
		Model: "test", ModelURL: "http://model.invalid/v1",
		Tools:          testTools("workspace", "memory_provider"),
		Ingress:        testIngress("workspace"),
		MemoryProvider: "hindsight", MemoryProviderURL: "http://hindsight.invalid:8888"}
	if err := s.Validate(); err == nil {
		t.Fatal("memory_provider without memory_vault must fail")
	}
}
