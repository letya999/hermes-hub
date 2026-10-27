package skillstore

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"testing"
)

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

	// Exec bit survives publish -> load round trip.
	withExec := skillTree("x")
	withExec["scripts/run.sh"] = File{Data: []byte("#!/bin/sh\n"), Exec: true}
	if _, err := s.Publish("demo", withExec, IndexMeta{Author: "u"}); err != nil {
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
	if len(m.Requires.Env) != 2 || m.Requires.Env[0].Name != "API_KEY" || m.Requires.Env[1].Name != "TOKEN" {
		t.Fatalf("env requirements not folded: %+v", m.Requires.Env)
	}
	if len(m.Requires.Bins) != 1 || m.Requires.Bins[0] != "jq" {
		t.Fatalf("openclaw bins not folded: %+v", m.Requires.Bins)
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
	withExec["scripts/r.sh"] = File{Data: []byte("s"), Exec: true}
	rev1, err := s.Publish("demo", withExec, IndexMeta{Author: "import", Source: "git", SessionID: "sess-9"})
	if err != nil {
		t.Fatal(err)
	}
	idx, err := s.Index()
	if err != nil {
		t.Fatal(err)
	}
	rec, ok := idx.Skills["demo"]
	if !ok || rec.Revision != rev1 || rec.Source != "git" || rec.Author != "import" || rec.SessionID != "sess-9" {
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
