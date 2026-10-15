package api

import (
	"strings"
	"testing"
)

func TestConnectionRecipeHelpers(t *testing.T) {
	// connection fields come from declared env blocks, not arbitrary keys
	doc := map[string]any{
		"env": map[string]any{
			"API_TOKEN": map[string]any{"delivery": "header", "target": "Authorization", "type": "bearer", "required": false, "secret": true},
			"REGION":    "us",
			"lower_bad": "x",
		},
	}
	recipe := ConnectionFromMap(doc, "env", "secrets")
	if len(recipe.Fields) != 2 {
		t.Fatalf("fields: %+v", recipe.Fields)
	}
	var token ConnectionField
	for _, f := range recipe.Fields {
		if f.Name == "API_TOKEN" {
			token = f
		}
	}
	if token.Delivery != "header" || token.Target != "Authorization" || token.Type != "bearer" || token.Required || !token.Secret {
		t.Fatalf("token field: %+v", token)
	}
	// oauth alternatives collect url + env fields
	altDoc := map[string]any{"oauth": map[string]any{
		"google": map[string]any{"authorization_url": "https://accounts.google.com", "env": map[string]any{"CLIENT_ID": "", "CLIENT_SECRET": ""}},
		"empty":  map[string]any{},
	}}
	alts := ConnectionAlternatives(altDoc)
	if len(alts.Alternatives) != 1 || alts.Alternatives[0].Name != "google" || len(alts.Alternatives[0].Fields) != 2 {
		t.Fatalf("alternatives: %+v", alts.Alternatives)
	}
	// merge normalizes, dedupes and sorts
	merged := mergeConnection(recipe, ConnectionRecipe{Fields: []ConnectionField{{Name: "API_TOKEN"}, {Name: "OTHER_KEY"}}})
	if len(merged.Fields) != 3 || merged.Fields[0].Name != "API_TOKEN" {
		t.Fatalf("merged: %+v", merged.Fields)
	}
	// definition merge replaces credentials with the recipe's inputs
	def := ToolDefinition{Credentials: []CredentialInput{{Name: "OLD"}}}
	MergeConnectionForDefinition(recipe, &def)
	if len(def.Credentials) != 2 {
		t.Fatalf("definition credentials: %+v", def.Credentials)
	}
	// launch merge: empty fills, disagreement warns
	var warnings []string
	l1 := LaunchRecipe{Artifact: "img", Digest: "sha256:" + string(make([]byte, 0))}
	r1 := LaunchRecipe{Endpoint: "https://x"}
	if got := MergeLaunch(LaunchRecipe{}, r1, &warnings); got.Endpoint != "https://x" {
		t.Fatalf("empty merge: %+v", got)
	}
	if got := MergeLaunch(l1, LaunchRecipe{Artifact: "other"}, &warnings); len(warnings) != 1 || got.Artifact != "img" {
		t.Fatalf("conflict merge: %+v %v", got, warnings)
	}
	if !LaunchReady(LaunchRecipe{Transport: RemoteMCP, Endpoint: "https://mcp.example.com/x"}) ||
		LaunchReady(LaunchRecipe{Transport: ContainerMCP, Artifact: "img"}) {
		t.Fatal("launch readiness")
	}
	// name classification helpers
	if ConnectionType("OAUTH_TOKEN") != "oauth" || ConnectionType("API_TOKEN") != "secret" || ConnectionType("REGION") != "string" {
		t.Fatal("connection types")
	}
	if !SecretName("PRIVATE_KEY") || SecretName("PUBLIC_URL") {
		t.Fatal("secret names")
	}
	if !nonCredentialEnvName("HTTP_PROXY") || !nonCredentialEnvName("TEST_DB") || nonCredentialEnvName("API_KEY") {
		t.Fatal("non-credential names")
	}
	if !testFixturePath("tests/fixtures/x.json") || testFixturePath("src/config.json") {
		t.Fatal("fixture paths")
	}
	if schemaFieldName("apiKey") != "API_KEY" || schemaFieldName("weird!") != "WEIRD_" {
		t.Fatalf("schema names: %q", schemaFieldName("weird!"))
	}
	// immutable image split and safe string arrays
	name, digest, ok := SplitImmutableImage("img@sha256:" + strings.Repeat("ab", 32))
	if !ok || name != "img" || digest == "" {
		t.Fatalf("split: %q %q %v", name, digest, ok)
	}
	if _, _, ok := SplitImmutableImage("img:latest"); ok {
		t.Fatal("mutable tag split accepted")
	}
	if got := StringArray([]any{"a", 1, "b"}); len(got) != 2 {
		t.Fatalf("string array: %v", got)
	}
	if StringArray([]any{"a\nb"}) != nil {
		t.Fatal("newline element accepted")
	}
}
