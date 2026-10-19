package api

import (
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
