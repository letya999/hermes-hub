package api

import (
	"archive/tar"
	"bytes"
	"context"
	"strings"
	"testing"
)

func recipeContextTar(t *testing.T, files map[string]string) []byte {
	t.Helper()
	var buf bytes.Buffer
	w := tar.NewWriter(&buf)
	for name, body := range files {
		if err := w.WriteHeader(&tar.Header{Name: name, Mode: 0600, Size: int64(len(body)), Typeflag: tar.TypeReg}); err != nil {
			t.Fatal(err)
		}
		if _, err := w.Write([]byte(body)); err != nil {
			t.Fatal(err)
		}
	}
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

// Every discovered connection field carries the path of the metadata file it
// came from, so a conflicting set can be filtered to one authoritative source.
func TestConnectionFieldsRecordSource(t *testing.T) {
	source := ArtifactSource{Repository: "https://github.com/acme/demo", CommitSHA: "0123456789abcdef0123456789abcdef01234567"}
	contextBytes := recipeContextTar(t, map[string]string{
		"server.json": `{
			"name": "acme/demo",
			"packages": [{"identifier": "demo", "registryType": "oci",
				"environmentVariables": [{"name": "DEMO_TOKEN", "isRequired": true, "isSecret": true}]}]}`,
		".env.example": "DEMO_TOKEN=\nDEMO_PROJECT=<YOUR_PROJECT>\n",
	})
	resolution, err := (RecipeResolver{}).Resolve(context.Background(), source, contextBytes)
	if err != nil {
		t.Fatal(err)
	}
	sources := map[string]string{}
	for _, field := range resolution.Connection.Fields {
		sources[field.Name] = field.Source
	}
	if sources["DEMO_PROJECT"] != ".env.example" {
		t.Fatalf("DEMO_PROJECT source=%q", sources["DEMO_PROJECT"])
	}
	if sources["DEMO_TOKEN"] != "server.json" {
		t.Fatalf("DEMO_TOKEN source=%q", sources["DEMO_TOKEN"])
	}
}

// Every recognized metadata file contributes its own path as the field
// provenance tag, not only server.json and .env.example.
func TestConnectionFieldsRecordAllSources(t *testing.T) {
	source := ArtifactSource{Repository: "https://github.com/acme/demo", CommitSHA: "0123456789abcdef0123456789abcdef01234567"}
	contextBytes := recipeContextTar(t, map[string]string{
		"manifest.json": `{"name": "demo", "server": {"mcp_config": {"command": "demo",
			"env": {"MCPB_TOKEN": "${user_config.mcpb_token}"}}},
			"user_config": {"mcpb_token": {"required": true, "sensitive": true}}}`,
		"Dockerfile":   "FROM scratch\nENV DOCKER_TOKEN=\n",
		"compose.yaml": "services:\n  mcp-app:\n    build: .\n    environment:\n      COMPOSE_TOKEN: ''\n",
		"README.md":    "# demo\nSet `README_TOKEN` env var to your token.\n",
		".mcp.json":    `{"mcpServers": {"demo": {"command": "demo", "env": {"MCPJSON_TOKEN": "x"}}}}`,
	})
	resolution, err := (RecipeResolver{}).Resolve(context.Background(), source, contextBytes)
	if err != nil {
		t.Fatal(err)
	}
	sources := map[string]string{}
	for _, field := range resolution.Connection.Fields {
		sources[field.Name] = field.Source
	}
	for name, want := range map[string]string{
		"MCPB_TOKEN": "manifest.json", "DOCKER_TOKEN": "Dockerfile",
		"COMPOSE_TOKEN": "compose.yaml", "README_TOKEN": "README.md",
		"MCPJSON_TOKEN": ".mcp.json",
	} {
		if sources[name] != want {
			t.Fatalf("%s source=%q want %q (fields=%v)", name, sources[name], want, resolution.Connection.Fields)
		}
	}
}

// When sources disagree about a field, the resolution warns which source says
// what instead of silently keeping the first merge result.
func TestConnectionSourceConflictWarns(t *testing.T) {
	source := ArtifactSource{Repository: "https://github.com/acme/demo", CommitSHA: "0123456789abcdef0123456789abcdef01234567"}
	contextBytes := recipeContextTar(t, map[string]string{
		"server.json": `{
			"name": "acme/demo",
			"packages": [{"identifier": "demo", "registryType": "oci",
				"environmentVariables": [{"name": "DEMO_PROJECT", "isRequired": false}]}]}`,
		".env.example": "DEMO_PROJECT=<YOUR_PROJECT>\n",
	})
	resolution, err := (RecipeResolver{}).Resolve(context.Background(), source, contextBytes)
	if err != nil {
		t.Fatal(err)
	}
	var found bool
	for _, warning := range resolution.Warnings {
		if strings.Contains(warning, "DEMO_PROJECT") && strings.Contains(warning, ".env.example") {
			found = true
		}
	}
	if !found {
		t.Fatalf("warnings=%v", resolution.Warnings)
	}
}

// Choosing one authoritative source rebuilds the connection recipe from that
// source alone and reports which sources were available.
func TestFilterConnectionSource(t *testing.T) {
	recipe := ConnectionRecipe{Fields: []ConnectionField{
		{Name: "DEMO_TOKEN", Required: true, Secret: true, Delivery: "env", Target: "DEMO_TOKEN", Source: "server.json"},
		{Name: "DEMO_PROJECT", Required: false, Delivery: "env", Target: "DEMO_PROJECT", Source: ".env.example"},
		{Name: "DEMO_OTHER", Required: false, Delivery: "env", Target: "DEMO_OTHER", Source: "docker/.env.example"},
	}}
	filtered, sources := FilterConnectionSource(recipe, "server.json")
	if len(filtered.Fields) != 1 || filtered.Fields[0].Name != "DEMO_TOKEN" {
		t.Fatalf("filtered=%+v", filtered.Fields)
	}
	joined := strings.Join(sources, ",")
	if !strings.Contains(joined, "server.json") || !strings.Contains(joined, ".env.example") {
		t.Fatalf("sources=%v", sources)
	}
	if again, _ := FilterConnectionSource(recipe, "nonexistent"); len(again.Fields) != 0 {
		t.Fatalf("unknown source must yield an empty recipe: %+v", again.Fields)
	}
	// The basename matches a nested path: "env.example" family selection keeps
	// every .env.example file regardless of directory.
	family, _ := FilterConnectionSource(recipe, ".env.example")
	if len(family.Fields) != 2 {
		t.Fatalf("family=%+v", family.Fields)
	}
}

// Catalog candidates carry their own provenance tag so a source filter can
// include or exclude catalog-declared fields deliberately.
func TestCatalogFieldsRecordSource(t *testing.T) {
	source := ArtifactSource{Repository: "https://github.com/acme/demo", CommitSHA: "0123456789abcdef0123456789abcdef01234567"}
	contextBytes := recipeContextTar(t, map[string]string{".env.example": "DEMO_TOKEN=\n"})
	resolver := RecipeResolver{Catalogs: []RecipeCatalog{catalogStub{candidates: []RecipeCandidate{{
		Repository: source.Repository, CommitSHA: source.CommitSHA,
		Connection: ConnectionRecipe{Fields: []ConnectionField{{Name: "CATALOG_KEY", Required: true, Secret: true, Delivery: "env", Target: "CATALOG_KEY"}}},
	}}}}}
	resolution, err := resolver.Resolve(context.Background(), source, contextBytes)
	if err != nil {
		t.Fatal(err)
	}
	for _, field := range resolution.Connection.Fields {
		if field.Name == "CATALOG_KEY" && field.Source != "catalog" {
			t.Fatalf("CATALOG_KEY source=%q", field.Source)
		}
	}
}

type catalogStub struct{ candidates []RecipeCandidate }

func (c catalogStub) Lookup(context.Context, RecipeLookup) ([]RecipeCandidate, error) {
	return c.candidates, nil
}
