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
