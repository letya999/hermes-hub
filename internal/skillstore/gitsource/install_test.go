package gitsource

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/letya999/hermes-hub/internal/skillstore"
)

type fakeGit struct {
	tree      skillstore.Tree
	commit    string
	forcePush bool
	dropped   []string
	requests  []FetchRequest
}

func (f *fakeGit) Fetch(_ context.Context, req FetchRequest) (FetchResult, error) {
	f.requests = append(f.requests, req)
	return FetchResult{Commit: f.commit, Tree: f.tree, ForcePush: f.forcePush, Dropped: f.dropped}, nil
}

func TestInstallPinsCommitAndRecordsOrigin(t *testing.T) {
	root := t.TempDir()
	st, err := skillstore.Open(root, skillstore.ScopeUser)
	if err != nil {
		t.Fatal(err)
	}
	commit := strings.Repeat("a1", 20)
	fake := &fakeGit{commit: commit, tree: skillstore.Tree{
		"skills/pdf/SKILL.md":  {Data: []byte("---\nname: pdf\ndescription: pdf\n---\nbody")},
		"skills/pdf/run.sh":    {Data: []byte("#!/bin/sh\n")},
		"skills/docx/SKILL.md": {Data: []byte("---\nname: docx\n---\n")},
	}}
	res, err := Install(context.Background(), st, InstallOptions{
		URL:  "https://github.com/anthropics/skills",
		Path: "skills/pdf",
		Meta: skillstore.DraftMeta{Author: "test"},
	}, fake)
	if err != nil {
		t.Fatalf("Install: %v", err)
	}
	if res.Commit != commit || res.DraftID == "" || res.Name != "pdf" {
		t.Fatalf("unexpected result %+v", res)
	}
	if len(res.Candidates) != 0 {
		t.Fatalf("explicit path must not return candidates: %v", res.Candidates)
	}
	// The draft tree must carry hub-owned origin metadata pinned to the SHA.
	drafts, err := st.Drafts()
	if err != nil || len(drafts) != 1 {
		t.Fatalf("drafts: %v %d", err, len(drafts))
	}
	originRaw, err := os.ReadFile(filepath.Join(root, ".drafts", drafts[0].ID, ".origin.json")) // #nosec G703 -- test path under t.TempDir
	if err != nil {
		t.Fatalf("origin record: %v", err)
	}
	var origin Origin
	if err := json.Unmarshal(originRaw, &origin); err != nil {
		t.Fatalf("origin json: %v", err)
	}
	if origin.Commit != commit || origin.URL != "https://github.com/anthropics/skills" || origin.Path != "skills/pdf" || origin.SourceID == "" {
		t.Fatalf("origin %+v", origin)
	}
	// Publishing makes the skill live while .origin.json stays hub-private.
	if _, err := st.PublishDraft(res.DraftID, "owner"); err != nil {
		t.Fatalf("PublishDraft: %v", err)
	}
	tree, err := st.Tree("pdf")
	if err != nil {
		t.Fatal(err)
	}
	if _, leaked := tree[".origin.json"]; leaked {
		t.Fatalf(".origin.json must not reach the agent-visible tree")
	}
	if _, err := os.Stat(filepath.Join(root, "pdf", ".origin.json")); err != nil {
		t.Fatalf("origin file must persist hub-side: %v", err)
	}
	if _, err := os.Stat(filepath.Join(root, ".sources", origin.SourceID+".git")); err != nil {
		t.Fatalf("bare mirror dir must exist: %v", err)
	}
}

// A fetch cell killed mid-operation leaves *.lock files in the mirror — git
// then refuses every later fetch ("Unable to create shallow.lock") and the
// source is bricked until manual cleanup. Install must clear stale locks
// before fetching; the lock is advisory, never live state.
func TestInstallClearsStaleMirrorLock(t *testing.T) {
	root := t.TempDir()
	st, err := skillstore.Open(root, skillstore.ScopeUser)
	if err != nil {
		t.Fatal(err)
	}
	mirror := filepath.Join(root, ".sources", SourceID("https://github.com/anthropics/skills")+".git")
	if err := os.MkdirAll(mirror, 0o755); err != nil {
		t.Fatal(err)
	}
	lock := filepath.Join(mirror, "shallow.lock")
	if err := os.WriteFile(lock, []byte("stale"), 0o644); err != nil { // #nosec G703 -- test path
		t.Fatal(err)
	}
	fake := &fakeGit{commit: strings.Repeat("a1", 20), tree: skillstore.Tree{
		"skills/pdf/SKILL.md": {Data: []byte("---\nname: pdf\ndescription: pdf\n---\nbody")},
	}}
	if _, err := Install(context.Background(), st, InstallOptions{
		URL: "https://github.com/anthropics/skills", Path: "skills/pdf",
		Meta: skillstore.DraftMeta{Author: "test"},
	}, fake); err != nil {
		t.Fatalf("Install: %v", err)
	}
	if _, err := os.Stat(lock); !os.IsNotExist(err) {
		t.Fatal("stale mirror lock survived Install")
	}
}

// R10.6: the origin record carries the exported tree's content hash and the
// scan verdict the draft pipeline produced — provenance is tamper-evident.
func TestInstallOriginCarriesTreeHashAndScanVerdict(t *testing.T) {
	root := t.TempDir()
	st, err := skillstore.Open(root, skillstore.ScopeUser)
	if err != nil {
		t.Fatal(err)
	}
	fake := &fakeGit{commit: strings.Repeat("b2", 20), tree: skillstore.Tree{
		"skills/pdf/SKILL.md": {Data: []byte("---\nname: pdf\n---\nbody")},
	}}
	res, err := Install(context.Background(), st, InstallOptions{
		URL: "https://github.com/anthropics/skills", Path: "skills/pdf",
		Meta: skillstore.DraftMeta{Author: "test"},
	}, fake)
	if err != nil {
		t.Fatalf("Install: %v", err)
	}
	raw, err := os.ReadFile(filepath.Join(root, ".drafts", res.DraftID, ".origin.json")) // #nosec G703 -- test path
	if err != nil {
		t.Fatal(err)
	}
	var origin Origin
	if err := json.Unmarshal(raw, &origin); err != nil {
		t.Fatal(err)
	}
	if len(origin.TreeSHA) != 64 {
		t.Fatalf("tree_sha %+v", origin)
	}
	if origin.Scan == "" {
		t.Fatalf("scan verdict missing: %+v", origin)
	}
}

func TestInstallFetchRequestCarriesPathAndNames(t *testing.T) {
	root := t.TempDir()
	st, err := skillstore.Open(root, skillstore.ScopeUser)
	if err != nil {
		t.Fatal(err)
	}
	fake := &fakeGit{commit: strings.Repeat("a1", 20), tree: skillstore.Tree{
		"skills/pdf/SKILL.md": {Data: []byte("---\nname: pdf\n---\n")},
	}}
	// An explicit path fetches that subtree's content; a bare repo URL asks
	// for names only so discovery never streams the whole repository.
	if _, err = Install(context.Background(), st, InstallOptions{
		URL: "https://github.com/o/r", Path: "skills/pdf", Meta: skillstore.DraftMeta{Author: "t"},
	}, fake); err != nil {
		t.Fatal(err)
	}
	withPath := fake.requests[len(fake.requests)-1]
	if withPath.Path != "skills/pdf" || withPath.Names {
		t.Fatalf("explicit-path fetch must carry the subtree, got %+v", withPath)
	}
	if _, err = Install(context.Background(), st, InstallOptions{
		URL: "https://github.com/o/r", Meta: skillstore.DraftMeta{Author: "t"},
	}, fake); err != nil {
		t.Fatal(err)
	}
	bare := fake.requests[len(fake.requests)-1]
	if bare.Path != "" || !bare.Names {
		t.Fatalf("discovery fetch must request names only, got %+v", bare)
	}
}

func TestInstallMultiSkillRepoRequiresSelection(t *testing.T) {
	root := t.TempDir()
	st, err := skillstore.Open(root, skillstore.ScopeUser)
	if err != nil {
		t.Fatal(err)
	}
	fake := &fakeGit{commit: strings.Repeat("b2", 20), tree: skillstore.Tree{
		"skills/pdf/SKILL.md":  {Data: []byte("---\nname: pdf\n---\n")},
		"skills/docx/SKILL.md": {Data: []byte("---\nname: docx\n---\n")},
	}}
	res, err := Install(context.Background(), st, InstallOptions{
		URL:  "https://github.com/anthropics/skills",
		Meta: skillstore.DraftMeta{Author: "test"},
	}, fake)
	if err != nil {
		t.Fatalf("Install: %v", err)
	}
	if len(res.Candidates) != 2 || res.DraftID != "" {
		t.Fatalf("ambiguous repo must return candidates, not a draft: %+v", res)
	}
	// A repeat call with an explicit selection installs from the same mirror.
	res2, err := Install(context.Background(), st, InstallOptions{
		URL:  "https://github.com/anthropics/skills",
		Path: "skills/docx",
		Meta: skillstore.DraftMeta{Author: "test"},
	}, fake)
	if err != nil || res2.Name != "docx" || res2.SourceID != res.SourceID {
		t.Fatalf("selection install: %+v %v", res2, err)
	}
}

// A subtree install must draft under the SKILL.md frontmatter name —
// publish requires name == dir, so path.Base of the repo directory is
// only a fallback. Bundler-style repos (skills/ingester → name:
// book-ingester) otherwise mint drafts that can never be approved.
func TestInstallSubdirSkillUsesFrontmatterName(t *testing.T) {
	root := t.TempDir()
	st, err := skillstore.Open(root, skillstore.ScopeUser)
	if err != nil {
		t.Fatal(err)
	}
	fake := &fakeGit{commit: strings.Repeat("c3", 20), tree: skillstore.Tree{
		"skills/ingester/SKILL.md": {Data: []byte("---\nname: book-ingester\ndescription: d\n---\nbody")},
	}}
	res, err := Install(context.Background(), st, InstallOptions{
		URL:  "https://github.com/letya999/ai-refinery-book-bundler",
		Path: "skills/ingester",
		Meta: skillstore.DraftMeta{Author: "test"},
	}, fake)
	if err != nil {
		t.Fatalf("Install: %v", err)
	}
	if res.Name != "book-ingester" {
		t.Fatalf("frontmatter name must win over path base: %q", res.Name)
	}
	// The draft must actually be publishable — the reported live failure.
	if _, err := st.PublishDraft(res.DraftID, "owner"); err != nil {
		t.Fatalf("publish drafted subtree skill: %v", err)
	}
}

// A single-skill repo keeps SKILL.md at its root: bare-URL discovery offers
// "." named after the repo; selecting it drafts the whole tree under the
// frontmatter name (publish requires name == dir) with an empty origin path
// so Update re-exports the full tree.
func TestInstallRootSkillRepo(t *testing.T) {
	root := t.TempDir()
	st, err := skillstore.Open(root, skillstore.ScopeUser)
	if err != nil {
		t.Fatal(err)
	}
	fake := &fakeGit{commit: strings.Repeat("e5", 20), tree: skillstore.Tree{
		"SKILL.md":                  {Data: []byte("---\nname: prose-polish-ru\ndescription: d\n---\nbody")},
		"references/a.md":           {Data: []byte("see [b](b.md)")},
		"references/b.md":           {Data: []byte("leaf")},
		".github/workflows/ci.yml":  {Data: []byte("ci")},
		".gitignore":                {Data: []byte("dist")},
		".origin.json":              {Data: []byte(`{"commit":"spoof"}`)},
		"node_modules/pkg/index.js": {Data: []byte("vendored")},
		"node_modules/dep/x/y/z.js": {Data: []byte("vendored")},
	}}
	for i := 0; i < 12; i++ {
		fake.tree[fmt.Sprintf("node_modules/pkg%d/index.js", i)] = skillstore.File{Data: []byte("vendored")}
	}
	res, err := Install(context.Background(), st, InstallOptions{
		URL: "https://github.com/letya999/prose-polish-ru", Meta: skillstore.DraftMeta{Author: "t"},
	}, fake)
	if err != nil {
		t.Fatalf("Install: %v", err)
	}
	if len(res.Candidates) != 1 || res.Candidates[0].Path != "." || res.Candidates[0].Name != "prose-polish-ru" {
		t.Fatalf("root skill must surface as the \".\" candidate named after the repo: %+v", res.Candidates)
	}
	res, err = Install(context.Background(), st, InstallOptions{
		URL: "https://github.com/letya999/prose-polish-ru", Path: ".", Meta: skillstore.DraftMeta{Author: "t"},
	}, fake)
	if err != nil {
		t.Fatalf("root install: %v", err)
	}
	if res.DraftID == "" || res.Name != "prose-polish-ru" {
		t.Fatalf("root install result %+v", res)
	}
	req := fake.requests[len(fake.requests)-1]
	if req.Path != "" || req.Names {
		t.Fatalf("root install must fetch the full tree, got %+v", req)
	}
	draft, tree, err := st.LoadDraft(res.DraftID)
	if err != nil {
		t.Fatal(err)
	}
	if tree["SKILL.md"].Data == nil || tree["references/a.md"].Data == nil {
		t.Fatalf("root draft must carry the whole tree: %v", tree)
	}
	// A foreign package links freely between its files — the import path
	// admits resolvable chains and reports them instead of refusing. The
	// filtered dot plumbing is named in the same audit notes.
	joined := strings.Join(draft.Meta.ImportNotes, "\n")
	if !draft.Meta.Imported || !strings.Contains(joined, "admitted") || !strings.Contains(joined, "dropped") {
		t.Fatalf("git draft must be marked imported with link+drop notes: %+v", draft.Meta)
	}
	// Repo plumbing, hub-reserved dot-space and vendored dependencies
	// (node_modules is reproducible noise that blows file quotas) never
	// enter the skill tree — a shipped .origin.json would otherwise spoof
	// provenance metadata.
	for p := range tree {
		for _, seg := range strings.Split(p, "/") {
			if strings.HasPrefix(seg, ".") || seg == "node_modules" {
				t.Fatalf("reserved/vendored path %q reached the draft tree", p)
			}
		}
	}
	if !strings.Contains(joined, "node_modules") {
		t.Fatalf("vendored deps must be named in the drop notes: %v", draft.Meta.ImportNotes)
	}
	// A repo can carry thousands of vendored files — the audit note
	// summarizes instead of enumerating every path into draft metadata.
	for _, n := range draft.Meta.ImportNotes {
		if strings.Contains(n, "dropped") && len(n) > 400 {
			t.Fatalf("drop note must bound its listing: %q", n)
		}
	}
	if !strings.Contains(joined, "+") || !strings.Contains(joined, "more") {
		t.Fatalf("drop note must summarize the overflow tail: %v", draft.Meta.ImportNotes)
	}
	// The transport drops vendored paths while streaming the archive —
	// before the tree ever exists — so the audit names what fetch filtered.
	fake.dropped = []string{"node_modules/fetched-only.js"}
	res, err = Install(context.Background(), st, InstallOptions{
		URL: "https://github.com/letya999/prose-polish-ru", Path: ".", Meta: skillstore.DraftMeta{Author: "t"},
	}, fake)
	if err != nil {
		t.Fatalf("re-install after transport drop: %v", err)
	}
	draft2, _, err := st.LoadDraft(res.DraftID)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(strings.Join(draft2.Meta.ImportNotes, "\n"), "node_modules/fetched-only.js") {
		t.Fatalf("transport-dropped paths must reach the audit notes: %v", draft2.Meta.ImportNotes)
	}
	if _, err := st.PublishDraft(res.DraftID, "owner"); err != nil {
		t.Fatalf("root draft must publish under the frontmatter name: %v", err)
	}
	raw, err := os.ReadFile(filepath.Join(root, "prose-polish-ru", ".origin.json")) // #nosec G703 -- test path
	if err != nil {
		t.Fatal(err)
	}
	var o Origin
	if err := json.Unmarshal(raw, &o); err != nil || o.Path != "" {
		t.Fatalf("root origin must record empty path: %+v %v", o, err)
	}
}

func TestUpdateProducesDiffDraftAndFlagsForcePush(t *testing.T) {
	root := t.TempDir()
	st, err := skillstore.Open(root, skillstore.ScopeUser)
	if err != nil {
		t.Fatal(err)
	}
	oldCommit := strings.Repeat("c1", 20)
	fake := &fakeGit{commit: oldCommit, tree: skillstore.Tree{
		"skills/pdf/SKILL.md": {Data: []byte("---\nname: pdf\ndescription: pdf\n---\nv1")},
	}}
	res, err := Install(context.Background(), st, InstallOptions{
		URL: "https://github.com/anthropics/skills", Path: "skills/pdf",
		Meta: skillstore.DraftMeta{Author: "test"},
	}, fake)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := st.PublishDraft(res.DraftID, "owner"); err != nil {
		t.Fatal(err)
	}
	// Upstream rewrote the ref: new commit does not descend from the old one.
	fake.commit = strings.Repeat("d4", 20)
	fake.forcePush = true
	// Upstream may add foreign-shaped content: a chained resource link is
	// an import, not authored hub content — admitted with notes.
	fake.tree = skillstore.Tree{
		"skills/pdf/SKILL.md":        {Data: []byte("---\nname: pdf\ndescription: pdf\n---\nv2")},
		"skills/pdf/references/a.md": {Data: []byte("see [b](b.md)")},
		"skills/pdf/references/b.md": {Data: []byte("leaf")},
		"skills/pdf/.github/ci.yml":  {Data: []byte("ci")},
	}
	fake.dropped = []string{"node_modules/fetched-only.js"}
	upd, err := Update(context.Background(), st, "pdf", fake, skillstore.DraftMeta{Author: "git"})
	if err != nil {
		t.Fatalf("Update: %v", err)
	}
	if !upd.ForcePush || upd.Commit != fake.commit || upd.DraftID == "" {
		t.Fatalf("update result %+v", upd)
	}
	draft, tree, err := st.LoadDraft(upd.DraftID)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(tree["SKILL.md"].Data), "v2") {
		t.Fatalf("update draft must carry the new tree")
	}
	unotes := strings.Join(draft.Meta.ImportNotes, "\n")
	if !draft.Meta.Imported || !strings.Contains(unotes, "admitted") || !strings.Contains(unotes, "dropped") {
		t.Fatalf("update draft must stay imported with link+drop notes: %+v", draft.Meta)
	}
	if !strings.Contains(unotes, "node_modules/fetched-only.js") {
		t.Fatalf("transport-dropped paths must reach the update audit: %v", draft.Meta.ImportNotes)
	}
	if tree[".github/ci.yml"].Data != nil {
		t.Fatal("dot path reached the update draft tree")
	}
	if draft.Meta.BaseRevision == "" {
		t.Fatalf("update draft must record the installed base revision")
	}
	// The update draft's origin record must point at the NEW commit —
	// publishing it would otherwise restore stale tracking metadata.
	raw, err := os.ReadFile(filepath.Join(root, ".drafts", upd.DraftID, ".origin.json")) // #nosec G703 -- test path
	if err != nil {
		t.Fatal(err)
	}
	var o Origin
	if err := json.Unmarshal(raw, &o); err != nil || o.Commit != fake.commit {
		t.Fatalf("update draft origin must record the new commit: %v %v", o, err)
	}
	prevRaw, err := os.ReadFile(filepath.Join(root, "pdf", ".origin.json")) // #nosec G703 -- test path
	if err != nil {
		t.Fatal(err)
	}
	var prev Origin
	if err := json.Unmarshal(prevRaw, &prev); err != nil {
		t.Fatal(err)
	}
	if len(o.TreeSHA) != 64 || o.TreeSHA == prev.TreeSHA {
		t.Fatalf("update origin must carry the NEW tree hash, not the stale one: %+v", o)
	}
	if o.Scan != draft.Meta.ScanVerdict {
		t.Fatalf("update origin scan must be this draft's verdict: %+v vs %q", o, draft.Meta.ScanVerdict)
	}
}

// The provenance hash must be stable: the same tree content always digests
// identically regardless of map iteration order, and content changes move it.
func TestTreeDigestIsDeterministic(t *testing.T) {
	a := skillstore.Tree{"x.md": {Data: []byte("1")}, "y.md": {Data: []byte("2")}}
	b := skillstore.Tree{"y.md": {Data: []byte("2")}, "x.md": {Data: []byte("1")}}
	if treeDigest(a) != treeDigest(b) {
		t.Fatalf("digest must not depend on iteration order")
	}
	c := skillstore.Tree{"x.md": {Data: []byte("1")}, "y.md": {Data: []byte("3")}}
	if treeDigest(a) == treeDigest(c) {
		t.Fatalf("content change must move the digest")
	}
}

func TestUpdateFetchRequestCarriesOriginPath(t *testing.T) {
	root := t.TempDir()
	st, err := skillstore.Open(root, skillstore.ScopeUser)
	if err != nil {
		t.Fatal(err)
	}
	fake := &fakeGit{commit: strings.Repeat("c1", 20), tree: skillstore.Tree{
		"skills/pdf/SKILL.md": {Data: []byte("---\nname: pdf\ndescription: pdf\n---\nv1")},
	}}
	res, err := Install(context.Background(), st, InstallOptions{
		URL: "https://github.com/anthropics/skills", Path: "skills/pdf",
		Meta: skillstore.DraftMeta{Author: "test"},
	}, fake)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := st.PublishDraft(res.DraftID, "owner"); err != nil {
		t.Fatal(err)
	}
	if _, err = Update(context.Background(), st, "pdf", fake, skillstore.DraftMeta{Author: "git"}); err != nil {
		t.Fatal(err)
	}
	req := fake.requests[len(fake.requests)-1]
	if req.Path != "skills/pdf" || req.PrevCommit != fake.commit || req.Names {
		t.Fatalf("update fetch must carry the tracked subtree and prev commit, got %+v", req)
	}
}

// Update hits the same mirror — a lock left by a killed fetch bricks the
// tracked-ref refresh exactly like it bricks install.
func TestUpdateClearsStaleMirrorLock(t *testing.T) {
	root := t.TempDir()
	st, err := skillstore.Open(root, skillstore.ScopeUser)
	if err != nil {
		t.Fatal(err)
	}
	fake := &fakeGit{commit: strings.Repeat("c1", 20), tree: skillstore.Tree{
		"skills/pdf/SKILL.md": {Data: []byte("---\nname: pdf\ndescription: pdf\n---\nv1")},
	}}
	res, err := Install(context.Background(), st, InstallOptions{
		URL: "https://github.com/anthropics/skills", Path: "skills/pdf",
		Meta: skillstore.DraftMeta{Author: "test"},
	}, fake)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := st.PublishDraft(res.DraftID, "owner"); err != nil {
		t.Fatal(err)
	}
	mirror := filepath.Join(root, ".sources", SourceID("https://github.com/anthropics/skills")+".git")
	lock := filepath.Join(mirror, "shallow.lock")
	if err := os.WriteFile(lock, []byte("stale"), 0o644); err != nil { // #nosec G703 -- test path
		t.Fatal(err)
	}
	if _, err := Update(context.Background(), st, "pdf", fake, skillstore.DraftMeta{Author: "git"}); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(lock); !os.IsNotExist(err) {
		t.Fatal("stale mirror lock survived Update")
	}
}

func TestCheckUpdatesReportsWithoutImporting(t *testing.T) {
	root := t.TempDir()
	st, err := skillstore.Open(root, skillstore.ScopeUser)
	if err != nil {
		t.Fatal(err)
	}
	oldCommit := strings.Repeat("c1", 20)
	fake := &fakeGit{commit: oldCommit, tree: skillstore.Tree{
		"skills/pdf/SKILL.md": {Data: []byte("---\nname: pdf\ndescription: pdf\n---\nv1")},
	}}
	res, err := Install(context.Background(), st, InstallOptions{
		URL: "https://github.com/anthropics/skills", Path: "skills/pdf",
		Meta: skillstore.DraftMeta{Author: "test"},
	}, fake)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := st.PublishDraft(res.DraftID, "owner"); err != nil {
		t.Fatal(err)
	}
	// A hand-installed skill has no origin and must not break the report.
	if _, err := st.Publish("local", skillstore.Tree{
		skillstore.SkillFile: {Data: []byte("---\nname: local\ndescription: local\n---\n")},
	}, skillstore.IndexMeta{Author: "test"}); err != nil {
		t.Fatal(err)
	}
	fake.commit = strings.Repeat("e5", 20)
	fake.tree = nil // a head-only probe returns no content
	reports, err := CheckUpdates(context.Background(), st, fake)
	if err != nil {
		t.Fatal(err)
	}
	if len(reports) != 1 || !reports[0].Update || reports[0].Latest != fake.commit || reports[0].Name != "pdf" {
		t.Fatalf("reports %+v", reports)
	}
	if !fake.requests[len(fake.requests)-1].HeadOnly {
		t.Fatalf("check_updates must fetch heads only, never content")
	}
	drafts, err := st.Drafts()
	if err != nil || len(drafts) != 0 {
		t.Fatalf("check_updates must not create drafts")
	}
}

func TestSourcesListAndRemoveTracksMirrors(t *testing.T) {
	root := t.TempDir()
	st, err := skillstore.Open(root, skillstore.ScopeUser)
	if err != nil {
		t.Fatal(err)
	}
	fake := &fakeGit{commit: strings.Repeat("f6", 20), tree: skillstore.Tree{
		"skills/pdf/SKILL.md": {Data: []byte("---\nname: pdf\ndescription: pdf\n---\nv1")},
	}}
	res, err := Install(context.Background(), st, InstallOptions{
		URL: "https://github.com/anthropics/skills", Path: "skills/pdf",
		Meta: skillstore.DraftMeta{Author: "test"},
	}, fake)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := st.PublishDraft(res.DraftID, "owner"); err != nil {
		t.Fatal(err)
	}
	sources, err := Sources(st)
	if err != nil || len(sources) != 1 {
		t.Fatalf("sources: %v %d", err, len(sources))
	}
	if sources[0].ID != res.SourceID || sources[0].URL != "https://github.com/anthropics/skills" {
		t.Fatalf("source %+v", sources[0])
	}
	if len(sources[0].Skills) != 1 || sources[0].Skills[0] != "pdf" {
		t.Fatalf("tracked skills %+v", sources[0].Skills)
	}
	if removed, err := RemoveSource(st, "../escape"); err == nil || removed {
		t.Fatalf("source id must be a fixed-shape hex: %v %v", removed, err)
	}
	removed, err := RemoveSource(st, res.SourceID)
	if err != nil || !removed {
		t.Fatalf("remove: %v %v", removed, err)
	}
	if _, err := os.Stat(filepath.Join(root, ".sources", res.SourceID+".git")); !os.IsNotExist(err) {
		t.Fatalf("mirror must be deleted")
	}
	sources, _ = Sources(st)
	if len(sources) != 0 {
		t.Fatalf("source list must be empty after remove")
	}
}

// A skills.sh page locates a skill by slug inside a GitHub repo: discovery
// resolves which candidate the slug names (root skill or subtree dir) and
// the install proceeds without the caller restating a repo path.
func TestInstallResolvesSkillsShSlug(t *testing.T) {
	root := t.TempDir()
	st, err := skillstore.Open(root, skillstore.ScopeUser)
	if err != nil {
		t.Fatal(err)
	}
	fake := &fakeGit{commit: strings.Repeat("c3", 20), tree: skillstore.Tree{
		"SKILL.md":        {Data: []byte("---\nname: grill-my-resume-as-manager\ndescription: d\n---\nbody")},
		"references/a.md": {Data: []byte("ref")},
	}}
	res, err := Install(context.Background(), st, InstallOptions{
		URL: "https://www.skills.sh/letya999/grill-my-resume-as-manager/grill-my-resume-as-manager",
	}, fake)
	if err != nil {
		t.Fatalf("skills.sh install: %v", err)
	}
	if res.DraftID == "" || res.Name != "grill-my-resume-as-manager" {
		t.Fatalf("slug must resolve the root skill: %+v", res)
	}
	draft, tree, err := st.LoadDraft(res.DraftID)
	if err != nil {
		t.Fatal(err)
	}
	if !draft.Meta.Imported || tree["SKILL.md"].Data == nil {
		t.Fatalf("draft %+v", draft.Meta)
	}
	// A slug matching nothing still returns candidates for a manual retry.
	fake2 := &fakeGit{commit: fake.commit, tree: skillstore.Tree{
		"skills/one/SKILL.md": {Data: []byte("---\nname: one\n---\n")},
	}}
	res2, err := Install(context.Background(), st, InstallOptions{
		URL: "https://www.skills.sh/acme/multi/no-such-skill",
	}, fake2)
	if err != nil || len(res2.Candidates) != 1 {
		t.Fatalf("unmatched slug must surface candidates: %+v %v", res2, err)
	}
}

// R10.6: an unmoved ref reports up_to_date instead of manufacturing a
// vacuous draft — repeated `skill update` must not fill .drafts with noise.
func TestUpdateUnmovedRefReportsUpToDate(t *testing.T) {
	root := t.TempDir()
	st, err := skillstore.Open(root, skillstore.ScopeUser)
	if err != nil {
		t.Fatal(err)
	}
	commit := strings.Repeat("c1", 20)
	fake := &fakeGit{commit: commit, tree: skillstore.Tree{
		"skills/pdf/SKILL.md": {Data: []byte("---\nname: pdf\ndescription: pdf\n---\nv1")},
	}}
	res, err := Install(context.Background(), st, InstallOptions{
		URL: "https://github.com/anthropics/skills", Path: "skills/pdf",
		Meta: skillstore.DraftMeta{Author: "test"},
	}, fake)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := st.PublishDraft(res.DraftID, "owner"); err != nil {
		t.Fatal(err)
	}
	upd, err := Update(context.Background(), st, "pdf", fake, skillstore.DraftMeta{Author: "git"})
	if err != nil {
		t.Fatalf("Update: %v", err)
	}
	if !upd.UpToDate || upd.DraftID != "" {
		t.Fatalf("unmoved ref must report up_to_date with no draft: %+v", upd)
	}
	drafts, err := st.Drafts()
	if err != nil {
		t.Fatal(err)
	}
	for _, d := range drafts {
		if d.Name == "pdf" && d.ID != res.DraftID {
			t.Fatalf("update manufactured a noise draft: %+v", d)
		}
	}
}
