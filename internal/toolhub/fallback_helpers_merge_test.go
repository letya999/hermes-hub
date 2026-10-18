package toolhub

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/letya999/hermes-hub/internal/toolhub/api"
)

// materializeRuntimeCredentials: a json-delivery value holding valid JSON is
// materialized as a read-only mount while the env file points at the mount
// target — keyed on the declared delivery, not on a CREDENTIALS-looking name.
func TestMaterializeRuntimeCredentials(t *testing.T) {
	t.Parallel()
	workspace := t.TempDir()
	envPath := filepath.Join(workspace, "credentials.env")
	if err := os.WriteFile(envPath, []byte("PLAN_GOOGLE_CREDENTIALS={\"k\":1}\nPLAN_TOKEN=tok\n"), 0600); err != nil {
		t.Fatal(err)
	}
	definition := api.ToolDefinition{Credentials: []api.CredentialInput{{Name: "PLAN_GOOGLE_CREDENTIALS", Delivery: "json"}, {Name: "PLAN_TOKEN"}}}
	env, mounts, err := materializeRuntimeCredentials(workspace, definition)
	if err != nil {
		t.Fatalf("materialize: %v", err)
	}
	if len(mounts) != 1 || !mounts[0].ReadOnly || !strings.HasPrefix(mounts[0].Target, "/run/credentials/") {
		t.Fatalf("mount contract broken: %+v", mounts)
	}
	body, err := os.ReadFile(env)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(body), "PLAN_GOOGLE_CREDENTIALS=/run/credentials/PLAN_GOOGLE_CREDENTIALS.json") || !strings.Contains(string(body), "PLAN_TOKEN=tok") {
		t.Fatalf("runtime env missing redirected credential: %q", body)
	}
	staged, err := os.ReadFile(mounts[0].Source)
	if err != nil || string(staged) != `{"k":1}` {
		t.Fatalf("staged credentials file: %q %v", staged, err)
	}
}

// Without a JSON credential the source env file is reused and no mount is
// produced; malformed JSON stays inline.
func TestMaterializeRuntimeCredentialsInline(t *testing.T) {
	t.Parallel()
	workspace := t.TempDir()
	envPath := filepath.Join(workspace, "credentials.env")
	if err := os.WriteFile(envPath, []byte("PLAN_GOOGLE_CREDENTIALS=not-json\nPLAN_TOKEN=tok\n"), 0600); err != nil {
		t.Fatal(err)
	}
	definition := api.ToolDefinition{Credentials: []api.CredentialInput{{Name: "PLAN_GOOGLE_CREDENTIALS"}, {Name: "PLAN_TOKEN"}, {Name: "PLAN_ABSENT"}}}
	env, mounts, err := materializeRuntimeCredentials(workspace, definition)
	if err != nil {
		t.Fatalf("materialize: %v", err)
	}
	if len(mounts) != 0 || env != envPath {
		t.Fatalf("expected inline credentials to reuse source env: env=%q mounts=%v", env, mounts)
	}
	if _, err := os.Stat(filepath.Join(workspace, "credentials.runtime.env")); !os.IsNotExist(err) {
		t.Fatal("runtime env file must not exist without mounts")
	}
}

// A file credential already delivered as a broker lease mount must not be
// materialized a second time: the workspace env already carries the container
// path, and a duplicate bind on the same target makes docker create fail.
func TestMaterializeRuntimeCredentialsSkipsLeaseDeliveredFile(t *testing.T) {
	t.Parallel()
	workspace := t.TempDir()
	envPath := filepath.Join(workspace, "credentials.env")
	if err := os.WriteFile(envPath, []byte("CLIENT_JSON=/run/mcp-secrets/client.json\nAPI_TOKEN=tok\n"), 0600); err != nil {
		t.Fatal(err)
	}
	definition := api.ToolDefinition{Credentials: []api.CredentialInput{
		{Name: "CLIENT_JSON", Required: true, Delivery: "file", Target: "/run/mcp-secrets/client.json"},
		{Name: "API_TOKEN", Required: true, Delivery: "env"},
	}}
	env, mounts, err := materializeRuntimeCredentials(workspace, definition)
	if err != nil {
		t.Fatalf("materialize: %v", err)
	}
	if len(mounts) != 0 {
		t.Fatalf("lease-delivered file credential mounted twice: %+v", mounts)
	}
	if _, err := os.Stat(filepath.Join(workspace, "CLIENT_JSON.secret")); !os.IsNotExist(err) {
		t.Fatal("lease-delivered file credential must not stage a workspace .secret")
	}
	body, err := os.ReadFile(env)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(body), "CLIENT_JSON=/run/mcp-secrets/client.json") || !strings.Contains(string(body), "API_TOKEN=tok") {
		t.Fatalf("runtime env lost entries: %q", body)
	}
}

// writeWorldReadableTemp produces a readable file or cleans it up on error.
func TestWriteWorldReadableTemp(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	name, err := writeWorldReadableTemp(dir, "stage-*", "payload")
	if err != nil {
		t.Fatal(err)
	}
	body, err := os.ReadFile(name)
	if err != nil || string(body) != "payload" {
		t.Fatalf("staged content: %q %v", body, err)
	}
	if !strings.HasPrefix(filepath.Base(name), "stage-") {
		t.Fatalf("name pattern ignored: %q", name)
	}
	if _, err := writeWorldReadableTemp(filepath.Join(dir, "missing", "nested"), "x-*", "y"); err == nil {
		t.Fatal("unwritable dir must error")
	}
}

// Fallback resource names and env shape are deterministic per workload.
func TestGenericFallbackResourcesAndEnv(t *testing.T) {
	t.Parallel()
	resources := genericFallbackResources("wl-1")
	for name, value := range map[string]string{
		"Network": resources.Network, "ProxyName": resources.ProxyName, "ProxyVol": resources.ProxyVol,
		"BridgeVol": resources.BridgeVol, "RelayName": resources.RelayName, "RelayVol": resources.RelayVol,
		"BridgeSeed": resources.BridgeSeed, "RemoteName": resources.RemoteName,
	} {
		if value == "" {
			t.Fatalf("%s empty", name)
		}
	}
	if genericStateVolume("wl-1", 2) != "wl-1-state-2" {
		t.Fatal("state volume naming contract broken")
	}
	env := fallbackToolHiveEnv("/state", map[string]string{"EXTRA": "1"})
	if env["HOME"] != "/state" || env["TOOLHIVE_RUNTIME"] != "docker" || env["TOOLHIVE_SECRETS_PROVIDER"] != "environment" || env["EXTRA"] != "1" {
		t.Fatalf("toolhive env contract broken: %v", env)
	}
}

// credentialHints skip optional and per-request inputs and attach the
// alternative-group index; recipe fields take precedence over definition
// inputs and inherit alternative URLs.
func TestCredentialHintsContract(t *testing.T) {
	t.Parallel()
	definition := api.ToolDefinition{
		Credentials: []api.CredentialInput{
			{Name: "PLAN_API_KEY", Required: true},
			{Name: "PLAN_GOOGLE_CREDENTIALS", Required: true},
			{Name: "OPTIONAL_KEY", Required: false},
			{Name: "PER_REQUEST_TOKEN", Required: true, PerRequest: true},
		},
		CredentialGroups: [][]string{{"PLAN_API_KEY"}},
	}
	hints := credentialHints(definition)
	if len(hints) != 2 {
		t.Fatalf("hints: %+v", hints)
	}
	byName := map[string]api.CredentialHint{}
	for _, hint := range hints {
		byName[hint.Name] = hint
	}
	if byName["PLAN_API_KEY"].AlternativeGroup != 1 || byName["PLAN_API_KEY"].Delivery != "env" {
		t.Fatalf("group/delivery: %+v", byName["PLAN_API_KEY"])
	}
	if byName["PLAN_GOOGLE_CREDENTIALS"].Type != "json" || byName["PLAN_GOOGLE_CREDENTIALS"].Delivery != "json" {
		t.Fatalf("CREDENTIALS input must hint json: %+v", byName["PLAN_GOOGLE_CREDENTIALS"])
	}
	recipe := api.ConnectionRecipe{Fields: []api.ConnectionField{
		{Name: "R1", Required: true, Type: "url", Target: "URL"},
		{Name: "R2", Required: false},
	}, Alternatives: []api.ConnectionAlternative{{URL: "https://alt.example"}}}
	recipeHints := credentialHintsForRecipe(definition, recipe)
	if len(recipeHints) != 1 || recipeHints[0].Name != "R1" || recipeHints[0].Type != "url" || recipeHints[0].AlternativeURL != "https://alt.example" {
		t.Fatalf("recipe hints: %+v", recipeHints)
	}
	fallback := credentialHintsForRecipe(definition, api.ConnectionRecipe{})
	if len(fallback) != len(hints) {
		t.Fatal("empty recipe must fall back to definition hints")
	}
	names := credentialNames(api.Onboarding{Required: []api.CredentialHint{{Name: "A"}, {Name: "B"}}})
	if strings.Join(names, ",") != "A,B" {
		t.Fatalf("names: %v", names)
	}
}
