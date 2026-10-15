package toolhub

import (
	"context"
	"errors"
	"github.com/letya999/hermes-hub/internal/toolhub/api"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func TestApplyPreflightToolListRestampAndRegisters(t *testing.T) {
	artifact := importedReviewPacket(t)
	claimedDigest := artifact.Definition.Source.ReviewDigest
	listed := []api.ToolSpec{{Name: "get_current_config", Effect: api.ReadEffect}, {Name: "list_dir", Effect: api.ReadEffect}}
	contract, err := api.ApplyPreflightToolList(&artifact, listed)
	if err != nil {
		t.Fatal(err)
	}
	if contract.Source != api.ToolContractPreflight || len(contract.Tools) != 2 {
		t.Fatalf("preflight contract: %+v", contract)
	}
	if artifact.Definition.Source.ReviewDigest == "" || artifact.Definition.Source.ReviewDigest == claimedDigest {
		t.Fatal("review digest was not recomputed from the confirmed tools")
	}
	if artifact.Definition.Source.ToolContractSource != api.ToolContractPreflight || artifact.Definition.Source.ToolContractDigest == "" {
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
	if _, err := api.ApplyPreflightToolList(nil, []api.ToolSpec{{Name: "read", Effect: api.ReadEffect}}); err == nil {
		t.Fatal("nil packet accepted")
	}
	if _, err := api.ApplyPreflightToolList(&artifact, nil); err == nil {
		t.Fatal("empty tools/list accepted")
	}
	if _, err := api.ApplyPreflightToolList(&artifact, []api.ToolSpec{{Name: "Not a tool", Effect: api.ReadEffect}}); err == nil {
		t.Fatal("invalid MCP tool name accepted")
	}
}

func TestDecodeMCPToolListReadsID2Payload(t *testing.T) {
	body := `{"jsonrpc":"2.0","id":1,"result":{"protocolVersion":"2024-11-05"}}
{"jsonrpc":"2.0","id":2,"result":{"tools":[{"name":"API-get-self","annotations":{"readOnlyHint":true}},{"name":"delete_file","annotations":{"destructiveHint":true}},{"name":"unknown_effect"}]}}
`
	tools, err := api.DecodeMCPToolList(strings.NewReader(body))
	if err != nil || len(tools) != 3 || tools[0].Name != "API-get-self" || tools[0].Effect != api.ReadEffect || tools[1].Effect != api.WriteEffect || tools[2].Effect != api.WriteEffect {
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
	tools, err := api.DecodeMCPToolList(strings.NewReader(body))
	if err != nil || len(tools) != 1 || tools[0].Name != "conversations_history" || tools[0].Effect != api.ReadEffect {
		t.Fatalf("decode with log lines: %+v %v", tools, err)
	}
}

func TestDecodeMCPToolListReturnsRPCError(t *testing.T) {
	body := `{"jsonrpc":"2.0","id":2,"error":{"code":-32600,"message":"bad request"}}
`
	if _, err := api.DecodeMCPToolList(strings.NewReader(body)); err == nil || !strings.Contains(err.Error(), "bad request") {
		t.Fatalf("expected RPC error, got %v", err)
	}
}

func TestParseMCPHelpCredentialsUsesServerDeclaration(t *testing.T) {
	help := `Environment Variables:
  SERVICE_TOKEN          Provider token (recommended)
  OPENAPI_HEADERS        JSON headers (alternative)
  AUTH_TOKEN             api.Transport authentication (alternative)
  REQUIRED_SECRET        Account secret (required)

Examples:`
	got := api.ParseMCPHelpCredentials(help)
	if len(got) != 2 || got[0].Name != "SERVICE_TOKEN" || got[1].Name != "REQUIRED_SECRET" || !got[0].Required || !got[1].Required {
		t.Fatalf("credentials=%+v", got)
	}
	if got := api.ParseMCPHelpCredentials("Environment Variables:\n  TOKEN optional\n"); len(got) != 0 {
		t.Fatalf("optional credential guessed: %+v", got)
	}
}

func TestRandomPreflightContainerName(t *testing.T) {
	first, err := api.RandomPreflightContainerName("preflight-")
	if err != nil {
		t.Fatal(err)
	}
	second, err := api.RandomPreflightContainerName("preflight-")
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
	args := api.PreflightDockerRunArgs(imported, "hermes-preflight-x", []string{"--rm", "-i"}, api.PreflightCommandArgs(imported))
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
	imported.Definition.Execution.Mounts = []api.Mount{{Source: "connection-state", Target: "/state"}}
	imported.Recipe.Entrypoint = []string{"/app/mcp-server"}
	args = api.PreflightDockerRunArgs(imported, "hermes-preflight-x", []string{"--rm", "-i"}, api.PreflightCommandArgs(imported))
	joined = strings.Join(args, "\x00")
	if !strings.Contains(joined, "/state:rw") || args[len(args)-1] != "/state" {
		t.Fatalf("stateful preflight must pass allowed dir: %q", args)
	}
	imported.Recipe.Entrypoint = []string{"/app/mcp-server", "/state"}
	args = api.PreflightDockerRunArgs(imported, "hermes-preflight-x", []string{"--rm", "-i"}, api.PreflightCommandArgs(imported))
	if args[len(args)-1] != imported.Definition.Source.Image {
		t.Fatalf("duplicate allowed dir appended: %q", args)
	}
	imported.Definition.Source.Args = []string{"--transport", "sse"}
	imported.Definition.Execution.Mounts = nil
	args = api.PreflightDockerRunArgs(imported, "hermes-preflight-x", []string{"--rm", "-i"}, api.PreflightCommandArgs(imported))
	if !strings.Contains(strings.Join(args, " "), "--transport stdio") || strings.Contains(strings.Join(args, " "), "--transport sse") {
		t.Fatalf("network transport was not probed over stdio: %q", args)
	}
}

func TestNetworkTransportValue(t *testing.T) {
	for argv, want := range map[string]string{
		"":                                      "",
		"--stdio":                               "",
		"--transport sse":                       "sse",
		"-t sse":                                "sse",
		"--transport SSE":                       "sse",
		"--transport http":                      "streamable-http",
		"--transport streamable-http":           "streamable-http",
		"--transport streamable_http":           "streamable-http",
		"--verbose --transport sse --port 9000": "sse",
		"--transport":                           "",
		"--other sse":                           "",
	} {
		var args []string
		if argv != "" {
			args = strings.Fields(argv)
		}
		if got := api.NetworkTransportValue(args); got != want {
			t.Fatalf("networkTransportValue(%q) = %q, want %q", argv, got, want)
		}
	}
}

func TestNetworkTransportFallbackMarksAndPropagates(t *testing.T) {
	imported := importedReviewPacket(t)
	if _, _, _, err := api.NetworkTransportFallback(context.Background(), imported, errors.New("stdio died"), nil); err == nil || imported.Definition.Source.NetworkTransport != "" {
		t.Fatal("fallback ran without a declared network transport")
	}
	imported.Definition.Source.Args = []string{"--transport", "sse"}
	saved := api.NetworkMCPToolList
	defer func() { api.NetworkMCPToolList = saved }()
	api.NetworkMCPToolList = func(context.Context, api.ImportedArtifact) ([]api.ToolSpec, error) {
		return []api.ToolSpec{{Name: "read", Effect: api.ReadEffect}}, nil
	}
	tools, marked, list, err := api.NetworkTransportFallback(context.Background(), imported, errors.New("stdio died"), nil)
	if err != nil || len(tools) != 1 || marked.Definition.Source.NetworkTransport != "sse" {
		t.Fatalf("fallback = %v %v %q", tools, err, marked.Definition.Source.NetworkTransport)
	}
	if list == nil {
		t.Fatal("network probe not kept for credential promotion")
	}
	api.NetworkMCPToolList = func(context.Context, api.ImportedArtifact) ([]api.ToolSpec, error) {
		return nil, errors.New("no listener")
	}
	_, unmarked, _, err := api.NetworkTransportFallback(context.Background(), imported, errors.New("stdio died"), nil)
	if err == nil || !strings.Contains(err.Error(), "network transport probe") || unmarked.Definition.Source.NetworkTransport != "" {
		t.Fatalf("failed fallback marked the definition or hid the error: %v %q", err, unmarked.Definition.Source.NetworkTransport)
	}
}

func TestNetworkProbeToolListFailClosed(t *testing.T) {
	bad := importedReviewPacket(t)
	bad.Definition.Source.Image = "bad image"
	if _, err := api.NetworkProbeToolList(context.Background(), bad); err == nil {
		t.Fatal("invalid local image reached network preflight")
	}
	noTransport := importedReviewPacket(t)
	if _, err := api.NetworkProbeToolList(context.Background(), noTransport); !errors.Is(err, api.ErrInvalid) {
		t.Fatalf("network probe ran without a declared transport: %v", err)
	}
	// A declared transport reaches the bridge-staging stage; without a
	// host-visible HUB_STATE mapping it must still fail closed.
	declared := importedReviewPacket(t)
	declared.Definition.Source.Args = []string{"--transport", "sse"}
	t.Setenv("HUB_STATE", t.TempDir())
	t.Setenv("HUB_DOCKER_HOST_ROOT", "")
	if _, err := api.NetworkProbeToolList(context.Background(), declared); err == nil {
		t.Fatal("network probe ran without a stageable bridge binary")
	}
}

func TestStagePreflightBridge(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("HUB_STATE", dir)
	t.Setenv("HUB_DOCKER_HOST_ROOT", "")
	host, cleanup, err := api.StagePreflightBridge()
	defer cleanup()
	if err != nil {
		t.Fatalf("stagePreflightBridge: %v", err)
	}
	if !strings.HasSuffix(host, "hubctl") {
		t.Fatalf("host path = %q", host)
	}
	// The staged file must be an ELF linux binary the daemon can exec.
	entries, _ := os.ReadDir(dir)
	if len(entries) != 1 {
		t.Fatalf("staged dir = %v", entries)
	}
	staged := filepath.Join(dir, entries[0].Name(), "hubctl")
	body, err := os.ReadFile(staged)
	if err != nil || len(body) < 4 || string(body[:4]) != "\x7fELF" {
		t.Fatalf("staged binary not ELF: %v", err)
	}
	info, err := os.Stat(staged)
	if err != nil {
		t.Fatalf("stat staged binary: %v", err)
	}
	if runtime.GOOS != "windows" && info.Mode().Perm()&0111 == 0 {
		t.Fatalf("staged binary not executable: %v", info.Mode())
	}
	// Host-path translation uses the daemon-visible prefix mapping.
	t.Setenv("HUB_DOCKER_HOST_ROOT", "/state=/host/state")
	if got := api.DockerBindSource("/state/x/hubctl"); got != "/host/state/x/hubctl" {
		t.Fatalf("dockerBindSource = %q", got)
	}
}

func TestRestampFromMCPListIgnoresClaimedImportTools(t *testing.T) {
	artifact := importedReviewPacket(t)
	claimed := artifact.Definition.Tools[0].Name
	listed := []api.ToolSpec{{Name: "resolve-library-id", Effect: api.ReadEffect}}
	packet, contract, err := api.RestampFromMCPList(context.Background(), artifact, func(context.Context, api.ImportedArtifact) ([]api.ToolSpec, error) {
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
	if _, err := api.ListMCPToolsFromLocalImage(context.Background(), bad); err == nil {
		t.Fatal("invalid local image reached Docker preflight")
	}
	if got := api.DiscoverMCPHelpCredentials(context.Background(), bad); got != nil {
		t.Fatalf("invalid local image produced credentials: %+v", got)
	}
	for _, body := range []string{
		"",
		`{"jsonrpc":"2.0","id":2,"error":{"message":"denied"}}`,
		`{"jsonrpc":"2.0","id":2,"result":{"tools":[]}}`,
		`{"jsonrpc":"2.0","id":2,"result":{"tools":[{"name":"bad name"}]}}`,
	} {
		if _, err := api.DecodeMCPToolList(strings.NewReader(body)); err == nil {
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
	packet.Definition.Credentials = []api.CredentialInput{{Name: "API_TOKEN", Required: true}}
	tools, err := api.ListMCPToolsFromLocalImage(context.Background(), packet)
	if err != nil || len(tools) != 1 || tools[0].Name != "read" || tools[0].Effect != api.ReadEffect {
		t.Fatalf("isolated tools/list: %+v %v", tools, err)
	}
}

func TestAttachConfirmedToolContractIsRequiredForTrust(t *testing.T) {
	artifact := importedReviewPacket(t)
	store := NewStore()
	if err := store.RegisterTrustedArtifact(artifact, artifact.Definition.Source.ReviewDigest); err == nil {
		t.Fatal("claimed ArtifactImportConfig tools registered without a confirmed contract")
	}
	claimed := api.ConfirmedToolContract{Source: api.ToolContractPreflight, Tools: artifact.Definition.Tools}
	if err := api.AttachConfirmedToolContract(&artifact, claimed); err != nil {
		t.Fatal(err)
	}
	if artifact.Definition.Source.ToolContractDigest == "" || artifact.Definition.Source.ToolContractSource != api.ToolContractPreflight {
		t.Fatal("contract digest was not recorded")
	}
	if err := store.RegisterTrustedArtifact(artifact, artifact.Definition.Source.ReviewDigest); err != nil {
		t.Fatalf("matching confirmed contract rejected: %v", err)
	}
}

func TestAttachConfirmedToolContractRejectsMismatches(t *testing.T) {
	artifact := importedReviewPacket(t)
	for name, contract := range map[string]api.ConfirmedToolContract{
		"empty-source": {Tools: artifact.Definition.Tools},
		"claimed-list": {Source: "artifact-import-config", Tools: artifact.Definition.Tools},
		"extra-tool": {Source: api.ToolContractPreflight, Tools: []api.ToolSpec{
			artifact.Definition.Tools[0],
			{Name: "other", Effect: api.ReadEffect},
		}},
		"effect-drift": {Source: api.ToolContractReviewManifest, Tools: []api.ToolSpec{{Name: artifact.Definition.Tools[0].Name, Effect: api.WriteEffect}}},
		"empty-tools":  {Source: api.ToolContractPreflight},
	} {
		t.Run(name, func(t *testing.T) {
			candidate := artifact
			if err := api.AttachConfirmedToolContract(&candidate, contract); err == nil {
				t.Fatal("unsafe tool contract accepted")
			}
		})
	}
	if err := api.AttachConfirmedToolContract(nil, api.ConfirmedToolContract{Source: api.ToolContractPreflight, Tools: artifact.Definition.Tools}); err == nil {
		t.Fatal("nil packet accepted")
	}
}

func TestConfirmedToolContractDigestIsStableAndCanonical(t *testing.T) {
	tools := []api.ToolSpec{{Name: "b-tool", Effect: api.WriteEffect}, {Name: "a-tool", Effect: api.ReadEffect}}
	left, err := api.ConfirmedToolContractDigest(api.ConfirmedToolContract{Source: api.ToolContractPreflight, Tools: tools})
	if err != nil {
		t.Fatal(err)
	}
	right, err := api.ConfirmedToolContractDigest(api.ConfirmedToolContract{Source: api.ToolContractPreflight, Tools: []api.ToolSpec{tools[1], tools[0]}})
	if err != nil || left != right || !strings.HasPrefix(left, "sha256:") {
		t.Fatalf("canonical digest drifted: %q %q %v", left, right, err)
	}
	other, err := api.ConfirmedToolContractDigest(api.ConfirmedToolContract{Source: api.ToolContractReviewManifest, Tools: []api.ToolSpec{tools[1], tools[0]}})
	if err != nil || other == left {
		t.Fatal("preflight and review-manifest contracts must not share a digest")
	}
}

func importedReviewPacket(t *testing.T) api.ImportedArtifact {
	t.Helper()
	definition := statefulContainerDefinition()
	definition.Source.Repository = "https://github.com/example/mcp"
	definition.Source.CommitSHA = "0123456789012345678901234567890123456789"
	definition.Source.ArchiveDigest = "sha256:" + repeatHex('a')
	definition.Source.ProvenanceDigest = "sha256:" + repeatHex('b')
	definition.Source.SBOMDigest = "sha256:" + repeatHex('c')
	definition.Source.RecipeDigest = "sha256:" + repeatHex('d')
	artifact := api.ImportedArtifact{Definition: definition, Recipe: api.ArtifactRecipe{Format: "dockerfile-v1", Dockerfile: ".hub/Dockerfile", Entrypoint: []string{"/app/server"}}, Artifact: api.StoredOCIArtifact{ArchiveDigest: definition.Source.ArchiveDigest, Evidence: api.OCIArtifactEvidence{ImageManifestDigest: definition.Source.Digest, ProvenanceDigest: definition.Source.ProvenanceDigest, SBOMDigest: definition.Source.SBOMDigest}}}
	config := api.ArtifactImportConfig{DefinitionID: definition.DefinitionID, Version: definition.Version, Image: definition.Source.Image, Tools: definition.Tools, Credentials: definition.Credentials, Environment: definition.Environment, Workload: definition.Workload, Execution: definition.Execution, Health: definition.Health}
	digest, err := api.ReviewDigest(api.ArtifactSource{Repository: definition.Source.Repository, CommitSHA: definition.Source.CommitSHA}, artifact.Recipe, artifact.Artifact, config)
	if err != nil {
		t.Fatal(err)
	}
	artifact.Definition.Source.ReviewDigest = digest
	return artifact
}

func TestDecodeMCPToolListKeepsAuthTextWhenNoToolsReply(t *testing.T) {
	body := "Authentication required: Either SLACK_MCP_XOXP_TOKEN or SLACK_MCP_XOXB_TOKEN must be provided\n{\"level\":\"fatal\",\"error\":\"authentication required\"}\n"
	_, err := api.DecodeMCPToolList(strings.NewReader(body))
	if err == nil || !strings.Contains(err.Error(), "SLACK_MCP_XOXP_TOKEN") || !strings.Contains(err.Error(), "authentication required") {
		t.Fatalf("auth text dropped: %v", err)
	}
}

func TestPreflightRetainsSchemaAndBoundsDescription(t *testing.T) {
	body := `{"id":2,"result":{"tools":[{"name":"read","description":"repository read","inputSchema":{"type":"object","properties":{"owner":{"type":"string"}}}}]}}`
	tools, err := api.DecodeMCPToolList(strings.NewReader(body))
	if err != nil || len(tools) != 1 || tools[0].Description != "repository read" || !strings.Contains(string(tools[0].InputSchema), `"owner"`) {
		t.Fatalf("schema lost: %+v %v", tools, err)
	}
	definition := api.ToolDefinition{Source: api.DefinitionSource{ToolContractSource: api.ToolContractPreflight}, Tools: tools}
	if !completeToolSchemas(definition) {
		t.Fatal("complete schema rejected")
	}
	definition.Tools[0].InputSchema = nil
	if completeToolSchemas(definition) {
		t.Fatal("stale schema-less preflight reused")
	}
	body = strings.ReplaceAll(body, "repository read", strings.Repeat("x", 1025))
	tools, err = api.DecodeMCPToolList(strings.NewReader(body))
	if err != nil || tools[0].Description != "" {
		t.Fatal("long prose rejected schema")
	}
	body = strings.ReplaceAll(body, `"owner"`, `"`+strings.Repeat("x", 65536)+`"`)
	if _, err = api.DecodeMCPToolList(strings.NewReader(body)); err == nil {
		t.Fatal("oversized schema accepted")
	}
}
