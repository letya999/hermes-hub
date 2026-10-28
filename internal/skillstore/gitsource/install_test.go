package gitsource

import (
	"context"
	"encoding/json"
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
	requests  []FetchRequest
}

func (f *fakeGit) Fetch(_ context.Context, req FetchRequest) (FetchResult, error) {
	f.requests = append(f.requests, req)
	return FetchResult{Commit: f.commit, Tree: f.tree, ForcePush: f.forcePush}, nil
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
	fake.tree = skillstore.Tree{"skills/pdf/SKILL.md": {Data: []byte("---\nname: pdf\ndescription: pdf\n---\nv2")}}
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
