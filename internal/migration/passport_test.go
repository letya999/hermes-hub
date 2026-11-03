package migration

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/letya999/hermes-hub/internal/passport"
	"gopkg.in/yaml.v3"
)

// TestLegacySpaceMigrationPlanApplyRollbackAndUnknownFiles drives S11.1:
// a real-shaped legacy space plans field mappings, applies them as layered
// passport sources, keeps a verified backup + rollback fence and never
// touches unknown or dynamic files.
func TestLegacySpaceMigrationPlanApplyRollbackAndUnknownFiles(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "spaces", "owner")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	write := func(rel, body string) {
		t.Helper()
		p := filepath.Join(dir, rel)
		if err := os.MkdirAll(filepath.Dir(p), 0700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(body), 0600); err != nil {
			t.Fatal(err)
		}
	}
	write("settings.yaml", `schema: 3
user: alice
timezone: Europe/Berlin
memory: true
model: default
model_url: https://models.example.test
google_email: alice@example.test
gitlab_host: gitlab.example.test
desktop_url: https://desktop.example.test
drafts_url: https://drafts.example.test
oauth_port: 13001
browser_port: 13002
tools:
  terminal:
    via: native
  file:
    via: toolhub
  gitea:
    via: mcp-raw
mcp_servers:
  gitea:
    command: mcp-gitea
workspace:
  mounts:
    - from: shared/docs
      to: /shared/docs
      mode: ro
hooks:
  on_start: echo hi
`)
	write("models.yaml", `providers:
  acme:
    url: https://acme.example.test
models:
  default:
    ladder:
      - provider: acme
        model: acme-pro
`)
	write(filepath.Join("runtime", "toolhub", "governance.json"), `{"schema":1,"revision":4,"inventory":[{"id":"native:terminal","owner":"hermes","official":true,"effect":"exec","default":"allow"},{"id":"hub:files","owner":"hub","official":true,"effect":"mutate","default":"allow"},{"id":"user_mcp","owner":"user","effect":"exec","default":"deny"}],"rules":[],"grants":[
  {"schema":1,"grant_id":"g1","principal_id":"alice","section":"hub:files","reason":"approved","granted_by":"operator","expires_at":"2999-01-01T00:00:00Z","status":"active","revision":1},
  {"schema":1,"grant_id":"g2","principal_id":"alice","section":"native:terminal","reason":"approved","granted_by":"operator","expires_at":"2999-01-01T00:00:00Z","status":"revoked","revision":1},
  {"schema":1,"grant_id":"g3","principal_id":"alice","section":"user_mcp","reason":"approved","granted_by":"operator","expires_at":"2999-01-01T00:00:00Z","status":"active","revision":1},
  {"schema":1,"grant_id":"g4","principal_id":"alice","section":"native:terminal","reason":"approved","granted_by":"operator","expires_at":"2999-01-01T00:00:00Z","status":"active","revision":1}
]}`)
	write("notes.txt", "operator scratch — not a config authority")
	write(filepath.Join("workspace", "projects", "repo", "main.go"), "package main\n")

	// Dry-run: maps fields, reports the hooks conflict, writes nothing.
	dry, err := MigratePassport(PassportOptions{Dir: dir, Now: time.Date(2026, 5, 1, 12, 0, 0, 0, time.UTC)})
	if err != nil {
		t.Fatalf("dry-run: %v", err)
	}
	if dry.Mode != "dry-run" || dry.Revision == "" {
		t.Fatalf("dry-run report: %+v", dry)
	}
	joined := strings.Join(dry.Mapped, "\n")
	for _, want := range []string{"tools.file -> tools.core.files", "tools.terminal -> tools.core.native:terminal", "tools.gitea -> tools.core.user_mcp", "models.yaml providers", "toolhub grants"} {
		if !strings.Contains(joined, want) {
			t.Fatalf("mapped missing %q:\n%s", want, joined)
		}
	}
	if len(dry.Conflicts) == 0 || !strings.Contains(strings.Join(dry.Conflicts, " "), "hooks") {
		t.Fatalf("conflicts should name preserved hooks: %v", dry.Conflicts)
	}
	if _, err := os.Stat(filepath.Join(dir, "passport-src")); !os.IsNotExist(err) {
		t.Fatal("dry-run wrote passport-src")
	}

	// Apply: revision published, sources written, backup verified, nothing lost.
	rep, err := MigratePassport(PassportOptions{Dir: dir, Environment: "dev", Apply: true, Now: time.Date(2026, 5, 1, 12, 0, 0, 0, time.UTC)})
	if err != nil {
		t.Fatalf("apply: %v", err)
	}
	revID, err := passport.Current(dir)
	if err != nil || revID != rep.Revision {
		t.Fatalf("current revision: %v %v", revID, err)
	}
	for _, doc := range []string{"stack.yaml", "config.yaml", "models.yaml", "workspace.yaml", "tools.yaml", "grants.yaml", "prompt-pointer.txt"} {
		if _, err := os.Stat(filepath.Join(dir, "generated", "passports", rep.Revision, doc)); err != nil {
			t.Fatalf("revision doc %s: %v", doc, err)
		}
	}
	for _, rel := range []string{"user.yaml", "platform-ceiling.yaml", "platform-template.yaml"} {
		if _, err := os.Stat(filepath.Join(dir, "passport-src", rel)); err != nil {
			t.Fatalf("source layer %s: %v", rel, err)
		}
	}
	if len(rep.Backup) == 0 {
		t.Fatal("apply recorded no backup objects")
	}
	for _, o := range rep.Backup {
		if o.SHA256 == "" || o.Bytes == 0 {
			t.Fatalf("backup object missing digest: %+v", o)
		}
		if _, err := os.Stat(filepath.Join(dir, "generated", "migration-backup", rep.Revision, o.Path)); err != nil {
			t.Fatalf("backup copy %s: %v", o.Path, err)
		}
	}
	if !strings.Contains(rep.Rollback, "migration-backup") {
		t.Fatalf("rollback fence: %q", rep.Rollback)
	}
	// Grants ledger carried the active hub:files grant into the revision.
	grantsRaw, err := os.ReadFile(filepath.Join(dir, "generated", "passports", rep.Revision, "grants.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	var grantsDoc struct {
		Allow map[string][]string `yaml:"allow"`
	}
	if err := yaml.Unmarshal(grantsRaw, &grantsDoc); err != nil {
		t.Fatal(err)
	}
	if ops := grantsDoc.Allow["workspace"]; len(ops) == 0 {
		t.Fatalf("grants doc missing workspace rights from hub:files requires: %v", grantsDoc.Allow)
	}
	// Nothing is ever deleted: legacy authority, unknown files, project trees.
	for _, rel := range []string{"settings.yaml", "models.yaml", "notes.txt", filepath.Join("workspace", "projects", "repo", "main.go")} {
		if _, err := os.Stat(filepath.Join(dir, rel)); err != nil {
			t.Fatalf("lost %s: %v", rel, err)
		}
	}

	// Backup refuses symlinked authority files.
	link := filepath.Join(dir, "settings.yaml")
	target := filepath.Join(dir, "settings-real.yaml")
	raw, _ := os.ReadFile(link)
	if err := os.WriteFile(target, raw, 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(link); err != nil {
		t.Skipf("cannot prepare symlink fixture: %v", err)
	}
	if err := os.Symlink(target, link); err != nil {
		os.WriteFile(link, raw, 0600)
		t.Skipf("symlinks unavailable: %v", err)
	}
	if _, err := MigratePassport(PassportOptions{Dir: dir, Apply: true, Now: time.Date(2026, 5, 1, 13, 0, 0, 0, time.UTC)}); err == nil || !strings.Contains(err.Error(), "regular file") {
		t.Fatalf("symlinked settings must fail the backup: %v", err)
	}
	var _ = json.Marshal // silence unused import across skips
}

// TestMigrationMapsLegacyToolNamesToR5Toolsets pins the r4->r5 rename hints:
// every retired `via: toolhub` alias lands on the catalog r5 toolset that
// absorbed it, so the operator sees the new logical name in the plan.
func TestMigrationMapsLegacyToolNamesToR5Toolsets(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "spaces", "alice")
	write := func(name, body string) {
		p := filepath.Join(dir, name)
		if err := os.MkdirAll(filepath.Dir(p), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(body), 0600); err != nil {
			t.Fatal(err)
		}
	}
	write("settings.yaml", `schema: 3
user: alice
model: default
model_url: https://models.example.test
oauth_port: 13001
browser_port: 13002
tools:
  artifacts:
    via: toolhub
  code_exec:
    via: toolhub
  docs:
    via: toolhub
  hosting:
    via: toolhub
  image_gen:
    via: toolhub
  services:
    via: toolhub
  skills_manage:
    via: toolhub
`)
	write("models.yaml", `providers:
  acme:
    url: https://acme.example.test
models:
  default:
    ladder:
      - provider: acme
        model: acme-pro
`)

	rep, err := MigratePassport(PassportOptions{Dir: dir, Now: time.Date(2026, 5, 1, 12, 0, 0, 0, time.UTC)})
	if err != nil {
		t.Fatalf("dry-run: %v", err)
	}
	joined := strings.Join(rep.Mapped, "\n")
	for _, want := range []string{
		"tools.artifacts -> tools.core.files",
		"tools.code_exec -> tools.core.terminal",
		"tools.docs -> tools.core.documents",
		"tools.hosting -> tools.core.sites",
		"tools.image_gen -> tools.core.media",
		"tools.services -> tools.core.connectors",
		"tools.skills_manage -> tools.core.skills",
	} {
		if !strings.Contains(joined, want) {
			t.Fatalf("mapped missing %q:\n%s", want, joined)
		}
	}
	for _, c := range rep.Conflicts {
		if strings.Contains(c, "tools.") {
			t.Fatalf("legacy tool name produced a conflict: %s", c)
		}
	}
}

// TestMigrationRejectsMixedAuthorityCorruptStoreAndSecretLeak covers the
// remaining S11.1 failure cases: mixed legacy/new authority must be
// reported, a corrupt governance store fails before any write, and the
// report never carries secret-bearing file contents.
func TestMigrationRejectsMixedAuthorityCorruptStoreAndSecretLeak(t *testing.T) {
	base := func() string {
		dir := filepath.Join(t.TempDir(), "spaces", "owner")
		if err := os.MkdirAll(dir, 0o700); err != nil {
			t.Fatal(err)
		}
		settings := `schema: 3
user: bob
timezone: UTC
memory: false
model: default
model_url: https://models.example.test
google_email: bob@example.test
gitlab_host: gitlab.example.test
desktop_url: https://desktop.example.test
drafts_url: https://drafts.example.test
oauth_port: 13001
browser_port: 13002
tools:
  files:
    via: toolhub
`
		for _, f := range []struct{ rel, body string }{
			{"settings.yaml", settings},
			{"models.yaml", "models:\n    default:\n        ladder:\n            - provider: cliproxy\n              model: m1\n"},
		} {
			p := filepath.Join(dir, f.rel)
			if err := os.WriteFile(p, []byte(f.body), 0600); err != nil {
				t.Fatal(err)
			}
		}
		return dir
	}

	// Mixed old/new: settings.yaml beside agent.yaml surfaces a conflict.
	dir := base()
	agent := `schema: 3
user: bob
timezone: UTC
model: default
model_url: https://models.example.test
google_email: bob@example.test
gitlab_host: gitlab.example.test
desktop_url: https://desktop.example.test
drafts_url: https://drafts.example.test
oauth_port: 13001
browser_port: 13002
`
	workspace := `tools:
  terminal:
    via: native
`
	for _, f := range []struct{ rel, body string }{
		{"agent.yaml", agent},
		{"workspace.yaml", workspace},
	} {
		if err := os.WriteFile(filepath.Join(dir, f.rel), []byte(f.body), 0600); err != nil {
			t.Fatal(err)
		}
	}
	rep, err := MigratePassport(PassportOptions{Dir: dir})
	if err != nil {
		t.Fatal(err)
	}
	joined := strings.Join(rep.Conflicts, " ")
	if !strings.Contains(joined, "mixed legacy settings.yaml") {
		t.Fatalf("mixed authority not reported: %v", rep.Conflicts)
	}

	// Corrupt governance store fails the migration before any write.
	dir = base()
	th := filepath.Join(dir, "runtime", "toolhub")
	if err := os.MkdirAll(th, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(th, "governance.json"), []byte("{not json"), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := MigratePassport(PassportOptions{Dir: dir, Apply: true}); err == nil || !strings.Contains(err.Error(), "governance") {
		t.Fatalf("corrupt governance store must fail: %v", err)
	}
	if _, err := os.Stat(filepath.Join(dir, "passport-src")); !os.IsNotExist(err) {
		t.Fatal("write attempted before governance check")
	}

	// A secret-bearing env file is never copied, read or echoed.
	dir = base()
	secret := "sk-live-" + strings.Repeat("a", 24)
	if err := os.MkdirAll(filepath.Join(dir, "runtime"), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "runtime", "model.env"), []byte("ACME_KEY="+secret), 0600); err != nil {
		t.Fatal(err)
	}
	rep, err = MigratePassport(PassportOptions{Dir: dir, Apply: true})
	if err != nil {
		t.Fatal(err)
	}
	out, _ := json.Marshal(rep)
	if strings.Contains(string(out), secret) {
		t.Fatal("secret value reached the migration report")
	}
	for _, o := range rep.Backup {
		if strings.Contains(o.Path, "model.env") {
			t.Fatalf("secret-bearing file backed up: %v", o)
		}
	}
}

// TestSpaceAuditReportsInventoryAndDenyReasons covers S11.2: an operator
// audit reads only the published revision and answers services, routes,
// workspace tree, toolset projection, grants, provenance and deny reasons.
func TestSpaceAuditReportsInventoryAndDenyReasons(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "spaces", "owner")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	settings := `schema: 3
user: carol
timezone: UTC
memory: false
model: default
model_url: https://models.example.test
google_email: c@example.test
gitlab_host: gitlab.example.test
desktop_url: https://desktop.example.test
drafts_url: https://drafts.example.test
oauth_port: 13001
browser_port: 13002
tools:
  terminal:
    via: native
  file:
    via: toolhub
`
	if err := os.WriteFile(filepath.Join(dir, "settings.yaml"), []byte(settings), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "models.yaml"), []byte("models:\n    default:\n        ladder:\n            - provider: cliproxy\n              model: m1\n"), 0600); err != nil {
		t.Fatal(err)
	}
	rep, err := MigratePassport(PassportOptions{Dir: dir, Apply: true})
	if err != nil {
		t.Fatal(err)
	}
	audit, err := AuditPassport(dir)
	if err != nil {
		t.Fatal(err)
	}
	if audit.Revision != rep.Revision {
		t.Fatalf("audit revision %q != applied %q", audit.Revision, rep.Revision)
	}
	if len(audit.Services) == 0 {
		t.Fatal("audit reports no services")
	}
	if audit.Workspace.Tree == "" || len(audit.Workspace.Writable) == 0 {
		t.Fatalf("workspace inventory empty: %+v", audit.Workspace)
	}
	if len(audit.Toolsets.Core)+len(audit.Toolsets.Extended) == 0 {
		t.Fatal("no projected toolsets reported")
	}
	found := false
	for _, d := range audit.DenyReasons {
		if strings.Contains(d, "user_mcp") || strings.Contains(d, "org_mcp") {
			found = true
		}
	}
	if !found {
		t.Fatalf("raw MCP default-deny not reported: %v", audit.DenyReasons)
	}
	if len(audit.Provenance) == 0 {
		t.Fatal("no layer provenance recorded")
	}
}

// TestMigrationNormalizesEnvSuffixedServiceNames covers per-space-env
// instances: compose names them <kind>-<user>-<env> (hosting et al), so the
// stack passport must carry the catalog kind, never the suffixed key.
func TestMigrationNormalizesEnvSuffixedServiceNames(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "spaces", "owner")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	write := func(rel, body string) {
		t.Helper()
		p := filepath.Join(dir, rel)
		if err := os.MkdirAll(filepath.Dir(p), 0700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(body), 0600); err != nil {
			t.Fatal(err)
		}
	}
	write("settings.yaml", `schema: 3
user: dana
timezone: UTC
model: default
model_url: https://models.example.test
google_email: dana@example.test
gitlab_host: gitlab.example.test
desktop_url: https://desktop.example.test
drafts_url: https://drafts.example.test
oauth_port: 13001
browser_port: 13002
tools:
  hosting:
    via: toolhub
hosting:
  slots: [site1]
`)
	write("models.yaml", "models:\n    default:\n        ladder:\n            - provider: cliproxy\n              model: m1\n")

	rep, err := MigratePassport(PassportOptions{Dir: dir, Environment: "dev", Apply: true, Now: time.Date(2026, 5, 1, 12, 0, 0, 0, time.UTC)})
	if err != nil {
		t.Fatalf("apply: %v", err)
	}
	raw, err := os.ReadFile(filepath.Join(dir, "generated", "passports", rep.Revision, "stack.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	var doc struct {
		Services map[string]struct {
			Mode string `yaml:"mode"`
		} `yaml:"services"`
	}
	if err := yaml.Unmarshal(raw, &doc); err != nil {
		t.Fatal(err)
	}
	names := make([]string, 0, len(doc.Services))
	for name := range doc.Services {
		names = append(names, name)
	}
	if _, ok := doc.Services["hosting"]; !ok {
		t.Fatalf("stack services must carry catalog kind hosting: %v", names)
	}
	for name := range doc.Services {
		if strings.Contains(name, "dana") || strings.Contains(name, "-dev") {
			t.Fatalf("service name leaked compose suffix: %q", name)
		}
	}
}
