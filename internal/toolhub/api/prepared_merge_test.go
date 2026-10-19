package api

import (
	"slices"
	"strings"
	"testing"
)

func TestEmbeddedCatalogsLoad(t *testing.T) {
	entries, err := PreparedCatalog()
	if err != nil || len(entries) == 0 {
		t.Fatalf("prepared catalog: %v len=%d", err, len(entries))
	}
	blocked, err := BlockedCatalog()
	if err != nil {
		t.Fatalf("blocked catalog: %v", err)
	}
	seen := map[string]bool{}
	for _, e := range entries {
		seen[e.ID] = true
		// broker contract resolves for every entry
		if _, err := e.BrokerContract(); err != nil {
			t.Fatalf("%s contract: %v", e.ID, err)
		}
	}
	for _, b := range blocked {
		if seen[b.ID] {
			t.Fatalf("blocked %s collides with prepared", b.ID)
		}
	}
	// lookup paths
	first := entries[0]
	got, ok, err := PreparedForSource(first.Source)
	if err != nil || !ok || got.ID != first.ID {
		t.Fatalf("PreparedForSource: %v %v", ok, err)
	}
	got, ok, err = PreparedForRepository(first.Source.Repository, first.Source.Subfolder)
	if err != nil || !ok || got.ID != first.ID {
		t.Fatalf("PreparedForRepository: %v %v", ok, err)
	}
	_, ok, err = PreparedForRepository("https://github.com/ghost/none", "")
	if err != nil || ok {
		t.Fatalf("ghost repo matched: %v %v", ok, err)
	}
	// malformed catalog data rejected
	if _, err := ParsePreparedCatalog([]byte("[{")); err == nil {
		t.Fatal("truncated catalog accepted")
	}
	if _, err := ParsePreparedCatalog([]byte(`[{"id":"x"}]`)); err == nil {
		t.Fatal("incomplete entry accepted")
	}
	if _, err := ParsePreparedCatalog([]byte(`[] trailing`)); err == nil {
		t.Fatal("trailing data accepted")
	}
}

func TestPreparedOverlayAndEnvArgs(t *testing.T) {
	entries, err := PreparedCatalog()
	if err != nil || len(entries) == 0 {
		t.Fatal("no prepared entries")
	}
	entry := entries[0]
	// overlay: prepared fields win over same-named connection fields
	conn := ConnectionRecipe{Fields: []ConnectionField{
		{Name: "PLACEHOLDER_X", Required: true},
	}}
	if len(entry.Connection.Fields) > 0 {
		conn.Fields = append(conn.Fields, ConnectionField{Name: entry.Connection.Fields[0].Name, Required: false})
	}
	out := entry.Overlay(conn)
	for _, ef := range entry.Connection.Fields {
		found := false
		for _, of := range out.Fields {
			if of.Name == ef.Name {
				found = of.Required == ef.Required
			}
		}
		if !found {
			t.Fatalf("prepared field %s lost in overlay: %+v", ef.Name, out.Fields)
		}
	}
	// runtime/proxy env args
	def := ToolDefinition{
		RuntimeEnvironment: map[string]string{"B_VAR": "2", "A_VAR": "1"},
		ProxyEnvironment:   []string{"HTTP_PROXY_HOST", "MY_PROXY"},
	}
	args := RuntimeEnvironmentArgs(def)
	if !slices.Equal(args, []string{"--env", "A_VAR=1", "--env", "B_VAR=2"}) {
		t.Fatalf("runtime args: %v", args)
	}
	args = ProxyEnvironmentArgs(def, "http://proxy.local:3128")
	joined := strings.Join(args, " ")
	if !strings.Contains(joined, "HTTP_PROXY_TYPE=http") || !strings.Contains(joined, "HTTP_PROXY_HOST=proxy.local") || !strings.Contains(joined, "MY_PROXY=http://proxy.local:3128") {
		t.Fatalf("proxy args: %v", args)
	}
	// gate: only AuthBeforeToolsList entries gate
	if err := PreparedCredentialGate(ArtifactSource{Repository: "https://github.com/ghost/none"}); err != nil {
		t.Fatalf("unknown source gated: %v", err)
	}
}

// The gitlab source ships docker/.env.example with GITLAB_PROJECT_ID=<YOUR_…>:
// discovery marks the placeholder required, but the reviewed gitlab-pat
// contract can never deliver it. Overlay must demote such leftover extras to
// optional or the produced definition fails "missing credential broker
// delivery" at validation.
func TestPreparedOverlayDemotesUnreviewedExtras(t *testing.T) {
	entries, err := PreparedCatalog()
	if err != nil {
		t.Fatal(err)
	}
	var entry PreparedEntry
	for _, e := range entries {
		if e.ID == "gitlab" {
			entry = e
		}
	}
	if entry.ID == "" {
		t.Fatal("gitlab prepared entry missing")
	}
	conn := entry.Overlay(ConnectionRecipe{Fields: []ConnectionField{
		{Name: "GITLAB_PROJECT_ID", Required: true, Type: "string", Delivery: "env"},
		{Name: "GITLAB_PERSONAL_ACCESS_TOKEN", Required: true, Type: "token", Secret: true, Delivery: "env"},
	}})
	byName := map[string]ConnectionField{}
	for _, f := range conn.Fields {
		byName[f.Name] = f
	}
	if !byName["GITLAB_PERSONAL_ACCESS_TOKEN"].Required {
		t.Fatal("reviewed field lost its required flag")
	}
	if byName["GITLAB_PROJECT_ID"].Required {
		t.Fatal("unreviewed upstream extra stayed required")
	}
	// End-to-end shape: the overlaid recipe must still yield a definition that
	// passes the credential broker delivery check.
	config := NormalizeArtifactImportConfig(entry.Apply(DefaultSelfInstallConfig(entry.Source)))
	config.Credentials = conn.CredentialInputs()
	definition := ToolDefinition{Schema: SchemaVersion, DefinitionID: entry.ID, Version: config.Version, Transport: ContainerMCP,
		Source:      DefinitionSource{Image: config.Image, Digest: "sha256:" + strings.Repeat("a", 64)},
		Credentials: config.Credentials, CredentialGroups: config.CredentialGroups, CredentialContractID: config.CredentialContractID, CredentialContractRevision: config.CredentialContractRevision, CredentialContractEnv: config.CredentialContractEnv,
		Environment: config.Environment, RuntimeEnvironment: config.RuntimeEnvironment, ProxyEnvironment: config.ProxyEnvironment, Tools: config.Tools, Workload: config.Workload, Execution: config.Execution, Health: config.Health}
	if err := definition.Validate(); err != nil {
		t.Fatalf("overlaid definition invalid: %v", err)
	}
}
