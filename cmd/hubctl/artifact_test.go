package main

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/letya999/hermes-hub/internal/toolhub"
)

func TestArtifactCommandRejectsIncompleteInvocation(t *testing.T) {
	for _, args := range [][]string{nil, {"unknown"}, {"import"}, {"preflight"}, {"approve"}} {
		if err := runArtifact(context.Background(), args); err == nil {
			t.Fatalf("artifact command %v unexpectedly succeeded", args)
		}
	}
}

func TestArtifactApproveRequiresConfirmedContract(t *testing.T) {
	root := t.TempDir()
	packet := filepath.Join(root, "packet.json")
	if err := os.WriteFile(packet, []byte(`{}`), 0600); err != nil {
		t.Fatal(err)
	}
	store := filepath.Join(root, "store.json")
	args := []string{"approve", "--packet", packet, "--store", store, "--review-digest", "sha256:" + "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"}
	if err := runArtifact(context.Background(), args); err == nil {
		t.Fatal("approve without a confirmed tool contract succeeded")
	}
	if err := runArtifact(context.Background(), []string{"linux-bridge"}); err == nil {
		t.Fatal("linux-bridge without output succeeded")
	}
	if err := runArtifact(context.Background(), []string{"linux-bridge", "--output", "relative"}); err == nil {
		t.Fatal("relative linux-bridge output accepted")
	}
}

func TestArtifactApproveRegistersOnlyWithMatchingContract(t *testing.T) {
	root := t.TempDir()
	definition := toolhub.ToolDefinition{
		Schema: 1, DefinitionID: "stateful-mcp", Version: "2.1.0", Transport: "container-mcp",
		Source: toolhub.DefinitionSource{
			Image: "ghcr.io/example/stateful-mcp", Digest: "sha256:0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef",
			Command: "/app/server", Repository: "https://github.com/example/mcp", CommitSHA: "0123456789012345678901234567890123456789",
			ArchiveDigest: "sha256:" + repeatHex('a'), ProvenanceDigest: "sha256:" + repeatHex('b'),
			SBOMDigest: "sha256:" + repeatHex('c'), RecipeDigest: "sha256:" + repeatHex('d'),
		},
		Tools:       []toolhub.ToolSpec{{Name: "read", Effect: "read"}},
		Credentials: []toolhub.CredentialInput{{Name: "SERVICE_TOKEN", Required: true}},
		Workload:    toolhub.WorkloadPolicy{Class: "per-user", Stateful: true, Rationale: "Provider session and durable connector state are user-owned", ToolHiveVersion: "v0.48.0", SidecarImages: []string{"ghcr.io/stacklok/toolhive/egress-proxy@sha256:0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"}},
		Execution:   toolhub.ExecutionPolicy{TimeoutSeconds: 60, OutputBytes: 2 << 20, CPUMillis: 1000, MemoryMiB: 512, MaxPIDs: 64, Egress: []string{"service.example.com"}, Mounts: []toolhub.Mount{{Source: "connection-state", Target: "/state"}}},
		Health:      toolhub.HealthProbe{Kind: "exec", Value: "/app/health", TimeoutSeconds: 5},
	}
	packet := toolhub.ImportedArtifact{Definition: definition, Recipe: toolhub.ArtifactRecipe{Format: "dockerfile-v1", Dockerfile: ".hub/Dockerfile", Entrypoint: []string{"/app/server"}}, Artifact: toolhub.StoredOCIArtifact{ArchiveDigest: definition.Source.ArchiveDigest, Evidence: toolhub.OCIArtifactEvidence{ImageManifestDigest: definition.Source.Digest, ProvenanceDigest: definition.Source.ProvenanceDigest, SBOMDigest: definition.Source.SBOMDigest}}}
	store := toolhub.NewStore()
	if err := store.RegisterTrustedArtifact(packet, "pending"); err == nil {
		t.Fatal("packet without review digest registered")
	}
	// Drive the shipped approve CLI: missing contract already covered; mismatched contract must fail.
	packetPath := filepath.Join(root, "packet.json")
	body, err := json.Marshal(packet)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(packetPath, body, 0600); err != nil {
		t.Fatal(err)
	}
	contractPath := filepath.Join(root, "contract.json")
	if err := os.WriteFile(contractPath, []byte(`{"source":"preflight-list","tools":[{"name":"other","effect":"read"}]}`), 0600); err != nil {
		t.Fatal(err)
	}
	if err := runArtifact(context.Background(), []string{"approve", "--packet", packetPath, "--store", filepath.Join(root, "store.json"), "--review-digest", "sha256:" + repeatHex('e'), "--contract", contractPath}); err == nil {
		t.Fatal("mismatched tool contract approved")
	}
}

func TestArtifactCLIErrorPaths(t *testing.T) {
	root := t.TempDir()
	ctx := context.Background()
	if err := runArtifact(ctx, []string{"import", "--repository", "https://github.com/example/mcp", "--commit", "0123456789012345678901234567890123456789", "--config", filepath.Join(root, "missing.json"), "--artifacts", root}); err == nil {
		t.Fatal("import missing config succeeded")
	}
	badConfig := filepath.Join(root, "bad.json")
	if err := os.WriteFile(badConfig, []byte(`{"nope":true}`), 0600); err != nil {
		t.Fatal(err)
	}
	if err := runArtifact(ctx, []string{"import", "--repository", "https://github.com/example/mcp", "--commit", "0123456789012345678901234567890123456789", "--config", badConfig, "--artifacts", root}); err == nil {
		t.Fatal("import unknown config field succeeded")
	}
	if err := runArtifact(ctx, []string{"preflight"}); err == nil {
		t.Fatal("preflight without flags succeeded")
	}
	if err := runArtifact(ctx, []string{"preflight", "--packet", filepath.Join(root, "missing.json"), "--artifacts", root, "--contract", filepath.Join(root, "c.json"), "--output-packet", filepath.Join(root, "out.json")}); err == nil {
		t.Fatal("preflight missing packet succeeded")
	}
	packetPath := filepath.Join(root, "packet.json")
	if err := os.WriteFile(packetPath, []byte(`{"nope":1}`), 0600); err != nil {
		t.Fatal(err)
	}
	if err := runArtifact(ctx, []string{"preflight", "--packet", packetPath, "--artifacts", root, "--contract", filepath.Join(root, "c.json"), "--output-packet", filepath.Join(root, "out.json")}); err == nil {
		t.Fatal("preflight unknown packet field succeeded")
	}
	review := "sha256:" + repeatHex('e')
	definition := toolhub.ToolDefinition{
		Schema: 1, DefinitionID: "cli-mcp", Version: "1.0.0", Transport: "container-mcp",
		Source: toolhub.DefinitionSource{
			Image: "ghcr.io/example/cli-mcp", Digest: "sha256:" + repeatHex('1'), Command: "/app/server",
			Repository: "https://github.com/example/mcp", CommitSHA: "0123456789012345678901234567890123456789",
			ArchiveDigest: "sha256:" + repeatHex('a'), ProvenanceDigest: "sha256:" + repeatHex('b'),
			SBOMDigest: "sha256:" + repeatHex('c'), RecipeDigest: "sha256:" + repeatHex('d'), ReviewDigest: review,
		},
		Tools:     []toolhub.ToolSpec{{Name: "read", Effect: "read"}},
		Workload:  toolhub.WorkloadPolicy{Class: "per-user", Stateful: true, Rationale: "state", ToolHiveVersion: "v0.48.0", SidecarImages: []string{"ghcr.io/stacklok/toolhive/egress-proxy@sha256:" + repeatHex('f')}},
		Execution: toolhub.ExecutionPolicy{TimeoutSeconds: 60, OutputBytes: 2 << 20, CPUMillis: 1000, MemoryMiB: 512, MaxPIDs: 64, Egress: []string{"example.com"}},
		Health:    toolhub.HealthProbe{Kind: "exec", Value: "/app/health", TimeoutSeconds: 5},
	}
	body, err := json.Marshal(toolhub.ImportedArtifact{Definition: definition})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(packetPath, body, 0600); err != nil {
		t.Fatal(err)
	}
	if err := runArtifact(ctx, []string{"approve", "--packet", filepath.Join(root, "missing-packet.json"), "--store", filepath.Join(root, "store.json"), "--review-digest", review, "--contract", filepath.Join(root, "c.json")}); err == nil {
		t.Fatal("approve missing packet succeeded")
	}
	if err := runArtifact(ctx, []string{"approve", "--packet", packetPath, "--store", filepath.Join(root, "store.json"), "--review-digest", "sha256:" + repeatHex('0'), "--contract", filepath.Join(root, "c.json")}); err == nil {
		t.Fatal("approve digest mismatch succeeded")
	}
	if err := os.WriteFile(filepath.Join(root, "c.json"), []byte(`{"nope":true}`), 0600); err != nil {
		t.Fatal(err)
	}
	if err := runArtifact(ctx, []string{"approve", "--packet", packetPath, "--store", filepath.Join(root, "store.json"), "--review-digest", review, "--contract", filepath.Join(root, "c.json")}); err == nil {
		t.Fatal("approve unknown contract field succeeded")
	}
	if err := runArtifact(ctx, []string{"linux-bridge", "--output", filepath.Join(root, "bridge"), "extra"}); err == nil {
		t.Fatal("linux-bridge extra args succeeded")
	}
	contract := toolhub.ConfirmedToolContract{Source: toolhub.ToolContractPreflight, Tools: []toolhub.ToolSpec{{Name: "read", Effect: "read"}}}
	contractBody, err := json.Marshal(contract)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "ok-contract.json"), contractBody, 0600); err != nil {
		t.Fatal(err)
	}
	if err := runArtifact(ctx, []string{"approve", "--packet", packetPath, "--store", filepath.Join(root, "ok-store.json"), "--review-digest", review, "--contract", filepath.Join(root, "ok-contract.json")}); err == nil {
		t.Fatal("incomplete review packet approved")
	}
	validConfig := filepath.Join(root, "valid-import.json")
	if err := os.WriteFile(validConfig, []byte(`{"DefinitionID":"cli-mcp","Version":"1.0.0","Image":"repo@sha256:bad"}`), 0600); err != nil {
		t.Fatal(err)
	}
	if err := runArtifact(ctx, []string{"import", "--repository", "https://github.com/example/mcp", "--commit", "0123456789012345678901234567890123456789", "--config", validConfig, "--artifacts", root}); err == nil {
		t.Fatal("import with digest-tagged image succeeded")
	}
	emptyPacket := filepath.Join(root, "empty-packet.json")
	if err := os.WriteFile(emptyPacket, []byte(`{}`), 0600); err != nil {
		t.Fatal(err)
	}
	if err := runArtifact(ctx, []string{"preflight", "--packet", emptyPacket, "--artifacts", root, "--contract", filepath.Join(root, "preflight-c.json"), "--output-packet", filepath.Join(root, "preflight-out.json")}); err == nil {
		t.Fatal("preflight empty packet succeeded")
	}
	storeDir := filepath.Join(root, "store-dir")
	if err := os.Mkdir(storeDir, 0700); err != nil {
		t.Fatal(err)
	}
	if err := runArtifact(ctx, []string{"approve", "--packet", packetPath, "--store", storeDir, "--review-digest", review, "--contract", filepath.Join(root, "ok-contract.json")}); err == nil {
		t.Fatal("approve with directory store succeeded")
	}
}

func TestWriteJSONFileRoundTripAndRejects(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "out.json")
	if err := writeJSONFile(path, map[string]string{"k": "v"}); err != nil {
		t.Fatal(err)
	}
	body, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var got map[string]string
	if err := json.Unmarshal(body, &got); err != nil || got["k"] != "v" {
		t.Fatalf("writeJSONFile round-trip: %s %v", body, err)
	}
	if err := writeJSONFile(path, make(chan int)); err == nil {
		t.Fatal("writeJSONFile accepted a value that cannot marshal")
	}
}

func TestArtifactApproveRegistersMatchingReviewPacket(t *testing.T) {
	root := t.TempDir()
	packet := completeCLIReviewPacket(t)
	digest, err := toolhub.ImportedReviewDigest(packet)
	if err != nil {
		t.Fatal(err)
	}
	packet.Definition.Source.ReviewDigest = digest
	packetPath := filepath.Join(root, "packet.json")
	body, err := json.Marshal(packet)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(packetPath, body, 0600); err != nil {
		t.Fatal(err)
	}
	contractPath := filepath.Join(root, "contract.json")
	contractBody, err := json.Marshal(toolhub.ConfirmedToolContract{Source: toolhub.ToolContractPreflight, Tools: packet.Definition.Tools})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(contractPath, contractBody, 0600); err != nil {
		t.Fatal(err)
	}
	storePath := filepath.Join(root, "store.json")
	if err := runArtifact(context.Background(), []string{"approve", "--packet", packetPath, "--store", storePath, "--review-digest", digest, "--contract", contractPath}); err != nil {
		t.Fatalf("matching review packet rejected: %v", err)
	}
	saved, err := os.ReadFile(storePath)
	if err != nil {
		t.Fatal(err)
	}
	text := strings.ReplaceAll(string(saved), " ", "")
	if !strings.Contains(text, `"definition_id":"cli-mcp"`) || !strings.Contains(text, `"tool_contract_source":"preflight-list"`) {
		t.Fatalf("approved store missing trusted definition: %s", saved)
	}
}

func completeCLIReviewPacket(t *testing.T) toolhub.ImportedArtifact {
	t.Helper()
	definition := toolhub.ToolDefinition{
		Schema: 1, DefinitionID: "cli-mcp", Version: "1.0.0", Transport: "container-mcp",
		Source: toolhub.DefinitionSource{
			Image: "ghcr.io/example/cli-mcp", Digest: "sha256:" + repeatHex('1'), Command: "/app/server",
			Repository: "https://github.com/example/mcp", CommitSHA: "0123456789012345678901234567890123456789",
			ArchiveDigest: "sha256:" + repeatHex('a'), ProvenanceDigest: "sha256:" + repeatHex('b'),
			SBOMDigest: "sha256:" + repeatHex('c'), RecipeDigest: "sha256:" + repeatHex('d'),
		},
		Tools:     []toolhub.ToolSpec{{Name: "read", Effect: "read"}},
		Workload:  toolhub.WorkloadPolicy{Class: "per-user", Stateful: true, Rationale: "state", ToolHiveVersion: "v0.48.0", SidecarImages: []string{"ghcr.io/stacklok/toolhive/egress-proxy@sha256:" + repeatHex('f')}},
		Execution: toolhub.ExecutionPolicy{TimeoutSeconds: 60, OutputBytes: 2 << 20, CPUMillis: 1000, MemoryMiB: 512, MaxPIDs: 64, Egress: []string{"example.com"}, Mounts: []toolhub.Mount{{Source: "connection-state", Target: "/state"}}},
		Health:    toolhub.HealthProbe{Kind: "exec", Value: "/app/health", TimeoutSeconds: 5},
	}
	return toolhub.ImportedArtifact{
		Definition: definition,
		Recipe:     toolhub.ArtifactRecipe{Format: "dockerfile-v1", Dockerfile: ".hub/Dockerfile", Entrypoint: []string{"/app/server"}},
		Artifact:   toolhub.StoredOCIArtifact{ArchiveDigest: definition.Source.ArchiveDigest, Evidence: toolhub.OCIArtifactEvidence{ImageManifestDigest: definition.Source.Digest, ProvenanceDigest: definition.Source.ProvenanceDigest, SBOMDigest: definition.Source.SBOMDigest}},
	}
}

func repeatHex(value byte) string {
	result := make([]byte, 64)
	for i := range result {
		result[i] = value
	}
	return string(result)
}

func TestArtifactImportRejectsUnknownConfigBeforeNetwork(t *testing.T) {
	root := t.TempDir()
	config := filepath.Join(root, "config.json")
	if err := os.WriteFile(config, []byte(`{"unknown":true}`), 0600); err != nil {
		t.Fatal(err)
	}
	args := []string{"import", "--repository", "https://github.com/example/mcp", "--commit", "0123456789012345678901234567890123456789", "--config", config, "--artifacts", root}
	if err := runArtifact(context.Background(), args); err == nil {
		t.Fatal("unknown import config accepted")
	}
}
