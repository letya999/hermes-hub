package api

import (
	"context"
	"fmt"
	"path/filepath"
	"strings"
	"testing"
)

// S5.1: a prepared entry may declare its tier and a CI-built image pin —
// the runtime then admits the pinned image instead of rebuilding.
func TestPreparedEntryTierAndImagePin(t *testing.T) {
	digest := "sha256:" + strings.Repeat("b", 64)
	body := `[{
		"id": "notion", "name": "Notion", "license": "MIT", "tier": "t0",
		"image": {"ref": "ghcr.io/letya999/hermes-hub-connectors/notion@` + digest + `", "digest": "` + digest + `",
			"provenance": "attestation://notion", "sbom": "sbom://notion"},
		"source": {"repository": "https://github.com/makenotion/notion-mcp-server", "commit_sha": "730ae781ba28beeaf0865025a3f2ed4c25ea2387"},
		"language": "node", "entrypoint": ["node", "/app/bin/cli.mjs"],
		"connection": {"fields": [{"name": "NOTION_TOKEN", "type": "token", "required": true, "secret": true, "delivery": "env", "target": "NOTION_TOKEN"}]},
		"contract_id": "notion-token", "contract_revision": 1,
		"egress": ["api.notion.com"], "read_tools": ["API-get-self"], "probe_tool": "API-get-self",
		"runbook": "docs/prepared-connectors.md#notion",
		"handoff": "Install notion."
	}]`
	entries, err := ParsePreparedCatalog([]byte(body))
	if err != nil {
		t.Fatal(err)
	}
	entry := entries[0]
	if entry.Tier != TierT0 || entry.Image.Digest != digest {
		t.Fatalf("entry=%+v", entry)
	}
	config := NormalizeArtifactImportConfig(entry.Apply(DefaultSelfInstallConfig(entry.Source)))
	if config.Image != "ghcr.io/letya999/hermes-hub-connectors/notion" {
		t.Fatalf("pinned image not applied: %q", config.Image)
	}
}

// S5.1: tier is an enum and the image pin must be a sha256 digest — a
// mutable tag or an unknown tier fails catalog validation closed.
func TestPreparedEntryTierValidation(t *testing.T) {
	entry := func(extra string) string {
		return `[{
			"id": "notion", "name": "Notion", "license": "MIT", ` + extra + `,
			"source": {"repository": "https://github.com/makenotion/notion-mcp-server", "commit_sha": "730ae781ba28beeaf0865025a3f2ed4c25ea2387"},
			"language": "node", "entrypoint": ["node", "/app/bin/cli.mjs"],
			"connection": {"fields": [{"name": "NOTION_TOKEN", "type": "token", "required": true, "secret": true, "delivery": "env", "target": "NOTION_TOKEN"}]},
			"contract_id": "notion-token", "contract_revision": 1,
			"egress": ["api.notion.com"], "read_tools": ["API-get-self"], "probe_tool": "API-get-self",
			"runbook": "docs/prepared-connectors.md#notion",
			"handoff": "Install notion."
		}]`
	}
	if _, err := ParsePreparedCatalog([]byte(entry(`"tier": "bogus"`))); err == nil {
		t.Fatal("unknown tier accepted")
	}
	if _, err := ParsePreparedCatalog([]byte(entry(`"image": {"ref": "ghcr.io/x/notion", "digest": "latest"}`))); err == nil {
		t.Fatal("mutable image digest accepted")
	}
}

// S5.2: T0 admission is born pinned — a prepared entry that carries a
// CI-built image digest never reaches the restricted builder.
func TestT0AdmissionSkipsBuilder(t *testing.T) {
	digest := "sha256:" + strings.Repeat("b", 64)
	provenance := "sha256:" + strings.Repeat("c", 64)
	sbom := "sha256:" + strings.Repeat("d", 64)
	entry := PreparedEntry{
		ID: "notion", Name: "Notion", Tier: TierT0,
		Image:      &PinnedImage{Ref: "ghcr.io/letya999/hermes-hub-connectors/notion@" + digest, Digest: digest, Provenance: provenance, SBOM: sbom},
		Source:     ArtifactSource{Repository: "https://github.com/makenotion/notion-mcp-server", CommitSHA: "730ae781ba28beeaf0865025a3f2ed4c25ea2387"},
		Language:   "node",
		Entrypoint: []string{"node", "/app/bin/cli.mjs"},
		Connection: ConnectionRecipe{Fields: []ConnectionField{{Name: "NOTION_TOKEN", Type: "token", Required: true, Secret: true, Delivery: "env", Target: "NOTION_TOKEN"}}},
		ContractID: "notion-token", ContractRevision: 1,
		Egress:     []string{"api.notion.com"},
	}
	oldLookup, oldBuild, oldPull, oldList := preparedLookup, restrictedBuild, PublishedImagePull, PublishedMCPToolList
	t.Cleanup(func() { preparedLookup, restrictedBuild, PublishedImagePull, PublishedMCPToolList = oldLookup, oldBuild, oldPull, oldList })
	preparedLookup = func(ArtifactSource) (PreparedEntry, bool, error) { return entry, true, nil }
	builds := 0
	restrictedBuild = func(context.Context, ArtifactRecipe, []byte, RestrictedBuildConfig) (StoredOCIArtifact, error) {
		builds++
		return StoredOCIArtifact{}, fmt.Errorf("build must not run for a pinned image")
	}
	PublishedImagePull = func(context.Context, string, string) error { return nil }
	PublishedMCPToolList = func(context.Context, ImportedArtifact) ([]ToolSpec, error) {
		return []ToolSpec{{Name: "API-get-self", Effect: ReadEffect}}, nil
	}
	dir := t.TempDir()
	seccomp, err := filepath.Abs(filepath.Join("..", "..", "..", "docker", "seccomp-buildkit-rootless.json"))
	if err != nil {
		t.Fatal(err)
	}
	review, err := DefaultSourceReviewerWithCatalogs(dir, seccomp, nil)(context.Background(), entry.Source, nil)
	if err != nil {
		t.Fatal(err)
	}
	if builds != 0 || review.Definition.Source.Digest != digest {
		t.Fatalf("builds=%d digest=%q", builds, review.Definition.Source.Digest)
	}
}
