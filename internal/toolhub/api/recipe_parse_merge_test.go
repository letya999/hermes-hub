package api

import (
	"strings"
	"testing"
)

func TestParseMCPJSONContract(t *testing.T) {
	launch, conn, err := ParseMCPJSON([]byte(`{"mcpServers":{"svc":{"url":"https://mcp.example.com/x","env":{"API_TOKEN":"tok","PLAIN":"v"}}}}`), "mcp.json")
	if err != nil {
		t.Fatal(err)
	}
	if launch.Transport != RemoteMCP || launch.Endpoint != "https://mcp.example.com/x" || launch.Network[0] != "mcp.example.com" {
		t.Fatalf("remote launch: %+v", launch)
	}
	found := false
	for _, f := range conn.Fields {
		if f.Name == "API_TOKEN" {
			found = true
		}
	}
	if !found {
		t.Fatalf("credential field missing: %+v", conn.Fields)
	}
	// command transport
	launch, _, err = ParseMCPJSON([]byte(`{"mcpServers":{"svc":{"command":"npx","args":["-y","pkg"]}}}`), "mcp.json")
	if err != nil || launch.Transport != BoundedCLI || launch.Entrypoint[0] != "npx" {
		t.Fatalf("cli launch: %v %+v", err, launch)
	}
	// multi-server documents never produce a single launch
	launch, _, err = ParseMCPJSON([]byte(`{"mcpServers":{"a":{"command":"npx"},"b":{"url":"https://x.example.com"}}}`), "mcp.json")
	if err != nil || HasLaunch(launch) {
		t.Fatalf("multi-server: %v %+v", err, launch)
	}
	for _, doc := range []string{
		`{"mcpServers":{"s":{"url":"http://insecure.example.com"}}}`,
		`{"mcpServers":{"s":{"url":"https://u:p@e.com"}}}`,
		`{"mcpServers":{"s":{"command":"rm -rf /"}}}`,
		`{"mcpServers":{"s":{"command":"a;b"}}}`,
		`{invalid`,
	} {
		if _, _, err := ParseMCPJSON([]byte(doc), "mcp.json"); err == nil {
			t.Fatalf("unsafe doc accepted: %s", doc)
		}
	}
	// server with neither url nor command is skipped
	if _, _, err := ParseMCPJSON([]byte(`{"mcpServers":{"s":{"note":"x"}}}`), "mcp.json"); err != nil {
		t.Fatalf("empty server: %v", err)
	}
}

func TestParseExampleEnvHeuristics(t *testing.T) {
	conn := ParseExampleEnv([]byte(`
# comment
API_TOKEN=your-token-here
JIRA_URL=https://your-company.atlassian.net
GOOGLE_CREDENTIALS=creds.json
PLAIN_NAME=alice
EMPTY=
`), ".env.example")
	byName := map[string]ConnectionField{}
	for _, f := range conn.Fields {
		byName[f.Name] = f
	}
	if !byName["API_TOKEN"].Required || !byName["API_TOKEN"].Secret {
		t.Fatalf("token field: %+v", byName["API_TOKEN"])
	}
	if !byName["JIRA_URL"].Required {
		t.Fatal("placeholder URL not required")
	}
	if byName["GOOGLE_CREDENTIALS"].Type != "json" || byName["GOOGLE_CREDENTIALS"].Delivery != "json" {
		t.Fatalf("json credentials field: %+v", byName["GOOGLE_CREDENTIALS"])
	}
	if _, ok := byName["PLAIN_NAME"]; ok {
		t.Fatal("concrete non-secret value became a field")
	}
	if byName["EMPTY"].Required || byName["EMPTY"].Secret {
		t.Fatalf("empty non-secret became required: %+v", byName["EMPTY"])
	}
}

func TestParseServerManifestMatching(t *testing.T) {
	source := ArtifactSource{Repository: "https://github.com/o/r", Subfolder: "srv"}
	doc := []byte(`{
		"repository": {"url": "https://github.com/o/r", "subfolder": "srv"},
		"packages": [{"identifier": "img:1@sha256:` + strings.Repeat("a", 64) + `", "transport": "stdio", "registryType": "oci", "command": "run",
			"environmentVariables": [{"name": "API_TOKEN", "isRequired": true, "isSecret": true}, {"name": "lowercase", "isRequired": false}]}],
		"remotes": [{"url": "https://mcp.example.com/x"}],
		"environment": {"DOC_VAR": "v"}
	}`)
	launch, conn, ok, err := ParseServerManifest(doc, "server.json", source)
	if err != nil || !ok || !HasLaunch(launch) {
		t.Fatalf("manifest: %v ok=%v launch=%+v", err, ok, launch)
	}
	if len(conn.Fields) == 0 {
		t.Fatal("connection fields missing")
	}
	// repository mismatch → skipped, not an error
	other := ArtifactSource{Repository: "https://github.com/o/other", Subfolder: "srv"}
	_, _, ok, err = ParseServerManifest(doc, "server.json", other)
	if err != nil || ok {
		t.Fatalf("foreign repo matched: %v %v", ok, err)
	}
	// subfolder mismatch → skipped
	other = ArtifactSource{Repository: "https://github.com/o/r", Subfolder: "other"}
	_, _, ok, err = ParseServerManifest(doc, "server.json", other)
	if err != nil || ok {
		t.Fatalf("foreign subfolder matched: %v %v", ok, err)
	}
	if _, _, _, err := ParseServerManifest([]byte("{bad"), "server.json", source); err == nil {
		t.Fatal("invalid JSON accepted")
	}
	// conflicting launch recipes → error
	conflict := []byte(`{"packages":[
		{"identifier":"a@sha256:` + strings.Repeat("a", 64) + `"},
		{"identifier":"b@sha256:` + strings.Repeat("b", 64) + `"}]}`)
	_, _, _, err = ParseServerManifest(conflict, "server.json", source)
	if err == nil {
		t.Fatal("conflicting launches accepted")
	}
}

func TestManifestPackageLaunchTransports(t *testing.T) {
	l := ManifestPackageLaunch(map[string]any{
		"identifier": "img@sha256:" + strings.Repeat("c", 64),
		"transport":  map[string]any{"type": "streamable-http", "url": "https://mcp.example.com"},
		"args":       []any{"--flag"},
		"runtimeArguments": []any{
			map[string]any{"value": "--verbose"},
			map[string]any{"value": "bad\narg"},
		},
	})
	if l.Transport != RemoteMCP || l.Endpoint != "https://mcp.example.com" {
		t.Fatalf("remote package: %+v", l)
	}
	if l.Digest != "sha256:"+strings.Repeat("c", 64) {
		t.Fatalf("digest split: %+v", l)
	}
	if len(l.Args) != 2 || l.Args[1] != "--verbose" {
		t.Fatalf("args: %v", l.Args)
	}
	l = ManifestPackageLaunch(map[string]any{"transport": "stdio", "registryType": "oci", "runtimeHint": "npx"})
	if l.Transport != ContainerMCP {
		t.Fatalf("oci stdio should stay container: %+v", l)
	}
	l = ManifestPackageLaunch(map[string]any{"transport": "stdio", "runtimeHint": "npx"})
	if l.Transport != BoundedCLI || l.Entrypoint[0] != "npx" {
		t.Fatalf("stdio package: %+v", l)
	}
	if got := ConnectionFromOfficialPackage(map[string]any{
		"environmentVariables": []any{
			map[string]any{"name": "API_TOKEN", "isRequired": true, "isSecret": true},
			map[string]any{"name": "not a key"},
			"garbage",
		},
	}); len(got.Fields) != 1 || !got.Fields[0].Secret {
		t.Fatalf("official package fields: %+v", got.Fields)
	}
}

func TestParseMCPBAndDocumentation(t *testing.T) {
	launch, conn, err := ParseMCPBManifest([]byte(`{
		"manifest_version": "0.2",
		"entry_point": "node",
		"args": ["server.js"],
		"environment": {"API_TOKEN": "x"},
		"config": {"TOKEN_HINT": "y"}
	}`), "manifest.json")
	if err != nil || launch.Transport != BoundedCLI || launch.Entrypoint[0] != "node" || launch.Args[0] != "server.js" {
		t.Fatalf("mcpb: %v %+v", err, launch)
	}
	if len(conn.Fields) == 0 {
		t.Fatal("mcpb connection fields missing")
	}
	if _, _, err := ParseMCPBManifest([]byte("{bad"), "m.json"); err == nil {
		t.Fatal("invalid mcpb accepted")
	}
	for _, ep := range []string{"a;rm", "a|b", "$(x)", "a`b`", "a&b", "a(b)"} {
		doc := `{"entry_point": "` + ep + `"}`
		if _, _, err := ParseMCPBManifest([]byte(doc), "m.json"); err == nil {
			t.Fatalf("unsafe entrypoint %q accepted", ep)
		}
	}
	conn = ParseDocumentationCredentials([]byte(`
Set SLACK_BOT_TOKEN and SLACK_TEAM_ID in the environment.
export JIRA_API_KEY=xxx
The OAUTH_CLIENT_ID must be configured.
lowercase_key is ignored
`), "README.md")
	names := map[string]bool{}
	for _, f := range conn.Fields {
		names[f.Name] = true
	}
	if !names["SLACK_BOT_TOKEN"] || !names["JIRA_API_KEY"] || !names["OAUTH_CLIENT_ID"] {
		t.Fatalf("documentation fields: %+v", conn.Fields)
	}
	if names["lowercase_key"] {
		t.Fatal("lowercase name collected")
	}
}
