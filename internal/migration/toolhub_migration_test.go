package migration

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/letya999/hermes-hub/internal/identity"
	"github.com/letya999/hermes-hub/internal/toolhub"
)

func TestToolHubMigrationDryRunApplyPreservesStateAndSecrets(t *testing.T) {
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, "generated"), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(root, "runtime"), 0700); err != nil {
		t.Fatal(err)
	}
	settings := `schema: 1
user: alice
timezone: UTC
oauth_port: 8000
browser_port: 6080
disabled_mcp: [cli]
mcp_servers:
  remote:
    url: https://example.invalid/mcp
    headers:
      Authorization: "Bearer ${REMOTE_TOKEN}"
    tools:
      include: [search]
  cli:
    command: glab
    args: [mr, list]
    tools:
      include: [list]
`
	if err := os.WriteFile(filepath.Join(root, "settings.yaml"), []byte(settings), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "runtime", "self-services.json"), []byte(`{"features":["legacy"]}`), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "generated", "mcp.json"), []byte("generated"), 0600); err != nil {
		t.Fatal(err)
	}
	storePath := filepath.Join(root, "toolhub", "store.json")
	dryRun, err := MigrateToolHub(ToolHubMigrationOptions{Directory: root, StateDir: filepath.Join(root, "runtime"), StorePath: storePath, User: "alice"})
	if err != nil {
		t.Fatal(err)
	}
	if dryRun.Mode != "dry-run" || dryRun.Applied || len(dryRun.ImportedManifests) != 2 || len(dryRun.Unmatched) != 1 || dryRun.SecretsCopied || !dryRun.GeneratedMCPPresent {
		t.Fatalf("dry-run report=%+v", dryRun)
	}
	if _, err := os.Stat(storePath); !os.IsNotExist(err) {
		t.Fatalf("dry-run created store: %v", err)
	}

	if err := os.MkdirAll(filepath.Dir(storePath), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(storePath, []byte("previous-store"), 0600); err != nil {
		t.Fatal(err)
	}
	applyReport, err := MigrateToolHub(ToolHubMigrationOptions{Directory: root, StateDir: filepath.Join(root, "runtime"), StorePath: storePath, User: "alice", Apply: true})
	if err != nil {
		t.Fatal(err)
	}
	if !applyReport.Applied || applyReport.RollbackPath == "" || !fileExists(applyReport.RollbackPath) || applyReport.SecretsCopied {
		t.Fatalf("apply report=%+v", applyReport)
	}
	store, err := toolhub.Load(storePath)
	if err != nil {
		t.Fatal(err)
	}
	auth := identity.Envelope{Schema: identity.Schema, PrincipalID: "alice", ExternalIdentityID: "alice", ContextID: "alice", RuntimeID: "alice", ConversationID: "migration", DeliveryTargetID: "migration", PolicyVersion: "policy-1"}
	entries, err := store.Catalog(auth)
	if err != nil || len(entries) != 2 {
		t.Fatalf("catalog=%+v err=%v", entries, err)
	}
	if entries[0].Status != "disabled" && entries[1].Status != "disabled" {
		t.Fatalf("disabled MCP state not preserved: %+v", entries)
	}
	if strings.Contains(string(mustJSON(t, applyReport)), "super-secret") || strings.Contains(string(mustJSON(t, applyReport)), "REMOTE_TOKEN=") {
		t.Fatal("migration report contains secret material")
	}
	if _, err := os.Stat(filepath.Join(root, "settings.yaml")); err != nil {
		t.Fatal("source settings removed:", err)
	}
}

func TestToolHubMigrationHelperValidation(t *testing.T) {
	for _, raw := range []string{"http://example.invalid/mcp", "https://user@example.invalid/mcp", "https://example.invalid/mcp\n", "https:///mcp"} {
		if _, err := hostFromURL(raw); err == nil {
			t.Fatalf("unsafe URL accepted: %q", raw)
		}
	}
	if host, err := hostFromURL("https://Example.invalid/mcp"); err != nil || host != "example.invalid" {
		t.Fatalf("valid URL host=%q err=%v", host, err)
	}
	if stableConnectionID("short") != "short-connection" || stableConnectionID(strings.Repeat("long", 20)) == "" {
		t.Fatal("connection IDs are not deterministic")
	}
}

func TestToolHubMigrationRejectsMissingAndMalformedInputs(t *testing.T) {
	if _, err := MigrateToolHub(ToolHubMigrationOptions{}); err == nil {
		t.Fatal("empty migration options accepted")
	}
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "settings.yaml"), []byte("schema: 1\nuser: alice\ntimezone: UTC\noauth_port: 8000\nbrowser_port: 6080\n"), 0600); err != nil {
		t.Fatal(err)
	}
	state := filepath.Join(root, "runtime")
	if err := os.MkdirAll(state, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(state, "self-services.json"), []byte("bad"), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := MigrateToolHub(ToolHubMigrationOptions{Directory: root, StateDir: state, User: "alice"}); err == nil {
		t.Fatal("malformed legacy service state accepted")
	}
	if err := os.Remove(filepath.Join(state, "self-services.json")); err != nil {
		t.Fatal(err)
	}
	if _, err := MigrateToolHub(ToolHubMigrationOptions{Directory: root, StateDir: state, StorePath: "relative/store.json", User: "alice"}); err == nil {
		t.Fatal("relative ToolHub store accepted")
	}
}

func TestReadLegacyServicesSortsAndCompacts(t *testing.T) {
	path := filepath.Join(t.TempDir(), "self-services.json")
	if err := os.WriteFile(path, []byte(`{"features":["zeta","alpha","zeta"]}`), 0600); err != nil {
		t.Fatal(err)
	}
	features, err := readLegacyServices(path)
	if err != nil || strings.Join(features, ",") != "alpha,zeta" {
		t.Fatalf("features=%v err=%v", features, err)
	}
}

func TestToolHubMigrationApplyRejectsUnwritableParent(t *testing.T) {
	root := t.TempDir()
	parent := filepath.Join(root, "parent")
	if err := os.WriteFile(parent, []byte("file"), 0600); err != nil {
		t.Fatal(err)
	}
	report := ToolHubMigrationReport{}
	if err := applyToolHubStore(toolhub.NewStore(), filepath.Join(parent, "store.json"), &report); err == nil {
		t.Fatal("unwritable ToolHub parent accepted")
	}
	if _, err := MigrateToolHub(ToolHubMigrationOptions{Directory: filepath.Join(root, "missing"), User: "alice"}); err == nil {
		t.Fatal("missing settings accepted")
	}
}
