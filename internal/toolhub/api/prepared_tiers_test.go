package api

import (
	"context"
	"errors"
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
		Egress: []string{"api.notion.com"},
	}
	oldLookup, oldBuild, oldPull, oldList := preparedLookup, restrictedBuild, PublishedImagePull, PublishedMCPToolList
	t.Cleanup(func() {
		preparedLookup, restrictedBuild, PublishedImagePull, PublishedMCPToolList = oldLookup, oldBuild, oldPull, oldList
	})
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

// Issue #282: a T0 admission is still a resolution — the reviewed catalog's
// launch and credential claims must land in the source ledger at the top
// ranks, or re-resolution cannot explain where the recipe came from.
func TestT0AdmissionRecordsPreparedClaimsInLedger(t *testing.T) {
	digest := "sha256:" + strings.Repeat("b", 64)
	entry := PreparedEntry{
		ID: "notion", Name: "Notion", Tier: TierT0,
		Image:      &PinnedImage{Ref: "ghcr.io/letya999/hermes-hub-connectors/notion@" + digest, Digest: digest, Provenance: "sha256:" + strings.Repeat("c", 64), SBOM: "sha256:" + strings.Repeat("d", 64)},
		Source:     ArtifactSource{Repository: "https://github.com/makenotion/notion-mcp-server", CommitSHA: "730ae781ba28beeaf0865025a3f2ed4c25ea2387"},
		Language:   "node",
		Entrypoint: []string{"node", "/app/bin/cli.mjs"},
		Connection: ConnectionRecipe{Fields: []ConnectionField{{Name: "NOTION_TOKEN", Type: "token", Required: true, Secret: true, Delivery: "env", Target: "NOTION_TOKEN"}}},
		ContractID: "notion-token", ContractRevision: 1,
		Egress: []string{"api.notion.com"},
	}
	oldLookup, oldPull, oldList := preparedLookup, PublishedImagePull, PublishedMCPToolList
	t.Cleanup(func() { preparedLookup, PublishedImagePull, PublishedMCPToolList = oldLookup, oldPull, oldList })
	preparedLookup = func(ArtifactSource) (PreparedEntry, bool, error) { return entry, true, nil }
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
	if review.Recipe == nil || len(review.Recipe.Ledger) == 0 {
		t.Fatalf("T0 resolution recorded no source ledger: %+v", review.Recipe)
	}
	var artifact, field bool
	for _, e := range review.Recipe.Ledger {
		if e.Ignored || e.Source != "prepared" {
			continue
		}
		artifact = artifact || e.Claim.Attribute == "artifact" && e.Rank == RankL0
		field = field || e.Claim.Field == "NOTION_TOKEN" && e.Rank == RankR0
	}
	if !artifact || !field {
		t.Fatalf("prepared claims missing from ledger: %+v", review.Recipe.Ledger)
	}
	if review.Recipe.Trust != TrustT0 {
		t.Fatalf("reviewed L0/R0 claims must resolve T0 trust, got %q", review.Recipe.Trust)
	}
}

// Issue #282: a T1 admission consults the hub catalog too — its reviewed
// credential contract is the R0 authority and must be a ledger claim, or the
// form cannot explain why those exact fields are the required ones.
func TestT1AdmissionRecordsPreparedClaimsInLedger(t *testing.T) {
	entry := PreparedEntry{
		ID: "weather", Name: "Weather", Tier: TierT1,
		Source:     ArtifactSource{Repository: "https://github.com/acme/weather", CommitSHA: "730ae781ba28beeaf0865025a3f2ed4c25ea2387"},
		Language:   "node",
		Entrypoint: []string{"node", "/app/index.js"},
		Connection: ConnectionRecipe{Fields: []ConnectionField{{Name: "WEATHER_TOKEN", Type: "token", Required: true, Secret: true, Delivery: "env", Target: "WEATHER_TOKEN"}}},
		ContractID: "weather-token", ContractRevision: 1,
		Egress: []string{"api.weather.example"},
	}
	contextBytes := recipeContextTar(t, map[string]string{
		"package.json":      `{"name":"weather","bin":"index.js"}`,
		"package-lock.json": `{"lockfileVersion":3,"packages":{}}`,
		"index.js":          `console.log("weather")`,
	})
	old := []any{preparedLookup, fetchRecipeContext, fetchArtifactContext, restrictedBuild, StoredArtifactLoader, LocalMCPToolList}
	t.Cleanup(func() {
		preparedLookup, fetchRecipeContext, fetchArtifactContext, restrictedBuild, StoredArtifactLoader, LocalMCPToolList =
			old[0].(func(ArtifactSource) (PreparedEntry, bool, error)),
			old[1].(func(context.Context, ArtifactSource, int64) ([]byte, error)),
			old[2].(func(context.Context, ArtifactSource, int64) ([]byte, error)),
			old[3].(func(context.Context, ArtifactRecipe, []byte, RestrictedBuildConfig) (StoredOCIArtifact, error)),
			old[4].(func(context.Context, string, string, string, int64) (string, error)),
			old[5].(func(context.Context, ImportedArtifact) ([]ToolSpec, error))
	})
	preparedLookup = func(ArtifactSource) (PreparedEntry, bool, error) { return entry, true, nil }
	fetchRecipeContext = func(context.Context, ArtifactSource, int64) ([]byte, error) { return contextBytes, nil }
	fetchArtifactContext = func(context.Context, ArtifactSource, int64) ([]byte, error) { return contextBytes, nil }
	restrictedBuild = func(context.Context, ArtifactRecipe, []byte, RestrictedBuildConfig) (StoredOCIArtifact, error) {
		return StoredOCIArtifact{ArchiveDigest: "sha256:" + strings.Repeat("e", 64), Size: 1,
			Evidence: OCIArtifactEvidence{ImageManifestDigest: "sha256:" + strings.Repeat("f", 64)}}, nil
	}
	StoredArtifactLoader = func(context.Context, string, string, string, int64) (string, error) { return "img-id", nil }
	LocalMCPToolList = func(context.Context, ImportedArtifact) ([]ToolSpec, error) {
		return nil, errors.New("WEATHER_TOKEN is required")
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
	if review.Recipe == nil {
		t.Fatal("credential-gated review carried no recipe")
	}
	var found bool
	for _, e := range review.Recipe.Ledger {
		if e.Claim.Field == "WEATHER_TOKEN" && e.Source == "prepared" && e.Rank == RankR0 && !e.Ignored {
			found = true
		}
	}
	if !found {
		t.Fatalf("prepared R0 claim missing from ledger: %+v", review.Recipe.Ledger)
	}
}

// S5.4: catalog toolsets ride the entry into the admitted definition so the
// binding-level projection can gate the non-default sets.
func TestPreparedEntryToolsetsCarry(t *testing.T) {
	digest := "sha256:" + strings.Repeat("b", 64)
	body := `[{
		"id": "notion", "name": "Notion", "license": "MIT", "tier": "t0",
		"image": {"ref": "ghcr.io/letya999/hermes-hub-connectors/notion@` + digest + `", "digest": "` + digest + `",
			"provenance": "sha256:` + strings.Repeat("c", 64) + `", "sbom": "sha256:` + strings.Repeat("d", 64) + `"},
		"source": {"repository": "https://github.com/makenotion/notion-mcp-server", "commit_sha": "730ae781ba28beeaf0865025a3f2ed4c25ea2387"},
		"language": "node", "entrypoint": ["node", "/app/bin/cli.mjs"],
		"connection": {"fields": [{"name": "NOTION_TOKEN", "type": "token", "required": true, "secret": true, "delivery": "env", "target": "NOTION_TOKEN"}]},
		"contract_id": "notion-token", "contract_revision": 1,
		"egress": ["api.notion.com"], "read_tools": ["API-get-self"], "probe_tool": "API-get-self",
		"toolsets": {"pages": ["API-get-page", "API-patch-page"]},
		"default_enabled": ["API-get-self"], "default_excluded": ["API-patch-page"],
		"runbook": "docs/prepared-connectors.md#notion",
		"handoff": "Install notion."
	}]`
	entries, err := ParsePreparedCatalog([]byte(body))
	if err != nil {
		t.Fatal(err)
	}
	config := entries[0].Apply(DefaultSelfInstallConfig(entries[0].Source))
	if len(config.Toolsets["pages"]) != 2 || len(config.DefaultEnabled) != 1 || len(config.DefaultExcluded) != 1 {
		t.Fatalf("config toolsets=%+v", config.Toolsets)
	}
	pin := entries[0].Image
	repository, _, _ := strings.Cut(pin.Ref, "@")
	resolution := RecipeResolution{
		Source: entries[0].Source, State: "ready",
		Launch:     LaunchRecipe{Transport: ContainerMCP, Artifact: repository, Digest: pin.Digest, Entrypoint: entries[0].Entrypoint},
		Connection: entries[0].Connection,
		Evidence: []RecipeEvidence{
			{Source: "oci", Digest: pin.Provenance, Detail: "provenance attestation"},
			{Source: "oci", Digest: pin.SBOM, Detail: "sbom attestation"},
		},
	}
	imported, err := ImportPublishedArtifact(entries[0].Source, config, resolution, entries[0].Egress)
	if err != nil {
		t.Fatal(err)
	}
	if len(imported.Definition.Toolsets["pages"]) != 2 || imported.Definition.DefaultEnabled[0] != "API-get-self" || imported.Definition.DefaultExcluded[0] != "API-patch-page" {
		t.Fatalf("definition toolsets=%+v", imported.Definition.Toolsets)
	}
}
