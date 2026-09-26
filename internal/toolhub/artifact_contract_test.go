package toolhub

import (
	"context"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func TestApplyPreflightToolListRestampAndRegisters(t *testing.T) {
	artifact := importedReviewPacket(t)
	claimedDigest := artifact.Definition.Source.ReviewDigest
	listed := []ToolSpec{{Name: "get_current_config", Effect: ReadEffect}, {Name: "list_dir", Effect: ReadEffect}}
	contract, err := ApplyPreflightToolList(&artifact, listed)
	if err != nil {
		t.Fatal(err)
	}
	if contract.Source != ToolContractPreflight || len(contract.Tools) != 2 {
		t.Fatalf("preflight contract: %+v", contract)
	}
	if artifact.Definition.Source.ReviewDigest == "" || artifact.Definition.Source.ReviewDigest == claimedDigest {
		t.Fatal("review digest was not recomputed from the confirmed tools")
	}
	if artifact.Definition.Source.ToolContractSource != ToolContractPreflight || artifact.Definition.Source.ToolContractDigest == "" {
		t.Fatal("preflight contract was not stamped")
	}
	store := NewStore()
	if err := store.RegisterTrustedArtifact(artifact, artifact.Definition.Source.ReviewDigest); err != nil {
		t.Fatalf("preflighted packet rejected: %v", err)
	}
	if err := store.RegisterTrustedArtifact(artifact, claimedDigest); err == nil {
		t.Fatal("stale claimed-tool review digest accepted")
	}
}

func TestApplyPreflightToolListRejectsInvalidTools(t *testing.T) {
	artifact := importedReviewPacket(t)
	if _, err := ApplyPreflightToolList(nil, []ToolSpec{{Name: "read", Effect: ReadEffect}}); err == nil {
		t.Fatal("nil packet accepted")
	}
	if _, err := ApplyPreflightToolList(&artifact, nil); err == nil {
		t.Fatal("empty tools/list accepted")
	}
	if _, err := ApplyPreflightToolList(&artifact, []ToolSpec{{Name: "Not a tool", Effect: ReadEffect}}); err == nil {
		t.Fatal("invalid MCP tool name accepted")
	}
}

func TestDecodeMCPToolListReadsID2Payload(t *testing.T) {
	body := `{"jsonrpc":"2.0","id":1,"result":{"protocolVersion":"2024-11-05"}}
{"jsonrpc":"2.0","id":2,"result":{"tools":[{"name":"API-get-self","annotations":{"readOnlyHint":true}},{"name":"delete_file","annotations":{"destructiveHint":true}},{"name":"unknown_effect"}]}}
`
	tools, err := decodeMCPToolList(strings.NewReader(body))
	if err != nil || len(tools) != 3 || tools[0].Name != "API-get-self" || tools[0].Effect != ReadEffect || tools[1].Effect != WriteEffect || tools[2].Effect != WriteEffect {
		t.Fatalf("decode: %+v %v", tools, err)
	}
}

func TestDecodeMCPToolListSkipsServerLogLines(t *testing.T) {
	// slack-mcp-server and similar Go servers interleave structured startup
	// logs and plain text on stdout with the real JSON-RPC stream.
	body := `{"level":"info","msg":"starting slack-mcp-server"}
{"level":"error","error":"SLACK_MCP_XOXC_TOKEN not set"}
plain text startup line
{"jsonrpc":"2.0","method":"notifications/progress","params":{}}
{"jsonrpc":"2.0","id":1,"result":{"protocolVersion":"2024-11-05"}}
{"jsonrpc":"2.0","id":2,"result":{"tools":[{"name":"conversations_history","annotations":{"readOnlyHint":true}}]}}
`
	tools, err := decodeMCPToolList(strings.NewReader(body))
	if err != nil || len(tools) != 1 || tools[0].Name != "conversations_history" || tools[0].Effect != ReadEffect {
		t.Fatalf("decode with log lines: %+v %v", tools, err)
	}
}

func TestDecodeMCPToolListReturnsRPCError(t *testing.T) {
	body := `{"jsonrpc":"2.0","id":2,"error":{"code":-32600,"message":"bad request"}}
`
	if _, err := decodeMCPToolList(strings.NewReader(body)); err == nil || !strings.Contains(err.Error(), "bad request") {
		t.Fatalf("expected RPC error, got %v", err)
	}
}

func TestParseMCPHelpCredentialsUsesServerDeclaration(t *testing.T) {
	help := `Environment Variables:
  SERVICE_TOKEN          Provider token (recommended)
  OPENAPI_HEADERS        JSON headers (alternative)
  AUTH_TOKEN             Transport authentication (alternative)
  REQUIRED_SECRET        Account secret (required)

Examples:`
	got := parseMCPHelpCredentials(help)
	if len(got) != 2 || got[0].Name != "SERVICE_TOKEN" || got[1].Name != "REQUIRED_SECRET" || !got[0].Required || !got[1].Required {
		t.Fatalf("credentials=%+v", got)
	}
	if got := parseMCPHelpCredentials("Environment Variables:\n  TOKEN optional\n"); len(got) != 0 {
		t.Fatalf("optional credential guessed: %+v", got)
	}
}

func TestRandomPreflightContainerName(t *testing.T) {
	first, err := randomPreflightContainerName("preflight-")
	if err != nil {
		t.Fatal(err)
	}
	second, err := randomPreflightContainerName("preflight-")
	if err != nil {
		t.Fatal(err)
	}
	if len(first) != len("preflight-")+8 || first == second {
		t.Fatalf("unsafe names %q %q", first, second)
	}
}

func TestPreflightDockerRunArgsPinMCPRuntimeNotBuilder(t *testing.T) {
	imported := importedReviewPacket(t)
	imported.Definition.Source.Image = "hermes-artifact/serena"
	imported.Definition.Execution.Mounts = nil
	args := preflightDockerRunArgs(imported, "hermes-preflight-x")
	joined := strings.Join(args, "\x00")
	for _, required := range []string{"--read-only", "--cap-drop\x00ALL", "no-new-privileges", "--network\x00none", "--user\x0010001:10001", "HOME=/tmp"} {
		if !strings.Contains(joined, required) {
			t.Fatalf("missing MCP runtime flag %q in %q", required, args)
		}
	}
	for _, forbidden := range []string{"--privileged", "seccomp=unconfined", "SETUID", "/var/run/docker.sock"} {
		if strings.Contains(joined, forbidden) {
			t.Fatalf("builder/host flag %q leaked into preflight: %q", forbidden, args)
		}
	}
	imported.Definition.Execution.Mounts = []Mount{{Source: "connection-state", Target: "/state"}}
	imported.Recipe.Entrypoint = []string{"/app/mcp-server"}
	args = preflightDockerRunArgs(imported, "hermes-preflight-x")
	joined = strings.Join(args, "\x00")
	if !strings.Contains(joined, "/state:rw") || args[len(args)-1] != "/state" {
		t.Fatalf("stateful preflight must pass allowed dir: %q", args)
	}
	imported.Recipe.Entrypoint = []string{"/app/mcp-server", "/state"}
	args = preflightDockerRunArgs(imported, "hermes-preflight-x")
	if args[len(args)-1] != imported.Definition.Source.Image {
		t.Fatalf("duplicate allowed dir appended: %q", args)
	}
}

func TestRestampFromMCPListIgnoresClaimedImportTools(t *testing.T) {
	artifact := importedReviewPacket(t)
	claimed := artifact.Definition.Tools[0].Name
	listed := []ToolSpec{{Name: "resolve-library-id", Effect: ReadEffect}}
	packet, contract, err := restampFromMCPList(context.Background(), artifact, func(context.Context, ImportedArtifact) ([]ToolSpec, error) {
		return listed, nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if contract.Tools[0].Name != "resolve-library-id" || packet.Definition.Tools[0].Name == claimed {
		t.Fatalf("claimed import tools survived preflight: %+v", packet.Definition.Tools)
	}
}

func TestPreflightHelpersFailClosedBeforeDocker(t *testing.T) {
	bad := importedReviewPacket(t)
	bad.Definition.Source.Image = "bad image"
	if _, err := listMCPToolsFromLocalImage(context.Background(), bad); err == nil {
		t.Fatal("invalid local image reached Docker preflight")
	}
	if got := discoverMCPHelpCredentials(context.Background(), bad); got != nil {
		t.Fatalf("invalid local image produced credentials: %+v", got)
	}
	for _, body := range []string{
		"",
		`{"jsonrpc":"2.0","id":2,"error":{"message":"denied"}}`,
		`{"jsonrpc":"2.0","id":2,"result":{"tools":[]}}`,
		`{"jsonrpc":"2.0","id":2,"result":{"tools":[{"name":"bad name"}]}}`,
	} {
		if _, err := decodeMCPToolList(strings.NewReader(body)); err == nil {
			t.Fatalf("unsafe tools/list accepted: %q", body)
		}
	}
}

func TestMCPPreflightUsesIsolatedDockerToolsList(t *testing.T) {
	dir := t.TempDir()
	name := "docker"
	script := "#!/bin/sh\nprintf '%s\\n' '{\"jsonrpc\":\"2.0\",\"id\":2,\"result\":{\"tools\":[{\"name\":\"read\",\"annotations\":{\"readOnlyHint\":true}}]}}'\nexec cat >/dev/null\n"
	if runtime.GOOS == "windows" {
		name = "docker.cmd"
		script = "@echo off\r\necho {\"jsonrpc\":\"2.0\",\"id\":2,\"result\":{\"tools\":[{\"name\":\"read\",\"annotations\":{\"readOnlyHint\":true}}]}}\r\n"
	}
	docker := filepath.Join(dir, name)
	if err := os.WriteFile(docker, []byte(script), 0700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
	packet := importedReviewPacket(t)
	packet.Definition.Source.Image = "ghcr.io/acme/weather"
	packet.Definition.Credentials = []CredentialInput{{Name: "API_TOKEN", Required: true}}
	tools, err := listMCPToolsFromLocalImage(context.Background(), packet)
	if err != nil || len(tools) != 1 || tools[0].Name != "read" || tools[0].Effect != ReadEffect {
		t.Fatalf("isolated tools/list: %+v %v", tools, err)
	}
}

func TestAttachConfirmedToolContractIsRequiredForTrust(t *testing.T) {
	artifact := importedReviewPacket(t)
	store := NewStore()
	if err := store.RegisterTrustedArtifact(artifact, artifact.Definition.Source.ReviewDigest); err == nil {
		t.Fatal("claimed ArtifactImportConfig tools registered without a confirmed contract")
	}
	claimed := ConfirmedToolContract{Source: ToolContractPreflight, Tools: artifact.Definition.Tools}
	if err := AttachConfirmedToolContract(&artifact, claimed); err != nil {
		t.Fatal(err)
	}
	if artifact.Definition.Source.ToolContractDigest == "" || artifact.Definition.Source.ToolContractSource != ToolContractPreflight {
		t.Fatal("contract digest was not recorded")
	}
	if err := store.RegisterTrustedArtifact(artifact, artifact.Definition.Source.ReviewDigest); err != nil {
		t.Fatalf("matching confirmed contract rejected: %v", err)
	}
}

func TestAttachConfirmedToolContractRejectsMismatches(t *testing.T) {
	artifact := importedReviewPacket(t)
	for name, contract := range map[string]ConfirmedToolContract{
		"empty-source": {Tools: artifact.Definition.Tools},
		"claimed-list": {Source: "artifact-import-config", Tools: artifact.Definition.Tools},
		"extra-tool": {Source: ToolContractPreflight, Tools: []ToolSpec{
			artifact.Definition.Tools[0],
			{Name: "other", Effect: ReadEffect},
		}},
		"effect-drift": {Source: ToolContractReviewManifest, Tools: []ToolSpec{{Name: artifact.Definition.Tools[0].Name, Effect: WriteEffect}}},
		"empty-tools":  {Source: ToolContractPreflight},
	} {
		t.Run(name, func(t *testing.T) {
			candidate := artifact
			if err := AttachConfirmedToolContract(&candidate, contract); err == nil {
				t.Fatal("unsafe tool contract accepted")
			}
		})
	}
	if err := AttachConfirmedToolContract(nil, ConfirmedToolContract{Source: ToolContractPreflight, Tools: artifact.Definition.Tools}); err == nil {
		t.Fatal("nil packet accepted")
	}
}

func TestConfirmedToolContractDigestIsStableAndCanonical(t *testing.T) {
	tools := []ToolSpec{{Name: "b-tool", Effect: WriteEffect}, {Name: "a-tool", Effect: ReadEffect}}
	left, err := confirmedToolContractDigest(ConfirmedToolContract{Source: ToolContractPreflight, Tools: tools})
	if err != nil {
		t.Fatal(err)
	}
	right, err := confirmedToolContractDigest(ConfirmedToolContract{Source: ToolContractPreflight, Tools: []ToolSpec{tools[1], tools[0]}})
	if err != nil || left != right || !strings.HasPrefix(left, "sha256:") {
		t.Fatalf("canonical digest drifted: %q %q %v", left, right, err)
	}
	other, err := confirmedToolContractDigest(ConfirmedToolContract{Source: ToolContractReviewManifest, Tools: []ToolSpec{tools[1], tools[0]}})
	if err != nil || other == left {
		t.Fatal("preflight and review-manifest contracts must not share a digest")
	}
}

func importedReviewPacket(t *testing.T) ImportedArtifact {
	t.Helper()
	definition := statefulContainerDefinition()
	definition.Source.Repository = "https://github.com/example/mcp"
	definition.Source.CommitSHA = "0123456789012345678901234567890123456789"
	definition.Source.ArchiveDigest = "sha256:" + repeatHex('a')
	definition.Source.ProvenanceDigest = "sha256:" + repeatHex('b')
	definition.Source.SBOMDigest = "sha256:" + repeatHex('c')
	definition.Source.RecipeDigest = "sha256:" + repeatHex('d')
	artifact := ImportedArtifact{Definition: definition, Recipe: ArtifactRecipe{Format: "dockerfile-v1", Dockerfile: ".hub/Dockerfile", Entrypoint: []string{"/app/server"}}, Artifact: StoredOCIArtifact{ArchiveDigest: definition.Source.ArchiveDigest, Evidence: OCIArtifactEvidence{ImageManifestDigest: definition.Source.Digest, ProvenanceDigest: definition.Source.ProvenanceDigest, SBOMDigest: definition.Source.SBOMDigest}}}
	config := ArtifactImportConfig{DefinitionID: definition.DefinitionID, Version: definition.Version, Image: definition.Source.Image, Tools: definition.Tools, Credentials: definition.Credentials, Environment: definition.Environment, Workload: definition.Workload, Execution: definition.Execution, Health: definition.Health}
	digest, err := reviewDigest(ArtifactSource{Repository: definition.Source.Repository, CommitSHA: definition.Source.CommitSHA}, artifact.Recipe, artifact.Artifact, config)
	if err != nil {
		t.Fatal(err)
	}
	artifact.Definition.Source.ReviewDigest = digest
	return artifact
}

func TestPreflightRetainsSchemaAndBoundsDescription(t *testing.T) {
	body := `{"id":2,"result":{"tools":[{"name":"read","description":"repository read","inputSchema":{"type":"object","properties":{"owner":{"type":"string"}}}}]}}`
	tools, err := decodeMCPToolList(strings.NewReader(body))
	if err != nil || len(tools) != 1 || tools[0].Description != "repository read" || !strings.Contains(string(tools[0].InputSchema), `"owner"`) {
		t.Fatalf("schema lost: %+v %v", tools, err)
	}
	definition := ToolDefinition{Source: DefinitionSource{ToolContractSource: ToolContractPreflight}, Tools: tools}
	if !completeToolSchemas(definition) {
		t.Fatal("complete schema rejected")
	}
	definition.Tools[0].InputSchema = nil
	if completeToolSchemas(definition) {
		t.Fatal("stale schema-less preflight reused")
	}
	body = strings.ReplaceAll(body, "repository read", strings.Repeat("x", 1025))
	tools, err = decodeMCPToolList(strings.NewReader(body))
	if err != nil || tools[0].Description != "" {
		t.Fatal("long prose rejected schema")
	}
	body = strings.ReplaceAll(body, `"owner"`, `"`+strings.Repeat("x", 65536)+`"`)
	if _, err = decodeMCPToolList(strings.NewReader(body)); err == nil {
		t.Fatal("oversized schema accepted")
	}
}
