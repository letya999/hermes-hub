package main

import (
	"archive/zip"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/letya999/hermes-hub/internal/skillstore"
	"github.com/letya999/hermes-hub/internal/stack"
	"gopkg.in/yaml.v3"
)

func TestContextMemorySkillRoutineCLI(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	if err := run(ctx, []string{"init", "--dir", dir, "--user", "owner"}); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "hermes", "memories", "USER.md"), []byte("remember tea"), 0600); err != nil {
		t.Fatal(err)
	}
	out := filepath.Join(t.TempDir(), "owner.zip")
	spool := filepath.Join(t.TempDir(), "spool")
	if err := os.MkdirAll(filepath.Join(spool, "schedules"), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(spool, "schedules", "morning.json"), []byte(`{"schedule_id":"morning","principal_id":"owner","context_id":"owner"}`), 0600); err != nil {
		t.Fatal(err)
	}
	if err := run(ctx, []string{"context", "backup", "--dir", dir, "--user", "owner", "--out", out, "--spool", spool}); err != nil {
		t.Fatal(err)
	}
	raw, _ := os.ReadFile(out)
	if strings.Contains(string(raw), "OPENAI_API_KEY=") && strings.Contains(string(raw), "super") {
		t.Fatal("backup leaked secrets")
	}
	other := t.TempDir()
	if err := run(ctx, []string{"context", "restore", "--dir", other, "--user", "intruder", "--in", out}); err == nil {
		t.Fatal("restore to another user")
	}
	dest := t.TempDir()
	exported := filepath.Join(t.TempDir(), "owner-export.zip")
	if err := run(ctx, []string{"context", "export", "--dir", dir, "--user", "owner", "--out", exported}); err != nil {
		t.Fatal(err)
	}
	spoolDest := filepath.Join(t.TempDir(), "spool-restore")
	if err := run(ctx, []string{"context", "restore", "--dir", dest, "--user", "owner", "--in", out, "--spool", spoolDest}); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(spoolDest, "schedules", "morning.json")); err != nil {
		t.Fatal("hubctl restore did not write schedules into --spool")
	}
	if err := run(ctx, []string{"memory", "list", "--dir", dir, "--user", "owner"}); err != nil {
		t.Fatal(err)
	}
	if err := run(ctx, []string{"memory", "delete", "--dir", dir, "--user", "owner", "--name", "USER.md"}); err != nil {
		t.Fatal(err)
	}
	skill := filepath.Join(t.TempDir(), "SKILL.md")
	if err := os.WriteFile(skill, []byte("---\nname: demo\ndescription: demo skill\n---\n\n# Demo\nList files.\n"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := run(ctx, []string{"skill", "install", "--dir", dir, "--user", "owner", "--name", "demo", "--from-file", skill, "--consent"}); err != nil {
		t.Fatal(err)
	}
	listed := captureOutput(t, func() error {
		return run(ctx, []string{"skill", "list", "--dir", dir, "--user", "owner"})
	})
	if !strings.Contains(listed, `"demo"`) {
		t.Fatalf("skill list lost the published skill: %s", listed)
	}
	if err := run(ctx, []string{"skill", "show", "--dir", dir, "--user", "owner", "--name", "demo"}); err != nil {
		t.Fatal(err)
	}
	if err := run(ctx, []string{"skill", "archive", "--dir", dir, "--user", "owner", "--name", "demo"}); err != nil {
		t.Fatal(err)
	}
	if err := run(ctx, []string{"skill", "restore", "--dir", dir, "--user", "owner", "--name", "demo"}); err != nil {
		t.Fatal(err)
	}
	if err := run(ctx, []string{"context", "purge", "--dir", dest, "--user", "owner", "--confirm", "owner"}); err != nil {
		t.Fatal(err)
	}
	routineSpool := filepath.Join(t.TempDir(), "routine-spool")
	due := "once:2099-01-01T00:00:00Z"
	if err := run(ctx, []string{"routine", "create", "--spool", routineSpool, "--user", "owner", "--chat-id", "1", "--organization", "personal", "--runtime", "owner", "--policy", "policy-1", "--id", "morning", "--tz", "UTC", "--expr", due, "--input", "brief"}); err != nil {
		t.Fatal(err)
	}
	if err := run(ctx, []string{"routine", "list", "--spool", routineSpool, "--user", "owner"}); err != nil {
		t.Fatal(err)
	}
	if err := run(ctx, []string{"routine", "pause", "--spool", routineSpool, "--user", "owner", "--chat-id", "1", "--organization", "personal", "--runtime", "owner", "--policy", "policy-1", "--id", "morning"}); err != nil {
		t.Fatal(err)
	}
	if err := run(ctx, []string{"routine", "delete", "--spool", routineSpool, "--user", "owner", "--chat-id", "1", "--organization", "personal", "--runtime", "owner", "--policy", "policy-1", "--id", "morning"}); err != nil {
		t.Fatal(err)
	}
	if err := run(ctx, []string{"context"}); err == nil {
		t.Fatal("context without action")
	}
	if err := run(ctx, []string{"context", "backup", "--dir", dir, "--user", "owner"}); err == nil {
		t.Fatal("backup without out")
	}
	if err := run(ctx, []string{"memory", "delete", "--dir", dir, "--user", "owner"}); err == nil {
		t.Fatal("memory delete without name")
	}
	if err := run(ctx, []string{"skill", "install", "--dir", dir, "--user", "owner", "--name", "demo"}); err == nil {
		t.Fatal("skill install without file")
	}
	// Without --consent the skill lands as an inactive draft, not an error.
	if err := run(ctx, []string{"skill", "install", "--dir", dir, "--user", "owner", "--name", "demo", "--from-file", skill}); err != nil {
		t.Fatal(err)
	}
	drafts := captureOutput(t, func() error {
		return run(ctx, []string{"skill", "drafts", "--dir", dir, "--user", "owner"})
	})
	if !strings.Contains(drafts, `"demo"`) {
		t.Fatalf("no-consent install did not land as a draft: %s", drafts)
	}
	var dlist []struct {
		ID   string `json:"id"`
		Name string `json:"name"`
	}
	if err := json.Unmarshal([]byte(drafts), &dlist); err != nil || len(dlist) == 0 {
		t.Fatalf("drafts output not decodable: %v %s", err, drafts)
	}
	if err := run(ctx, []string{"skill", "approve", "--dir", dir, "--user", "owner", "--id", dlist[0].ID}); err != nil {
		t.Fatal(err)
	}
	shown := captureOutput(t, func() error {
		return run(ctx, []string{"skill", "show", "--dir", dir, "--user", "owner", "--name", dlist[0].Name})
	})
	if !strings.Contains(shown, `"demo"`) {
		t.Fatalf("approved draft not active: %s", shown)
	}
	// A second unconsented install drafts again; reject drops it.
	if err := run(ctx, []string{"skill", "install", "--dir", dir, "--user", "owner", "--name", "demo", "--from-file", skill}); err != nil {
		t.Fatal(err)
	}
	drafts2 := captureOutput(t, func() error {
		return run(ctx, []string{"skill", "drafts", "--dir", dir, "--user", "owner"})
	})
	var dlist2 []struct {
		ID string `json:"id"`
	}
	if err := json.Unmarshal([]byte(drafts2), &dlist2); err != nil || len(dlist2) == 0 {
		t.Fatalf("second draft missing: %v %s", err, drafts2)
	}
	if err := run(ctx, []string{"skill", "reject", "--dir", dir, "--user", "owner", "--id", dlist2[0].ID}); err != nil {
		t.Fatal(err)
	}
	drafts3 := captureOutput(t, func() error {
		return run(ctx, []string{"skill", "drafts", "--dir", dir, "--user", "owner"})
	})
	if strings.Contains(drafts3, dlist2[0].ID) {
		t.Fatalf("rejected draft still listed: %s", drafts3)
	}
	if err := run(ctx, []string{"skill", "rollback", "--dir", dir, "--user", "owner", "--name", "demo"}); err == nil {
		t.Fatal("rollback without --to")
	}
	if err := run(ctx, []string{"skill", "rollback", "--dir", dir, "--user", "owner", "--name", "demo", "--to", "deadbeef"}); err == nil || strings.Contains(err.Error(), "unknown skill command") {
		t.Fatalf("rollback to a missing revision must reach the store: %v", err)
	}
	if err := run(ctx, []string{"routine", "list"}); err == nil {
		t.Fatal("routine without spool")
	}
	if err := run(ctx, []string{"context", "stop-runtime", "--user", "owner"}); err != nil {
		t.Fatal(err)
	}
	if err := run(ctx, []string{"context", "delete-runtime", "--user", "owner"}); err != nil {
		t.Fatal(err)
	}
	if err := run(ctx, []string{"memory", "unknown", "--dir", dir, "--user", "owner"}); err == nil {
		t.Fatal("unknown memory command")
	}
	if err := run(ctx, []string{"skill", "unknown", "--dir", dir, "--user", "owner"}); err == nil {
		t.Fatal("unknown skill command")
	}
	if err := run(ctx, []string{"routine", "unknown", "--spool", routineSpool, "--user", "owner"}); err == nil {
		t.Fatal("unknown routine command")
	}
	if err := run(ctx, []string{"context", "restore", "--dir", dest, "--user", "owner"}); err == nil {
		t.Fatal("restore without archive")
	}
	if err := run(ctx, []string{"memory", "list", "--dir", dir, "--user", "owner"}); err != nil {
		t.Fatal(err)
	}
}

// --from imports a staged foreign package — a directory, a zip or a
// standalone markdown file — through the same imported-draft pipeline the
// agent's install_local uses: consent publishes, no consent lands a draft.
func TestSkillInstallFromLocal(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	if err := run(ctx, []string{"init", "--dir", dir, "--user", "owner"}); err != nil {
		t.Fatal(err)
	}
	src := t.TempDir()
	head := "---\nname: local-one\ndescription: d\n---\n\nbody"
	mk := func(rel, body string) {
		full := filepath.Join(src, filepath.FromSlash(rel))
		if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(full, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	mk("pkg/SKILL.md", head+"[guide](references/a.md)")
	mk("pkg/references/a.md", "see [b](b.md)")
	mk("pkg/references/b.md", "leaf")
	mk("pkg/.github/ci.yml", "on: push")
	// No consent: an imported draft with the dot plumbing filtered out.
	if err := run(ctx, []string{"skill", "install", "--dir", dir, "--user", "owner", "--from", filepath.Join(src, "pkg")}); err != nil {
		t.Fatal(err)
	}
	drafts := captureOutput(t, func() error {
		return run(ctx, []string{"skill", "drafts", "--dir", dir, "--user", "owner"})
	})
	if !strings.Contains(drafts, `"local-one"`) {
		t.Fatalf("--from dir did not draft: %s", drafts)
	}
	st, err := skillstore.Open(filepath.Join(dir, "skills"), skillstore.ScopeUser)
	if err != nil {
		t.Fatal(err)
	}
	var dlist []struct {
		ID string `json:"id"`
	}
	if err := json.Unmarshal([]byte(drafts), &dlist); err != nil || len(dlist) == 0 {
		t.Fatalf("drafts output not decodable: %v %s", err, drafts)
	}
	d, tree, err := st.LoadDraft(dlist[0].ID)
	if err != nil {
		t.Fatal(err)
	}
	if !d.Meta.Imported || tree[".github/ci.yml"].Data != nil || tree["references/b.md"].Data == nil {
		t.Fatalf("imported draft must be marked and dot-filtered: %+v", d.Meta)
	}
	// Consent: the standalone file installs and publishes in one step.
	mk("solo/SKILL.md", "---\nname: solo-file\ndescription: d\n---\n\nbody")
	if err := run(ctx, []string{"skill", "install", "--dir", dir, "--user", "owner", "--from", filepath.Join(src, "solo", "SKILL.md"), "--consent"}); err != nil {
		t.Fatal(err)
	}
	listed := captureOutput(t, func() error {
		return run(ctx, []string{"skill", "list", "--dir", dir, "--user", "owner"})
	})
	if !strings.Contains(listed, `"solo-file"`) {
		t.Fatalf("--from file --consent did not publish: %s", listed)
	}
	// Zip: the archive's single skill root is selected; its binary assets
	// survive intact (owner approved binary payloads).
	zpath := filepath.Join(src, "pkg.zip")
	zf, err := os.Create(zpath)
	if err != nil {
		t.Fatal(err)
	}
	zw := zip.NewWriter(zf)
	bin := []byte{0x89, 0x50, 0x4E, 0x47, 0x00, 0xFF}
	for n, b := range map[string][]byte{"wrap/SKILL.md": []byte(head), "wrap/assets/logo.png": bin} {
		w, err := zw.Create(n)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := w.Write(b); err != nil {
			t.Fatal(err)
		}
	}
	if err := zw.Close(); err != nil {
		t.Fatal(err)
	}
	if err := zf.Close(); err != nil {
		t.Fatal(err)
	}
	if err := run(ctx, []string{"skill", "install", "--dir", dir, "--user", "owner", "--from", zpath, "--consent"}); err != nil {
		t.Fatal(err)
	}
	ztree, err := st.Tree("local-one")
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(ztree["assets/logo.png"].Data, bin) {
		t.Fatalf("binary asset must survive the import: %v", ztree["assets/logo.png"].Data)
	}
}

// A host-side approve mutates the store the same way /v1/skill-apply does;
// the effective view must be rebuilt so the mounted /skills picks up the
// publish without waiting for the next render (R6.5).
func TestSkillApproveRebuildsEffectiveView(t *testing.T) {
	ctx := context.Background()
	dir := filepath.Join(t.TempDir(), "spaces", "me")
	if err := run(ctx, []string{"init", "--dir", dir, "--user", "me"}); err != nil {
		t.Fatal(err)
	}
	st, err := skillstore.Open(filepath.Join(dir, "skills"), skillstore.ScopeUser)
	if err != nil {
		t.Fatal(err)
	}
	draft, err := st.CreateDraft("fresh", skillstore.Tree{
		skillstore.SkillFile: {Data: []byte("---\nname: fresh\ndescription: d\n---\n\nBody.\n")},
	}, skillstore.DraftMeta{Author: "agent", Reason: "test"})
	if err != nil {
		t.Fatal(err)
	}
	if err := run(ctx, []string{"skill", "approve", "--dir", dir, "--user", "me", "--id", draft.ID}); err != nil {
		t.Fatalf("approve failed: %v", err)
	}
	if _, err := os.Stat(filepath.Join(stack.GeneratedEffectiveSkillsDir(dir), "fresh", "SKILL.md")); err != nil {
		t.Fatalf("approved skill did not reach the effective view: %v", err)
	}
}

// Every host-side live-store mutation refreshes the view: consent-install
// lands it, archive drops it, restore brings it back, rollback reverts it.
func TestSkillOpsRefreshEffectiveView(t *testing.T) {
	ctx := context.Background()
	dir := filepath.Join(t.TempDir(), "spaces", "me")
	if err := run(ctx, []string{"init", "--dir", dir, "--user", "me"}); err != nil {
		t.Fatal(err)
	}
	md := filepath.Join(t.TempDir(), "SKILL.md")
	write := func(body string) {
		t.Helper()
		if err := os.WriteFile(md, []byte(body), 0600); err != nil {
			t.Fatal(err)
		}
	}
	view := stack.GeneratedEffectiveSkillsDir(dir)
	skillFile := filepath.Join(view, "demo", "SKILL.md")
	live := func() bool {
		_, err := os.Stat(skillFile)
		return err == nil
	}
	write("---\nname: demo\ndescription: v1\n---\n\nV1.\n")
	if err := run(ctx, []string{"skill", "install", "--dir", dir, "--user", "me", "--name", "demo", "--from-file", md, "--consent"}); err != nil {
		t.Fatalf("consent install: %v", err)
	}
	if !live() {
		t.Fatal("consent-installed skill missing from the view")
	}
	if err := run(ctx, []string{"skill", "archive", "--dir", dir, "--user", "me", "--name", "demo"}); err != nil {
		t.Fatalf("archive: %v", err)
	}
	if live() {
		t.Fatal("archived skill still in the view")
	}
	if err := run(ctx, []string{"skill", "restore", "--dir", dir, "--user", "me", "--name", "demo"}); err != nil {
		t.Fatalf("restore: %v", err)
	}
	if !live() {
		t.Fatal("restored skill missing from the view")
	}
	st, err := skillstore.Open(filepath.Join(dir, "skills"), skillstore.ScopeUser)
	if err != nil {
		t.Fatal(err)
	}
	idx, err := st.Index()
	if err != nil {
		t.Fatal(err)
	}
	base := idx.Skills["demo"].Revision
	write("---\nname: demo\ndescription: v2\n---\n\nV2.\n")
	if err := run(ctx, []string{"skill", "install", "--dir", dir, "--user", "me", "--name", "demo", "--from-file", md, "--consent"}); err != nil {
		t.Fatal(err)
	}
	if err := run(ctx, []string{"skill", "rollback", "--dir", dir, "--user", "me", "--name", "demo", "--to", base}); err != nil {
		t.Fatalf("rollback: %v", err)
	}
	body, err := os.ReadFile(skillFile)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(body), "v1") {
		t.Fatal("view did not reflect the rollback revision")
	}
}

// skill promote publishes an org-store draft on behalf of a bound member;
// the member's org role gates it exactly like runtime skill-apply (R5.4).
func TestSkillPromotePublishesOrgDraft(t *testing.T) {
	ctx := context.Background()
	root := t.TempDir()
	dir := filepath.Join(root, "spaces", "me")
	orgDir := filepath.Join(root, "spaces", "acme")
	if err := run(ctx, []string{"org-init", "--org", "acme", "--org-dir", orgDir, "--user", "owner"}); err != nil {
		t.Fatal(err)
	}
	if err := run(ctx, []string{"init", "--dir", dir, "--user", "owner", "--org", "acme"}); err != nil {
		t.Fatal(err)
	}
	orgStore, err := skillstore.Open(filepath.Join(orgDir, "skills"), skillstore.ScopeOrg)
	if err != nil {
		t.Fatal(err)
	}
	draft, err := orgStore.CreateDraft("team-skill", skillstore.Tree{
		skillstore.SkillFile: {Data: []byte("---\nname: team-skill\ndescription: t\n---\n\nTeam body.\n")},
	}, skillstore.DraftMeta{Author: "agent", Reason: "proposal"})
	if err != nil {
		t.Fatal(err)
	}
	if err := run(ctx, []string{"skill", "promote", "--dir", dir, "--user", "owner", "--id", draft.ID}); err != nil {
		t.Fatalf("owner promote failed: %v", err)
	}
	if _, err := orgStore.Tree("team-skill"); err != nil {
		t.Fatalf("org draft not promoted: %v", err)
	}
	if _, err := os.Stat(filepath.Join(stack.GeneratedEffectiveSkillsDir(dir), "team-skill", "SKILL.md")); err != nil {
		t.Fatalf("promoted org skill did not reach the view: %v", err)
	}
	// A plain member cannot promote.
	memberDraft, err := orgStore.CreateDraft("team-skill-2", skillstore.Tree{
		skillstore.SkillFile: {Data: []byte("---\nname: team-skill-2\ndescription: t\n---\n\nTwo.\n")},
	}, skillstore.DraftMeta{Author: "agent", Reason: "proposal"})
	if err != nil {
		t.Fatal(err)
	}
	memberDir := filepath.Join(root, "spaces", "joe")
	if err := run(ctx, []string{"init", "--dir", memberDir, "--user", "joe", "--org", "acme"}); err != nil {
		t.Fatal(err)
	}
	// Make joe a plain member in the org scope.
	orgSettings, err := stack.ReadOrganization(orgDir)
	if err != nil {
		t.Fatal(err)
	}
	orgSettings.Members["joe"] = stack.OrgMember{Role: "member"}
	raw, err := yaml.Marshal(orgSettings)
	if err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"settings.yaml", "scope.yaml"} {
		if err := os.WriteFile(filepath.Join(orgDir, name), raw, 0600); err != nil {
			t.Fatal(err)
		}
	}
	if err := run(ctx, []string{"skill", "promote", "--dir", memberDir, "--user", "joe", "--id", memberDraft.ID}); err == nil {
		t.Fatal("member promoted an org draft")
	}
	if _, err := orgStore.Tree("team-skill-2"); err == nil {
		t.Fatal("member's draft went live in the org store")
	}
}

func TestImportRedactsSecretWithWarning(t *testing.T) {
	dir := t.TempDir()
	skill := filepath.Join(t.TempDir(), "SKILL.md")
	body := "---\nname: demo\ndescription: demo\n---\n\nANTHROPIC_API_KEY=sk-ant-api03-aabbccddeeff1122334455\n"
	if err := os.WriteFile(skill, []byte(body), 0600); err != nil {
		t.Fatal(err)
	}
	out := captureOutput(t, func() error {
		return run(context.Background(), []string{"skill", "install", "--dir", dir, "--user", "owner", "--name", "demo", "--from-file", skill, "--consent"})
	})
	if !strings.Contains(out, `"redacted":1`) {
		t.Fatalf("install did not report redaction: %s", out)
	}
	stored, err := os.ReadFile(filepath.Join(dir, "skills", "demo", "SKILL.md"))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(stored), "sk-ant-api03") {
		t.Fatal("secret value stored unredacted")
	}
	if !strings.Contains(string(stored), "<REDACTED>") {
		t.Fatalf("redaction marker missing: %s", stored)
	}
}
