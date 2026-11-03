package stack

import (
	"os"
	"path/filepath"
	"testing"
)

// The published models doc is the imperative model plane (SPEC-0092 R7):
// every service becomes a provider hop with its own endpoint and credential
// contract — per-instance cliproxy included — with limits and reasoning
// carried through, and no file-level switch policy (models.* rights gate
// switching at the relay PEP).
func TestRelayRegistryFromPassport(t *testing.T) {
	spaces := filepath.Join(t.TempDir(), "spaces")
	user := filepath.Join(spaces, "artem_two")
	rev := "r-testmodels"
	revDir := filepath.Join(user, "generated", "passports", rev)
	if err := os.MkdirAll(revDir, 0700); err != nil {
		t.Fatal(err)
	}
	head := func(kind string) string {
		return "apiVersion: six-passport/v2\nkind: " + kind + "\nmetadata:\n  space: artem_two\n  environment: dev\n  revision: " + rev + "\n  catalogs: \"5\"\n"
	}
	docs := map[string]string{
		"stack":     "identity:\n  user: artem_two\nservices:\n  cliproxy-google-1:\n    mode: per-space\n  model-relay:\n    mode: per-space\n",
		"config":    "runtime:\n  timezone: UTC\n",
		"workspace": "writable:\n  - /workspace\n",
		"tools":     "core:\n  files:\n    via: toolhub\n",
		"grants":    "allow:\n  models:\n    - list\n    - use\n",
		"models": `default: everyday
routes:
  everyday:
    ladder: [google-pro-1, openrouter-key]
services:
  google-pro-1:
    via: cliproxy
    endpoint: http://cliproxy-google-1:8317/v1
    auth:
      upstream: {kind: oauth-session, ref: google-one-1}
      proxy_access: {kind: broker-key, ref: cliproxy-google-1}
    sharing: personal
    limits:
      - {of: provider_quota, amount: 50, unit: percent, per: 1d, clock: provider}
      - {of: requests, amount: 100, per: 5h}
    models:
      - {id: gemini-3.8-flash-high, reasoning: high}
      - {id: gemini-3.7-flash-high, reasoning: high}
  openrouter-key:
    via: openai-compatible
    endpoint: https://openrouter.ai/api/v1
    auth:
      upstream: {kind: broker-key, ref: openrouter-alice}
    sharing: personal
    limits:
      - {of: spend_usd, amount: 10, per: 1d}
    models:
      - {id: google/gemini-3.8-flash, reasoning: high}
`,
	}
	for name, body := range docs {
		if err := os.WriteFile(filepath.Join(revDir, name+".yaml"), []byte(head(name)+body), 0600); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(filepath.Join(user, "generated", "passports", "current"), []byte(rev+"\n"), 0600); err != nil {
		t.Fatal(err)
	}
	reg, err := LoadModelRegistry(user, Settings{User: "artem_two", Environment: "dev"})
	if err != nil {
		t.Fatalf("registry from published models doc: %v", err)
	}
	gp, ok := reg.Providers["google-pro-1"]
	if !ok {
		t.Fatalf("cliproxy service not a provider hop: %+v", reg.Providers)
	}
	if gp.URL != "http://cliproxy-google-1:8317/v1" {
		t.Fatalf("service endpoint lost: %q", gp.URL)
	}
	if !gp.AllowPrivate {
		t.Fatal("cliproxy endpoint is an internal name but AllowPrivate is unset")
	}
	if gp.BrokerGrants["artem_two"] != "cliproxy-google-1" {
		t.Fatalf("proxy_access ref not wired as the hop credential: %+v", gp.BrokerGrants)
	}
	or := reg.Providers["openrouter-key"]
	if or.BrokerGrants["artem_two"] != "openrouter-alice" || or.CredentialEnv == "" {
		t.Fatalf("upstream broker-key not wired: %+v", or)
	}
	ladder := reg.Models["everyday"].Ladder
	if len(ladder) != 2 || ladder[0].Provider != "google-pro-1" || ladder[1].Provider != "openrouter-key" {
		t.Fatalf("route ladder does not name services: %+v", ladder)
	}
	if len(ladder[0].Models) != 2 || ladder[0].Models[0] != "gemini-3.8-flash-high" {
		t.Fatalf("in-service model list lost: %+v", ladder[0].Models)
	}
	if !ladder[0].Reasoning {
		t.Fatal("reasoning-capable service lost its flag")
	}
	var quota *ModelLimit
	for i := range ladder[0].Limits {
		if ladder[0].Limits[i].Of == "provider_quota" {
			quota = &ladder[0].Limits[i]
		}
	}
	if quota == nil || quota.Amount != 50 || quota.Per != "1d" {
		t.Fatalf("provider_quota limit dropped: %+v", ladder[0].Limits)
	}
	if len(reg.Switch) != 0 {
		t.Fatalf("file switch policy emitted — models.* rights own switching: %+v", reg.Switch)
	}
}
