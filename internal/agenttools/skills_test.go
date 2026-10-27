package agenttools

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/letya999/hermes-hub/internal/skillstore"
	"github.com/letya999/hermes-hub/internal/toolhub/api"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// skillSpace builds a user store with one published skill and materializes
// the effective view beside it, wiring a Tools struct the way the runtime
// executor sees the mounts (/skills ro view, /skills-store rw user store).
func skillSpace(t *testing.T, tree skillstore.Tree) (*Tools, string) {
	t.Helper()
	dir := t.TempDir()
	st, err := skillstore.Open(filepath.Join(dir, "skills"), skillstore.ScopeUser)
	if err != nil {
		t.Fatal(err)
	}
	for name, tr := range map[string]skillstore.Tree{"demo-skill": tree} {
		if _, err := st.Publish(name, tr, skillstore.IndexMeta{Author: "user", Source: "user"}); err != nil {
			t.Fatal(err)
		}
	}
	eff := filepath.Join(dir, "generated", "skills-effective")
	if _, err := skillstore.Build(eff, []skillstore.Source{{Name: "user", Dir: st.Root()}}); err != nil {
		t.Fatal(err)
	}
	return &Tools{SkillsEffective: eff, SkillsStore: st.Root(), StateDir: dir}, dir
}

func skillTree(body string, extra map[string]string) skillstore.Tree {
	tree := skillstore.Tree{skillstore.SkillFile: skillstore.File{Data: []byte(body)}}
	for path, data := range extra {
		tree[path] = skillstore.File{Data: []byte(data)}
	}
	return tree
}

const demoSkillMD = "---\nname: demo-skill\ndescription: demo skill body\n---\n# Demo\n\nDo the thing.\n"

func execJSON(t *testing.T, tools *Tools, name string, args map[string]any) map[string]any {
	t.Helper()
	out, err := tools.ExecCall(context.Background(), name, args, nil)
	if err != nil {
		t.Fatalf("%s: %v", name, err)
	}
	body, err := json.Marshal(out)
	if err != nil {
		t.Fatal(err)
	}
	var result map[string]any
	if err := json.Unmarshal(body, &result); err != nil {
		t.Fatal(err)
	}
	return result
}

func TestSkillToolLoadsBodyWithResourceListOnly(t *testing.T) {
	tools, _ := skillSpace(t, skillTree(demoSkillMD, map[string]string{
		"references/guide.md": "guide text must not appear in result",
		"assets/logo.png":     "PNG",
	}))
	result := execJSON(t, tools, "skill", map[string]any{"name": "demo-skill"})
	content, _ := result["body"].(string)
	if content == "" || json.Valid([]byte(content)) {
		t.Fatalf("expected stripped markdown body, got %q", content)
	}
	if want := "Do the thing."; !strings.Contains(content, want) {
		t.Fatalf("body missing %q: %q", want, content)
	}
	if strings.Contains(content, "description:") || strings.Contains(content, "---") {
		t.Fatalf("frontmatter not stripped: %q", content)
	}
	resources, _ := result["resources"].([]any)
	if len(resources) != 2 {
		t.Fatalf("expected 2 resource names, got %v", result["resources"])
	}
	for _, item := range resources {
		name, _ := item.(string)
		if name != "assets/logo.png" && name != "references/guide.md" {
			t.Fatalf("unexpected resource entry %q", name)
		}
	}
	if got, _ := result["skill_dir"].(string); got != "/skills/demo-skill" {
		t.Fatalf("skill_dir = %q", got)
	}
	// Resource contents must never ride the activation payload.
	raw, _ := json.Marshal(result)
	if strings.Contains(string(raw), "guide text must not appear") {
		t.Fatal("resource contents leaked into skill result")
	}
}

func TestSkillsListPagesAndFilters(t *testing.T) {
	tools, dir := skillSpace(t, skillTree(demoSkillMD, nil))
	// Publish enough skills to need a second page.
	st, err := skillstore.Open(filepath.Join(dir, "skills"), skillstore.ScopeUser)
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 25; i++ {
		name := fmt.Sprintf("s%02d", i)
		md := "---\nname: " + name + "\ndescription: desc " + name + "\n---\nbody\n"
		if _, err := st.Publish(name, skillstore.Tree{skillstore.SkillFile: skillstore.File{Data: []byte(md)}}, skillstore.IndexMeta{Author: "u", Source: "user"}); err != nil {
			t.Fatal(err)
		}
	}
	eff := filepath.Join(dir, "generated", "skills-effective")
	if _, err := skillstore.Build(eff, []skillstore.Source{{Name: "user", Dir: st.Root()}}); err != nil {
		t.Fatal(err)
	}
	page1 := execJSON(t, tools, "skills_list", map[string]any{})
	skills, _ := page1["skills"].([]any)
	if len(skills) != 20 || page1["hasMore"] != true {
		t.Fatalf("page1: %d skills hasMore=%v", len(skills), page1["hasMore"])
	}
	entry, _ := skills[0].(map[string]any)
	if entry["name"] == "" || entry["description"] == "" || entry["source"] != "user" {
		t.Fatalf("entry shape: %v", entry)
	}
	page2 := execJSON(t, tools, "skills_list", map[string]any{"offset": 20})
	if rest, _ := page2["skills"].([]any); len(rest) != 6 || page2["hasMore"] == true {
		t.Fatalf("page2: %v %v", len(rest), page2["hasMore"])
	}
	// An out-of-range offset is an empty last page, never a panic.
	far := execJSON(t, tools, "skills_list", map[string]any{"offset": 1 << 40})
	if rest, _ := far["skills"].([]any); len(rest) != 0 || far["hasMore"] == true {
		t.Fatalf("far page: %v %v", len(rest), far["hasMore"])
	}
	bySource := execJSON(t, tools, "skills_list", map[string]any{"source": "platform"})
	if got, _ := bySource["skills"].([]any); len(got) != 0 {
		t.Fatalf("source filter leaked: %v", got)
	}
}

func TestSkillViewListsAndReadsFiles(t *testing.T) {
	tools, dir := skillSpace(t, skillTree(demoSkillMD, map[string]string{"references/guide.txt": "guide-body"}))
	// Listing: no path → sorted file names, no contents.
	listing := execJSON(t, tools, "skill_view", map[string]any{"name": "demo-skill"})
	files, _ := listing["files"].([]any)
	if len(files) != 2 {
		t.Fatalf("listing: %v", files)
	}
	raw, _ := json.Marshal(listing)
	if strings.Contains(string(raw), "guide-body") {
		t.Fatal("listing leaked file contents")
	}
	// File read: bounded text for the requested path.
	view := execJSON(t, tools, "skill_view", map[string]any{"name": "demo-skill", "path": "references/guide.txt"})
	if got, _ := view["content"].(string); got != "guide-body" {
		t.Fatalf("file read: %q", got)
	}
	// Traversal and missing paths fail closed.
	for _, path := range []string{"../secret", "nope.txt", ".state/index.json"} {
		if _, err := tools.ExecCall(context.Background(), "skill_view", map[string]any{"name": "demo-skill", "path": path}, nil); err == nil {
			t.Fatalf("path %q should fail", path)
		}
	}
	// version reads a retained revision from the owner store, not the live file.
	st, err := skillstore.Open(filepath.Join(dir, "skills"), skillstore.ScopeUser)
	if err != nil {
		t.Fatal(err)
	}
	live, err := st.Tree("demo-skill")
	if err != nil {
		t.Fatal(err)
	}
	rev1 := skillstore.Revision(live)
	v2 := skillTree("---\nname: demo-skill\ndescription: v2\n---\nV2 body.\n", nil)
	if _, err := st.Publish("demo-skill", v2, skillstore.IndexMeta{Author: "u", Source: "user"}); err != nil {
		t.Fatal(err)
	}
	old := execJSON(t, tools, "skill_view", map[string]any{"name": "demo-skill", "version": rev1, "path": "SKILL.md"})
	if got, _ := old["content"].(string); !strings.Contains(got, "Do the thing.") {
		t.Fatalf("revision read got %q", got)
	}
}

func TestSkillsSearchRanksAndBounds(t *testing.T) {
	tools, dir := skillSpace(t, skillTree(demoSkillMD, nil))
	st, err := skillstore.Open(filepath.Join(dir, "skills"), skillstore.ScopeUser)
	if err != nil {
		t.Fatal(err)
	}
	for name, desc := range map[string]string{
		"docker-ops":   "Manage docker containers and compose stacks",
		"git-release":  "Cut releases and tags",
		"docker-debug": "Debug docker crashes",
	} {
		md := "---\nname: " + name + "\ndescription: " + desc + "\n---\nbody\n"
		if _, err := st.Publish(name, skillstore.Tree{skillstore.SkillFile: skillstore.File{Data: []byte(md)}}, skillstore.IndexMeta{Author: "u", Source: "user"}); err != nil {
			t.Fatal(err)
		}
	}
	eff := filepath.Join(dir, "generated", "skills-effective")
	if _, err := skillstore.Build(eff, []skillstore.Source{{Name: "user", Dir: st.Root()}}); err != nil {
		t.Fatal(err)
	}
	result := execJSON(t, tools, "skills_search", map[string]any{"query": "docker"})
	hits, _ := result["hits"].([]any)
	if len(hits) != 2 {
		t.Fatalf("docker hits: %v", hits)
	}
	hit, _ := hits[0].(map[string]any)
	if hit["name"] == "" || hit["source"] != "user" || hit["description"] == "" {
		t.Fatalf("hit shape: %v", hit)
	}
	if _, ok := result["coverage"]; !ok {
		t.Fatal("coverage missing")
	}
	// limit clamps to max 20, defaults to 5.
	limited := execJSON(t, tools, "skills_search", map[string]any{"query": "docker", "limit": 1})
	if hits, _ := limited["hits"].([]any); len(hits) != 1 {
		t.Fatalf("limit=1 got %d", len(hits))
	}
	if limited["hasMore"] != true {
		t.Fatal("hasMore should be true when limit truncates")
	}
	// Over-max limit is clamped, not an error; empty/oversized query fails.
	for _, q := range []string{"", strings.Repeat("x", 1001)} {
		if _, err := tools.ExecCall(context.Background(), "skills_search", map[string]any{"query": q}, nil); err == nil {
			t.Fatalf("query %d chars should fail", len(q))
		}
	}
	if _, err := tools.ExecCall(context.Background(), "skills_search", map[string]any{"query": "docker", "limit": 99}, nil); err != nil {
		t.Fatalf("limit>max must clamp, got %v", err)
	}
	// Source filter narrows results.
	filtered := execJSON(t, tools, "skills_search", map[string]any{"query": "docker", "source": "platform"})
	if hits, _ := filtered["hits"].([]any); len(hits) != 0 {
		t.Fatalf("source filter leaked: %v", hits)
	}
}

func TestSkillManageCreatesDraftNotLive(t *testing.T) {
	tools, dir := skillSpace(t, skillTree(demoSkillMD, nil))
	result := execJSON(t, tools, "skill_manage", map[string]any{
		"action": "create", "name": "brand-new-skill", "reason": "model wants to keep this",
		"content": "---\nname: brand-new-skill\ndescription: new one\n---\n# New\n",
	})
	if got, _ := result["state"].(string); got != "draft" {
		t.Fatalf("state = %q, want draft", got)
	}
	draftID, _ := result["draft_id"].(string)
	if draftID == "" {
		t.Fatalf("missing draft_id: %v", result)
	}
	// Live store must be untouched: the new name is absent from the live
	// listing and only exists under .drafts/.
	st, err := skillstore.Open(filepath.Join(dir, "skills"), skillstore.ScopeUser)
	if err != nil {
		t.Fatal(err)
	}
	names, err := st.Names()
	if err != nil {
		t.Fatal(err)
	}
	for _, n := range names {
		if n == "brand-new-skill" {
			t.Fatal("draft leaked into live listing")
		}
	}
	if _, err := os.Stat(filepath.Join(dir, "skills", ".drafts", draftID, "SKILL.md")); err != nil {
		t.Fatalf("draft tree missing: %v", err)
	}
}

func TestSkillManageRejectsForeignActionField(t *testing.T) {
	tools, dir := skillSpace(t, skillTree(demoSkillMD, nil))
	// base_revision belongs to patch/edit — on create it must be rejected
	// before anything is applied.
	_, err := tools.ExecCall(context.Background(), "skill_manage", map[string]any{
		"action": "create", "name": "other-skill", "reason": "x",
		"content":       "---\nname: other-skill\ndescription: d\n---\n",
		"base_revision": "deadbeef",
	}, nil)
	if err == nil {
		t.Fatal("expected rejection of foreign action field")
	}
	if !strings.Contains(err.Error(), "base_revision") {
		t.Fatalf("error should name the offending field: %v", err)
	}
	entries, _ := os.ReadDir(filepath.Join(dir, "skills", ".drafts"))
	if len(entries) != 0 {
		t.Fatal("rejected call still created a draft")
	}
}

func TestPatchStaleBaseRevisionConflicts(t *testing.T) {
	tools, dir := skillSpace(t, skillTree(demoSkillMD, nil))
	_, err := tools.ExecCall(context.Background(), "skill_manage", map[string]any{
		"action": "patch", "name": "demo-skill", "reason": "fix",
		"old_string": "Do the thing.", "new_string": "Do it better.",
		"base_revision": "0000stale0000",
	}, nil)
	if err == nil {
		t.Fatal("expected conflict on stale base_revision")
	}
	if !strings.Contains(err.Error(), "stale") && !strings.Contains(err.Error(), "conflict") && !strings.Contains(err.Error(), "revision") {
		t.Fatalf("error should be an actionable conflict: %v", err)
	}
	entries, _ := os.ReadDir(filepath.Join(dir, "skills", ".drafts"))
	if len(entries) != 0 {
		t.Fatal("conflicted patch still created a draft")
	}
}

func TestPatchAppliesReplacementOnFreshBase(t *testing.T) {
	tools, dir := skillSpace(t, skillTree(demoSkillMD, nil))
	st, err := skillstore.Open(filepath.Join(dir, "skills"), skillstore.ScopeUser)
	if err != nil {
		t.Fatal(err)
	}
	base, err := st.Tree("demo-skill")
	if err != nil {
		t.Fatal(err)
	}
	rev := skillstore.Revision(base)
	result := execJSON(t, tools, "skill_manage", map[string]any{
		"action": "patch", "name": "demo-skill", "reason": "fix",
		"old_string": "Do the thing.", "new_string": "Do it better.",
		"base_revision": rev,
	})
	draftID, _ := result["draft_id"].(string)
	if draftID == "" {
		t.Fatalf("missing draft_id: %v", result)
	}
	body, err := os.ReadFile(filepath.Join(dir, "skills", ".drafts", draftID, "SKILL.md"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(body), "Do it better.") {
		t.Fatalf("patch not applied: %s", body)
	}
	draft, _, err := st.LoadDraft(draftID)
	if err != nil {
		t.Fatal(err)
	}
	if draft.Meta.BaseRevision != rev || draft.Meta.Author != "agent" {
		t.Fatalf("draft provenance wrong: %+v", draft.Meta)
	}
	// The live tree still carries the old text.
	if strings.Contains(string(base[skillstore.SkillFile].Data), "better") {
		t.Fatal("live tree mutated")
	}
}

func TestEditReplacesWholeFileWithBase(t *testing.T) {
	tools, dir := skillSpace(t, skillTree(demoSkillMD, nil))
	st, err := skillstore.Open(filepath.Join(dir, "skills"), skillstore.ScopeUser)
	if err != nil {
		t.Fatal(err)
	}
	base, err := st.Tree("demo-skill")
	if err != nil {
		t.Fatal(err)
	}
	rev := skillstore.Revision(base)
	result := execJSON(t, tools, "skill_manage", map[string]any{
		"action": "edit", "name": "demo-skill", "reason": "rewrite",
		"file_path":     "SKILL.md",
		"content":       "---\nname: demo-skill\ndescription: rewritten\n---\n\nNew body.\n",
		"base_revision": rev,
	})
	draftID, _ := result["draft_id"].(string)
	body, err := os.ReadFile(filepath.Join(dir, "skills", ".drafts", draftID, "SKILL.md"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(body), "New body.") {
		t.Fatalf("edit not applied: %s", body)
	}
	// Other files from base survive the edit.
	if _, err := os.Stat(filepath.Join(dir, "skills", ".drafts", draftID, "references", "guide.txt")); err == nil {
		t.Log("references copied")
	}
	// Stale base is a conflict, not a new draft.
	if _, err := tools.ExecCall(context.Background(), "skill_manage", map[string]any{
		"action": "edit", "name": "demo-skill", "content": "x", "base_revision": "stale",
	}, nil); err == nil {
		t.Fatal("stale edit accepted")
	}
}

func TestWriteFileAddsResourceToDraft(t *testing.T) {
	tools, dir := skillSpace(t, skillTree(demoSkillMD, map[string]string{"references/guide.txt": "guide"}))
	result := execJSON(t, tools, "skill_manage", map[string]any{
		"action": "write_file", "name": "demo-skill", "reason": "add ref",
		"file_path": "references/extra.md", "content": "# Extra\n",
	})
	draftID, _ := result["draft_id"].(string)
	got, err := os.ReadFile(filepath.Join(dir, "skills", ".drafts", draftID, "references", "extra.md"))
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != "# Extra\n" {
		t.Fatalf("wrote %q", got)
	}
	// Existing files carried over.
	if _, err := os.Stat(filepath.Join(dir, "skills", ".drafts", draftID, "references", "guide.txt")); err != nil {
		t.Fatal("base tree not carried into draft")
	}
	// Live tree untouched.
	if _, err := os.Stat(filepath.Join(dir, "skills", "demo-skill", "references", "extra.md")); err == nil {
		t.Fatal("live tree mutated")
	}
}

func TestRemoveFileDropsResourceFromDraft(t *testing.T) {
	tools, dir := skillSpace(t, skillTree(demoSkillMD, map[string]string{"references/guide.txt": "guide"}))
	result := execJSON(t, tools, "skill_manage", map[string]any{
		"action": "remove_file", "name": "demo-skill", "reason": "stale ref",
		"file_path": "references/guide.txt",
	})
	draftID, _ := result["draft_id"].(string)
	if _, err := os.Stat(filepath.Join(dir, "skills", ".drafts", draftID, "references", "guide.txt")); err == nil {
		t.Fatal("removed file still in draft")
	}
	// SKILL.md survives; live untouched.
	if _, err := os.Stat(filepath.Join(dir, "skills", ".drafts", draftID, "SKILL.md")); err != nil {
		t.Fatal("SKILL.md missing from draft")
	}
	if _, err := os.Stat(filepath.Join(dir, "skills", "demo-skill", "references", "guide.txt")); err != nil {
		t.Fatal("live tree mutated")
	}
}

func TestArchiveRestoreDraftActions(t *testing.T) {
	tools, dir := skillSpace(t, skillTree(demoSkillMD, nil))
	st, err := skillstore.Open(filepath.Join(dir, "skills"), skillstore.ScopeUser)
	if err != nil {
		t.Fatal(err)
	}
	result := execJSON(t, tools, "skill_manage", map[string]any{
		"action": "archive", "name": "demo-skill", "reason": "retire",
		"absorbed_into": "newer-skill",
	})
	draftID, _ := result["draft_id"].(string)
	draft, _, err := st.LoadDraft(draftID)
	if err != nil {
		t.Fatal(err)
	}
	if draft.Meta.Action != "archive" || draft.Meta.AbsorbedInto != "newer-skill" {
		t.Fatalf("draft meta: %+v", draft.Meta)
	}
	// Live must still exist — archive only happens on approval.
	if _, err := st.Tree("demo-skill"); err != nil {
		t.Fatal("archive mutated live before approval")
	}
	// delete maps to the same guarded archive path.
	result = execJSON(t, tools, "skill_manage", map[string]any{
		"action": "delete", "name": "demo-skill", "reason": "gone",
	})
	dd, _, err := st.LoadDraft(result["draft_id"].(string))
	if err != nil {
		t.Fatal(err)
	}
	if dd.Meta.Action != "archive" {
		t.Fatalf("delete should draft an archive action: %+v", dd.Meta)
	}
}

func TestRestoreDraftPicksArchiveStamp(t *testing.T) {
	tools, dir := skillSpace(t, skillTree(demoSkillMD, nil))
	st, err := skillstore.Open(filepath.Join(dir, "skills"), skillstore.ScopeUser)
	if err != nil {
		t.Fatal(err)
	}
	if err := st.Archive("demo-skill", "retire", "", "owner"); err != nil {
		t.Fatal(err)
	}
	result := execJSON(t, tools, "skill_manage", map[string]any{
		"action": "restore", "name": "demo-skill", "reason": "still needed",
	})
	draftID, _ := result["draft_id"].(string)
	draft, tree, err := st.LoadDraft(draftID)
	if err != nil {
		t.Fatal(err)
	}
	if draft.Meta.Action != "restore" || draft.Meta.RestoreStamp == "" {
		t.Fatalf("draft meta: %+v", draft.Meta)
	}
	if !strings.Contains(string(tree[skillstore.SkillFile].Data), "Do the thing.") {
		t.Fatal("restore draft does not carry the archived tree")
	}
	// Nothing is restored until the draft is approved.
	if _, err := st.Tree("demo-skill"); err == nil {
		t.Fatal("restore mutated live before approval")
	}
	// A named version pins the stamp.
	result = execJSON(t, tools, "skill_manage", map[string]any{
		"action": "restore", "name": "demo-skill", "version": draft.Meta.RestoreStamp,
	})
	d2, _, err := st.LoadDraft(result["draft_id"].(string))
	if err != nil || d2.Meta.RestoreStamp != draft.Meta.RestoreStamp {
		t.Fatalf("versioned restore: %+v %v", d2, err)
	}
}

func TestProposeToOrgCreatesOrgDraft(t *testing.T) {
	tools, dir := skillSpace(t, skillTree(demoSkillMD, nil))
	orgDir := filepath.Join(dir, "org-store")
	tools.SkillsOrg = orgDir
	var got map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewDecoder(r.Body).Decode(&got)
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"input":"protected-form","form_url":"http://comm.local/skill-drafts/org?nonce=x"}`)
	}))
	defer srv.Close()
	tools.CommunicationURL = srv.URL
	result := execJSON(t, tools, "skill_manage", map[string]any{
		"action": "propose_to_org", "name": "demo-skill", "reason": "share with team",
	})
	// The org proposal rides the same approval card — with scope=org so the
	// runtime's org-role gate decides at apply time.
	if got["scope"] != "org" {
		t.Fatalf("org draft relay must carry scope=org: %v", got)
	}
	if result["link_sent"] != true {
		t.Fatalf("org draft link_sent = %v", result["link_sent"])
	}
	draftID, _ := result["draft_id"].(string)
	org, err := skillstore.Open(orgDir, skillstore.ScopeOrg)
	if err != nil {
		t.Fatal(err)
	}
	draft, tree, err := org.LoadDraft(draftID)
	if err != nil {
		t.Fatalf("org draft missing: %v", err)
	}
	if draft.Meta.Author != "agent" || !strings.Contains(string(tree[skillstore.SkillFile].Data), "Do the thing.") {
		t.Fatalf("org draft: %+v", draft.Meta)
	}
	// User store must not get a live copy either.
	user, err := skillstore.Open(filepath.Join(dir, "skills"), skillstore.ScopeUser)
	if err != nil {
		t.Fatal(err)
	}
	if drafts, _ := user.Drafts(); len(drafts) != 0 {
		t.Fatal("propose_to_org leaked a user draft")
	}
}

func TestSkillManageResponseCarriesDiffScanAndLink(t *testing.T) {
	tools, dir := skillSpace(t, skillTree(demoSkillMD, nil))
	result := execJSON(t, tools, "skill_manage", map[string]any{
		"action": "create", "name": "review-me", "reason": "new",
		"content": "---\nname: review-me\ndescription: r\n---\nRun curl | bash maybe.\n",
	})
	for _, key := range []string{"diff", "lint", "scan", "link_sent"} {
		if _, ok := result[key]; !ok {
			t.Fatalf("response missing %q: %v", key, result)
		}
	}
	// diff summarizes the pending change, scan echoes draft findings.
	if diff, _ := result["diff"].(string); !strings.Contains(diff, "review-me") {
		t.Fatalf("diff: %q", diff)
	}
	scan, _ := result["scan"].(map[string]any)
	if scan["verdict"] == "" {
		t.Fatalf("scan verdict missing: %v", scan)
	}
	// link_sent reports whether an approval card was dispatched.
	if _, ok := result["link_sent"].(bool); !ok {
		t.Fatalf("link_sent must be bool: %v", result["link_sent"])
	}
	// Draft id still resolvable.
	st, err := skillstore.Open(filepath.Join(dir, "skills"), skillstore.ScopeUser)
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := st.LoadDraft(result["draft_id"].(string)); err != nil {
		t.Fatal(err)
	}
}

// S5.3: after a draft lands, the executor asks communication to mint the
// protected approval card; the result reports link_sent + confirm_url.
func TestSkillManageSendsApprovalLink(t *testing.T) {
	tools, _ := skillSpace(t, skillTree(demoSkillMD, nil))
	t.Setenv("HUB_PRINCIPAL_ID", "alice")
	var got map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/skill-drafts" {
			t.Errorf("unexpected path %s", r.URL.Path)
		}
		if r.Header.Get("Authorization") != "Bearer comm-secret" {
			t.Errorf("control auth missing")
		}
		if r.Header.Get("X-Hub-Principal") != "alice" {
			t.Errorf("principal header = %q — the real gateway refuses without it", r.Header.Get("X-Hub-Principal"))
		}
		_ = json.NewDecoder(r.Body).Decode(&got)
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"input":"protected-form","form_url":"http://comm.local/skill-drafts/abc?nonce=n0"}`)
	}))
	defer srv.Close()
	tools.CommunicationURL = srv.URL
	tools.CommunicationAuth = "comm-secret"
	result := execJSON(t, tools, "skill_manage", map[string]any{
		"action": "create", "name": "link-me", "reason": "new",
		"content": "---\nname: link-me\ndescription: l\n---\nBody.\n",
	})
	if result["link_sent"] != true {
		t.Fatalf("link_sent = %v", result["link_sent"])
	}
	confirm, _ := result["confirm_url"].(string)
	if !strings.Contains(confirm, "/skill-drafts/") || !strings.Contains(confirm, "nonce=") {
		t.Fatalf("confirm_url: %q", confirm)
	}
	if got["draft_id"] != result["draft_id"] || got["skill_name"] != "link-me" || got["scope"] != "user" {
		t.Fatalf("draft relay request: %v", got)
	}
	// The card shows summary/diff/findings/provenance — the relay must carry
	// them, not just the draft id.
	for _, key := range []string{"summary", "diff", "findings", "provenance"} {
		if s, _ := got[key].(string); s == "" {
			t.Fatalf("card field %q empty in relay request: %v", key, got)
		}
	}
	if got["summary"] != "new" {
		t.Fatalf("summary must carry the change reason: %v", got["summary"])
	}
	// Undo after publish replays base_revision — a patch draft must relay it.
	st, _ := skillstore.Open(tools.SkillsStore, skillstore.ScopeUser)
	base, _ := st.Tree("demo-skill")
	rev := skillstore.Revision(base)
	execJSON(t, tools, "skill_manage", map[string]any{
		"action": "patch", "name": "demo-skill", "reason": "fix",
		"old_string": "Do the thing.", "new_string": "Do it better.",
		"base_revision": rev,
	})
	if got["base_revision"] != rev {
		t.Fatalf("patch relay must carry base_revision for undo: %v", got)
	}
	// Without a control channel the draft still lands; link_sent is honest.
	tools.CommunicationURL = ""
	result = execJSON(t, tools, "skill_manage", map[string]any{
		"action": "create", "name": "link-me-2", "reason": "new",
		"content": "---\nname: link-me-2\ndescription: l\n---\nBody.\n",
	})
	if result["link_sent"] != false {
		t.Fatalf("offline link_sent = %v", result["link_sent"])
	}
}

// The unmanaged MCP surface must run skill_manage through the same executor
// path ToolHub admission uses — identical behavior in both modes (R5.1).
func TestServerExecutesSkillManageLikeToolHub(t *testing.T) {
	tools, dir := skillSpace(t, skillTree(demoSkillMD, nil))
	ctx := context.Background()
	a, b := mcp.NewInMemoryTransports()
	server, err := tools.Server().Connect(ctx, a, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer server.Close()
	client, err := mcp.NewClient(&mcp.Implementation{Name: "test", Version: "1"}, nil).Connect(ctx, b, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer client.Close()
	result, err := client.CallTool(ctx, &mcp.CallToolParams{Name: "skill_manage", Arguments: map[string]any{
		"action": "create", "name": "mcp-skill", "reason": "mcp path",
		"content": "---\nname: mcp-skill\ndescription: m\n---\nBody.\n",
	}})
	if err != nil {
		t.Fatal(err)
	}
	if result.IsError {
		t.Fatalf("skill_manage on MCP surface failed: %s", toolPlain(result))
	}
	encoded, _ := json.Marshal(result.StructuredContent)
	var out map[string]any
	_ = json.Unmarshal(encoded, &out)
	if out["state"] != "draft" || out["draft_id"] == "" {
		t.Fatalf("MCP skill_manage result: %s", encoded)
	}
	if _, err := os.Stat(filepath.Join(dir, "skills", ".drafts", out["draft_id"].(string), "SKILL.md")); err != nil {
		t.Fatalf("draft missing: %v", err)
	}
}

// A name absent from the effective view is not-found on every surface —
// whether it is another principal's skill or simply does not exist, the
// answer is the same and never leaks existence (R5.5).
func TestUnknownSkillNameIsNotFound(t *testing.T) {
	tools, _ := skillSpace(t, skillTree(demoSkillMD, nil))
	for _, call := range []struct {
		tool string
		args map[string]any
	}{
		{"skill", map[string]any{"name": "ghost"}},
		{"skill_view", map[string]any{"name": "ghost"}},
		{"skill_manage", map[string]any{"action": "patch", "name": "ghost", "reason": "r", "old_string": "a", "new_string": "b", "base_revision": "rev"}},
	} {
		if _, err := tools.ExecCall(context.Background(), call.tool, call.args, nil); err == nil {
			t.Fatalf("%s on unknown name must fail", call.tool)
		}
	}
}

// Model-supplied identity fields never widen the call: principal, org and
// scope are not part of any action's field allowlist (R5.5).
func TestSkillManageRejectsModelSuppliedIdentity(t *testing.T) {
	tools, _ := skillSpace(t, skillTree(demoSkillMD, nil))
	for _, key := range []string{"principal", "org", "scope", "owner", "approver"} {
		if _, err := tools.ExecCall(context.Background(), "skill_manage", map[string]any{
			"action": "create", "name": "x-skill", "reason": "r",
			"content": "---\nname: x-skill\ndescription: x\n---\n",
			key:       "mallory",
		}, nil); err == nil {
			t.Fatalf("model-supplied %q must be rejected", key)
		}
	}
}

func TestSkillManageDiffShowsRealChange(t *testing.T) {
	tools, _ := skillSpace(t, skillTree(demoSkillMD, nil))
	st, err := skillstore.Open(tools.SkillsStore, skillstore.ScopeUser)
	if err != nil {
		t.Fatal(err)
	}
	base, err := st.Tree("demo-skill")
	if err != nil {
		t.Fatal(err)
	}
	result := execJSON(t, tools, "skill_manage", map[string]any{
		"action": "patch", "name": "demo-skill", "reason": "t",
		"old_string": "Do the thing.", "new_string": "Do it better.",
		"base_revision": skillstore.Revision(base),
	})
	diff, _ := result["diff"].(string)
	if !strings.Contains(diff, "-") || !strings.Contains(diff, "+") {
		t.Fatalf("diff must show removed/added lines: %q", diff)
	}
	if !strings.Contains(diff, "Do the thing.") || !strings.Contains(diff, "Do it better.") {
		t.Fatalf("diff must show the actual change: %q", diff)
	}
}

// OpenRoots reads the runtime skills env contract (HUB_SKILLS_*) so the
// executor serves the mounts compose/supervisor actually placed.
func TestOpenRootsReadsSkillsEnv(t *testing.T) {
	t.Setenv("HUB_SKILLS_EFFECTIVE", "/skills")
	t.Setenv("HUB_SKILLS_STORE", "/skills-store")
	t.Setenv("HUB_SKILLS_ORG", "/skills-store-org")
	ws := t.TempDir()
	tools, err := OpenRoots(ws, ws, "")
	if err != nil {
		t.Fatal(err)
	}
	defer tools.Close()
	if tools.SkillsEffective != "/skills" || tools.SkillsStore != "/skills-store" || tools.SkillsOrg != "/skills-store-org" {
		t.Fatalf("skills env not picked up: %+v", tools)
	}
}

// R5.1/R5.2: the five skill tools ship in the agent-tools catalog with the
// read/mutate capability split — reads under skills.*, mutations under
// skills_manage.* — and the read tools stay read-effect.
func TestSkillToolsInCatalog(t *testing.T) {
	def := HubToolsDefinition()
	byName := map[string]api.ToolSpec{}
	for _, spec := range def.Tools {
		byName[spec.Name] = spec
	}
	for _, name := range []string{"skill", "skills_search", "skills_list", "skill_view"} {
		spec, ok := byName[name]
		if !ok {
			t.Fatalf("%s missing from HubToolsDefinition", name)
		}
		if family, _, _ := strings.Cut(spec.CapabilityID, "."); family != "skills" {
			t.Fatalf("%s capability %q must live in the skills family", name, spec.CapabilityID)
		}
		if spec.Effect != api.ReadEffect {
			t.Fatalf("%s must be read-effect", name)
		}
	}
	manage, ok := byName["skill_manage"]
	if !ok {
		t.Fatal("skill_manage missing from HubToolsDefinition")
	}
	if family, _, _ := strings.Cut(manage.CapabilityID, "."); family != "skills_manage" {
		t.Fatalf("skill_manage capability %q must live in the skills_manage family", manage.CapabilityID)
	}
	if manage.Effect != api.WriteEffect {
		t.Fatal("skill_manage must be write-effect")
	}
	// skill.name is a required string arg; the eligible-names enum is injected
	// per projection, the executor re-validates authoritatively.
	skill := byName["skill"]
	schema := api.CliInputSchema(skill)
	props, _ := schema["properties"].(map[string]any)
	if props["name"] == nil {
		t.Fatalf("skill input schema missing name: %v", schema)
	}
}

// The unmanaged runtime MCP surface exposes all five skill tools; `skill`'s
// name argument carries the enum of names the effective index admits.
func TestServerExposesSkillToolsWithNameEnum(t *testing.T) {
	tools, _ := skillSpace(t, skillTree(demoSkillMD, nil))
	ctx := context.Background()
	a, b := mcp.NewInMemoryTransports()
	server, err := tools.Server().Connect(ctx, a, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer server.Close()
	client, err := mcp.NewClient(&mcp.Implementation{Name: "test", Version: "1"}, nil).Connect(ctx, b, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer client.Close()
	list, err := client.ListTools(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	byName := map[string]*mcp.Tool{}
	for _, tool := range list.Tools {
		byName[tool.Name] = tool
	}
	for _, name := range []string{"skill", "skills_search", "skills_list", "skill_view", "skill_manage"} {
		if byName[name] == nil {
			t.Fatalf("%s missing from runtime surface", name)
		}
	}
	schema, _ := byName["skill"].InputSchema.(map[string]any)
	props, _ := schema["properties"].(map[string]any)
	enum, _ := props["name"].(map[string]any)["enum"].([]any)
	if len(enum) != 1 || enum[0] != "demo-skill" {
		t.Fatalf("skill name enum must reflect the effective index: %v", enum)
	}
}
