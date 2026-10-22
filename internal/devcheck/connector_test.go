package devcheck

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/letya999/hermes-hub/internal/toolhub/api"
)

const connectorCatalogFixture = `[
  {
    "id": "playwright-mcp", "aliases": ["browser"], "name": "Managed browser", "license": "Apache-2.0",
    "tier": "t1",
    "source": {"repository": "https://github.com/microsoft/playwright-mcp", "commit_sha": "ab6d67cbf8b619338f096980806fdbe29432cc4d"},
    "read_tools": ["browser_navigate"],
    "runbook": "docs/prepared-connectors.md#playwright-mcp",
    "handoff": "x"
  },
  {
    "id": "notion", "name": "Notion", "license": "MIT",
    "tier": "t0",
    "image": {"ref": "localhost:5543/notion@sha256:old", "digest": "sha256:old", "provenance": "sha256:p0", "sbom": "sha256:s0"},
    "source": {"repository": "https://github.com/example/notion", "commit_sha": "730ae781"},
    "read_tools": ["search"],
    "runbook": "docs/prepared-connectors.md#notion",
    "handoff": "x"
  }
]
`

func pinFixture() connectorPin {
	return connectorPin{
		ID:         "playwright-mcp",
		Ref:        "ghcr.io/letya999/hermes-hub-connectors/playwright-mcp",
		Tag:        "src-ab6d67cbf8b6",
		Digest:     "sha256:9b46b8e593e6b72c7f3f8c4aed20e624331358f1e099d2f8705bd59283504925",
		Provenance: "sha256:50fc6b71dd8fc1ea2eda515f31ccce9c33bba63bcb812f31c772e76aee916bf2",
		SBOM:       "sha256:50fc6b71dd8fc1ea2eda515f31ccce9c33bba63bcb812f31c772e76aee916bf2",
	}
}

func TestConnectorCatalogPatchInsertsImageAndTier(t *testing.T) {
	pin := pinFixture()
	patched, err := connectorCatalogPatch([]byte(connectorCatalogFixture), pin)
	if err != nil {
		t.Fatal(err)
	}
	out := string(patched)
	wantImage := `"image": {"ref": "ghcr.io/letya999/hermes-hub-connectors/playwright-mcp@` + pin.Digest + `", "digest": "` + pin.Digest + `", "provenance": "` + pin.Provenance + `", "sbom": "` + pin.SBOM + `"},`
	if !strings.Contains(out, wantImage) {
		t.Fatalf("image line missing or malformed:\n%s", out)
	}
	if !strings.Contains(out, `"tier": "t0"`) {
		t.Fatal("entry tier must be promoted to t0")
	}
	// The second entry keeps its own pin untouched.
	if !strings.Contains(out, `localhost:5543/notion@sha256:old`) {
		t.Fatal("unrelated entry pin mutated")
	}
}

func TestConnectorCatalogPatchReplacesExistingPin(t *testing.T) {
	pin := pinFixture()
	pin.ID = "notion"
	pin.Ref = "ghcr.io/letya999/hermes-hub-connectors/notion"
	patched, err := connectorCatalogPatch([]byte(connectorCatalogFixture), pin)
	if err != nil {
		t.Fatal(err)
	}
	out := string(patched)
	if strings.Contains(out, "localhost:5543") || strings.Contains(out, "sha256:old") {
		t.Fatalf("stale pin survived:\n%s", out)
	}
	if !strings.Contains(out, `"image": {"ref": "ghcr.io/letya999/hermes-hub-connectors/notion@`+pin.Digest) {
		t.Fatalf("pin not updated:\n%s", out)
	}
}

func TestConnectorCatalogPatchRejectsUnknownID(t *testing.T) {
	pin := pinFixture()
	pin.ID = "ghost"
	if _, err := connectorCatalogPatch([]byte(connectorCatalogFixture), pin); err == nil {
		t.Fatal("unknown entry id accepted")
	}
}

func TestConnectorPinVerifiesRegistryDigest(t *testing.T) {
	pin := pinFixture()
	// sha256("manifest-bytes") == pin.Digest only for the exact fixture body.
	manifest := []byte("manifest-bytes")
	sum := sha256.Sum256(manifest)
	pin.Digest = "sha256:" + hex.EncodeToString(sum[:])
	var written []byte
	run := func(ctx context.Context, args ...string) ([]byte, error) {
		if args[0] != "skopeo" || args[1] != "inspect" || args[2] != "--raw" || args[3] != "docker://"+pin.Ref+":"+pin.Tag {
			t.Fatalf("unexpected inspect args: %v", args)
		}
		return manifest, nil
	}
	// The patched bytes must round-trip the strict catalog parser, so the
	// fixture is a copy of the real embedded catalog.
	real, err := os.ReadFile(filepath.Join("..", "toolhub", "api", "prepared", "catalog.json"))
	if err != nil {
		t.Fatal(err)
	}
	catalog := filepath.Join(t.TempDir(), "catalog.json")
	if err := os.WriteFile(catalog, real, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := connectorPinApply(context.Background(), pin, pin.Ref+":"+pin.Tag, catalog, run); err != nil {
		t.Fatalf("matching digest rejected: %v", err)
	}
	out, err := os.ReadFile(catalog)
	if err != nil {
		t.Fatal(err)
	}
	written = out
	if !strings.Contains(string(written), `"ref": "`+pin.Ref+"@"+pin.Digest+`"`) {
		t.Fatalf("catalog not pinned to verified digest:\n%s", written)
	}
	// A mismatched registry digest must refuse before touching the file.
	bad := func(ctx context.Context, args ...string) ([]byte, error) { return []byte("other"), nil }
	if err := connectorPinApply(context.Background(), pin, pin.Ref+":"+pin.Tag, catalog, bad); err == nil {
		t.Fatal("mismatched registry digest accepted")
	}
}

func TestConnectorPinFileRoundTripAndRejectsMalformed(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "pin.json")
	pin := pinFixture()
	if err := writeConnectorPin(path, pin); err != nil {
		t.Fatal(err)
	}
	got, err := readConnectorPin(path)
	if err != nil || got != pin {
		t.Fatalf("round trip: got %+v err %v", got, err)
	}
	if err := os.WriteFile(path, []byte(`{"id":"x","digest":"not-a-digest"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := readConnectorPin(path); err == nil {
		t.Fatal("malformed evidence accepted")
	}
}

func TestConnectorPublishRefResolvesNamespace(t *testing.T) {
	t.Setenv("HUB_REGISTRY_REPO", "o/r")
	ref, err := ConnectorPublishRef(context.Background(), "playwright-mcp")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(ref, "ghcr.io/o/r/hermes-hub-connectors/playwright-mcp:src-") {
		t.Fatalf("canonical ref %q", ref)
	}
	if _, err := ConnectorPublishRef(context.Background(), "ghost"); err == nil {
		t.Fatal("unknown entry accepted")
	}
}

func TestConnectorPinRefMustMatchPinFile(t *testing.T) {
	dir := t.TempDir()
	pinPath := filepath.Join(dir, "pin.json")
	if err := writeConnectorPin(pinPath, pinFixture()); err != nil {
		t.Fatal(err)
	}
	catalog := filepath.Join(dir, "catalog.json")
	// A foreign ref is refused before any registry or catalog access.
	err := ConnectorPin(context.Background(), "playwright-mcp", pinPath, "ghcr.io/evil/x:latest", catalog)
	if err == nil || !strings.Contains(err.Error(), "pin's own") {
		t.Fatalf("foreign ref accepted: %v", err)
	}
}

func TestConnectorBuildRejectsUnknownEntry(t *testing.T) {
	t.Setenv("HUB_REGISTRY_REPO", "o/r")
	if err := ConnectorBuild(context.Background(), "ghost", t.TempDir()); err == nil {
		t.Fatal("unknown entry accepted")
	}
}

func TestConnectorBuildExportsArchiveAndPin(t *testing.T) {
	dir := t.TempDir()
	archive := filepath.Join(dir, "blob.oci.tar")
	if err := os.WriteFile(archive, []byte("oci-bytes"), 0o600); err != nil {
		t.Fatal(err)
	}
	entries, err := api.PreparedCatalog()
	if err != nil {
		t.Fatal(err)
	}
	var entrypoint []string
	for _, entry := range entries {
		if entry.ID == "playwright-mcp" {
			entrypoint = entry.Entrypoint
		}
	}
	fakeImport := func(ctx context.Context, source api.ArtifactSource, config api.ArtifactImportConfig, build api.RestrictedBuildConfig) (api.ImportedArtifact, error) {
		if source.CommitSHA == "" || config.Image != "hermes-connector/playwright-mcp" {
			t.Fatalf("import config: %+v %+v", source, config)
		}
		return api.ImportedArtifact{
			Recipe: api.ArtifactRecipe{Entrypoint: entrypoint},
			Artifact: api.StoredOCIArtifact{
				ArchiveDigest: "sha256:" + strings.Repeat("f", 64),
				Evidence: api.OCIArtifactEvidence{
					ImageManifestDigest: "sha256:" + strings.Repeat("1", 64),
					ProvenanceDigest:    "sha256:" + strings.Repeat("2", 64),
					SBOMDigest:          "sha256:" + strings.Repeat("3", 64),
				},
			},
		}, nil
	}
	fakeOpen := func(ctx context.Context, directory, digest string, maxBytes int64) (*os.File, api.StoredOCIArtifact, error) {
		file, err := os.Open(archive)
		return file, api.StoredOCIArtifact{}, err
	}
	err = connectorBuild(context.Background(), "playwright-mcp", dir, "o/r", fakeImport, fakeOpen)
	if err != nil {
		t.Fatal(err)
	}
	if body, err := os.ReadFile(filepath.Join(dir, "artifact.oci.tar")); err != nil || string(body) != "oci-bytes" {
		t.Fatalf("archive export: %v", err)
	}
	pin, err := readConnectorPin(filepath.Join(dir, "pin.json"))
	if err != nil {
		t.Fatal(err)
	}
	if pin.Ref != "ghcr.io/o/r/hermes-hub-connectors/playwright-mcp" || pin.Tag != "src-ab6d67cbf8b6" ||
		pin.Digest != "sha256:"+strings.Repeat("1", 64) {
		t.Fatalf("pin: %+v", pin)
	}
}
