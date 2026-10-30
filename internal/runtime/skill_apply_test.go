package runtime

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/letya999/hermes-hub/internal/skillstore"
)

// SPEC-0083 R5.3: /v1/skill-apply is the only path that mutates the mounted
// store — the confirmation page relays here after the owner's click.
func TestSkillApplyPublishesDraft(t *testing.T) {
	storeDir := t.TempDir()
	t.Setenv("HUB_SKILLS_STORE", storeDir)
	t.Setenv("HUB_RUNTIME_AUTH", "runtime-secret")
	st, err := skillstore.Open(storeDir, skillstore.ScopeUser)
	if err != nil {
		t.Fatal(err)
	}
	draft, err := st.CreateDraft("new-skill", skillstore.Tree{
		skillstore.SkillFile: {Data: []byte("---\nname: new-skill\ndescription: demo\n---\n\nDo things.\n")},
	}, skillstore.DraftMeta{Author: "agent", Reason: "test"})
	if err != nil {
		t.Fatal(err)
	}
	body := `{"action":"publish","draft_id":"` + draft.ID + `","skill_name":"new-skill","scope":"user","approver":"alice"}`
	req := httptest.NewRequest(http.MethodPost, "/v1/skill-apply", strings.NewReader(body))
	req.Header.Set("Authorization", "Bearer runtime-secret")
	rec := httptest.NewRecorder()
	runtimeHandler().ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("apply status=%d body=%s", rec.Code, rec.Body.String())
	}
	if _, err := st.Tree("new-skill"); err != nil {
		t.Fatalf("draft did not publish: %v", err)
	}
	// Second apply of the same draft must fail (consumed).
	rec2 := httptest.NewRecorder()
	req2 := httptest.NewRequest(http.MethodPost, "/v1/skill-apply", strings.NewReader(body))
	req2.Header.Set("Authorization", "Bearer runtime-secret")
	runtimeHandler().ServeHTTP(rec2, req2)
	if rec2.Code == http.StatusOK {
		t.Fatal("draft applied twice")
	}
}

// A successful publish must refresh the agent-visible view in place —
// /skills is a bind-mounted copy of the store, so apply rebuilds it from
// the mounted sources and the persisted capability snapshot (R6.5: the
// catalog-update notice only fires when index.json actually changes).
func TestSkillApplyRebuildsEffectiveView(t *testing.T) {
	storeDir := t.TempDir()
	platformDir := t.TempDir()
	viewDir := t.TempDir()
	metaDir := t.TempDir()
	t.Setenv("HUB_SKILLS_STORE", storeDir)
	t.Setenv("HUB_SKILLS_PLATFORM", platformDir)
	t.Setenv("HUB_SKILLS_EFFECTIVE_RW", viewDir)
	t.Setenv("HUB_SKILLS_META", metaDir)
	t.Setenv("HUB_RUNTIME_AUTH", "runtime-secret")
	if err := skillstore.WriteViewMeta(metaDir, skillstore.ViewMeta{Capabilities: skillstore.Capabilities{
		Toolsets: map[string]bool{}, Connectors: map[string]bool{},
		Platform: "linux", Registries: map[string]bool{}, SkillCreds: map[string]map[string]bool{},
	}}); err != nil {
		t.Fatal(err)
	}
	sources := []skillstore.Source{{Name: "platform", Dir: platformDir}, {Name: "user", Dir: storeDir}}
	if _, err := skillstore.Build(viewDir, sources); err != nil {
		t.Fatal(err)
	}
	st, err := skillstore.Open(storeDir, skillstore.ScopeUser)
	if err != nil {
		t.Fatal(err)
	}
	draft, err := st.CreateDraft("new-skill", skillstore.Tree{
		skillstore.SkillFile: {Data: []byte("---\nname: new-skill\ndescription: demo\n---\n\nDo things.\n")},
	}, skillstore.DraftMeta{Author: "agent", Reason: "test"})
	if err != nil {
		t.Fatal(err)
	}
	body := `{"action":"publish","draft_id":"` + draft.ID + `","skill_name":"new-skill","scope":"user","approver":"alice"}`
	req := httptest.NewRequest(http.MethodPost, "/v1/skill-apply", strings.NewReader(body))
	req.Header.Set("Authorization", "Bearer runtime-secret")
	rec := httptest.NewRecorder()
	runtimeHandler().ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("apply status=%d body=%s", rec.Code, rec.Body.String())
	}
	if _, err := os.Stat(filepath.Join(viewDir, "new-skill", skillstore.SkillFile)); err != nil {
		t.Fatalf("published skill did not reach the effective view: %v", err)
	}
	idx, err := skillstore.ReadIndex(viewDir)
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, e := range idx.Skills {
		if e.Name == "new-skill" && e.Eligible {
			found = true
		}
	}
	if !found {
		t.Fatalf("index.json missing the published skill: %+v", idx.Skills)
	}
}

// Rollback is a store mutation too: the view must return to the base
// revision's tree immediately, not on the next spawn.
func TestSkillApplyRollbackRebuildsView(t *testing.T) {
	storeDir := t.TempDir()
	platformDir := t.TempDir()
	viewDir := t.TempDir()
	metaDir := t.TempDir()
	t.Setenv("HUB_SKILLS_STORE", storeDir)
	t.Setenv("HUB_SKILLS_PLATFORM", platformDir)
	t.Setenv("HUB_SKILLS_EFFECTIVE_RW", viewDir)
	t.Setenv("HUB_SKILLS_META", metaDir)
	t.Setenv("HUB_RUNTIME_AUTH", "runtime-secret")
	if err := skillstore.WriteViewMeta(metaDir, skillstore.ViewMeta{Capabilities: skillstore.Capabilities{
		Toolsets: map[string]bool{}, Connectors: map[string]bool{},
		Platform: "linux", Registries: map[string]bool{}, SkillCreds: map[string]map[string]bool{},
	}}); err != nil {
		t.Fatal(err)
	}
	st, err := skillstore.Open(storeDir, skillstore.ScopeUser)
	if err != nil {
		t.Fatal(err)
	}
	baseMD := "---\nname: s\ndescription: v1\n---\n\nVersion one.\n"
	if _, err := st.Publish("s", skillstore.Tree{skillstore.SkillFile: {Data: []byte(baseMD)}}, skillstore.IndexMeta{Author: "alice", Source: "test"}); err != nil {
		t.Fatal(err)
	}
	baseRev := skillstore.Revision(mustTree(t, st, "s"))
	sources := []skillstore.Source{{Name: "platform", Dir: platformDir}, {Name: "user", Dir: storeDir}}
	if _, err := skillstore.Build(viewDir, sources); err != nil {
		t.Fatal(err)
	}
	draft, err := st.CreateDraft("s", skillstore.Tree{
		skillstore.SkillFile: {Data: []byte("---\nname: s\ndescription: v2\n---\n\nVersion two.\n")},
	}, skillstore.DraftMeta{Author: "agent", Reason: "test", BaseRevision: baseRev})
	if err != nil {
		t.Fatal(err)
	}
	post := func(body string) *httptest.ResponseRecorder {
		req := httptest.NewRequest(http.MethodPost, "/v1/skill-apply", strings.NewReader(body))
		req.Header.Set("Authorization", "Bearer runtime-secret")
		rec := httptest.NewRecorder()
		runtimeHandler().ServeHTTP(rec, req)
		return rec
	}
	if rec := post(`{"action":"publish","draft_id":"` + draft.ID + `","skill_name":"s","scope":"user","approver":"alice"}`); rec.Code != http.StatusOK {
		t.Fatalf("publish status=%d", rec.Code)
	}
	body, err := os.ReadFile(filepath.Join(viewDir, "s", skillstore.SkillFile))
	if err != nil || !strings.Contains(string(body), "v2") {
		t.Fatalf("view did not pick up v2: %v", err)
	}
	rec := post(`{"action":"rollback","skill_name":"s","scope":"user","base_revision":"` + baseRev + `","approver":"alice"}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("rollback status=%d body=%s", rec.Code, rec.Body.String())
	}
	body, err = os.ReadFile(filepath.Join(viewDir, "s", skillstore.SkillFile))
	if err != nil {
		t.Fatal(err)
	}
	if string(body) != baseMD {
		t.Fatal("view did not return to the base revision after rollback")
	}
}

// A failed view rebuild must not mask a successful publish — the store
// mutation already landed and the draft is consumed, so the response is
// applied with a stale-view marker, not an error.
func TestSkillApplyReportsStaleViewOnRebuildFailure(t *testing.T) {
	storeDir := t.TempDir()
	viewDir := t.TempDir()
	t.Setenv("HUB_SKILLS_STORE", storeDir)
	t.Setenv("HUB_SKILLS_PLATFORM", t.TempDir())
	t.Setenv("HUB_SKILLS_EFFECTIVE_RW", viewDir)
	t.Setenv("HUB_SKILLS_META", filepath.Join(t.TempDir(), "missing"))
	t.Setenv("HUB_RUNTIME_AUTH", "runtime-secret")
	st, err := skillstore.Open(storeDir, skillstore.ScopeUser)
	if err != nil {
		t.Fatal(err)
	}
	draft, err := st.CreateDraft("s", skillstore.Tree{
		skillstore.SkillFile: {Data: []byte("---\nname: s\ndescription: d\n---\n\nBody.\n")},
	}, skillstore.DraftMeta{Author: "agent", Reason: "test"})
	if err != nil {
		t.Fatal(err)
	}
	body := `{"action":"publish","draft_id":"` + draft.ID + `","skill_name":"s","scope":"user","approver":"alice"}`
	req := httptest.NewRequest(http.MethodPost, "/v1/skill-apply", strings.NewReader(body))
	req.Header.Set("Authorization", "Bearer runtime-secret")
	rec := httptest.NewRecorder()
	runtimeHandler().ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("publish must still succeed, got %d body=%s", rec.Code, rec.Body.String())
	}
	var out struct {
		State string `json:"state"`
		View  string `json:"view"`
	}
	if err := json.NewDecoder(rec.Body).Decode(&out); err != nil {
		t.Fatal(err)
	}
	if out.State != "applied" || !strings.HasPrefix(out.View, "stale") {
		t.Fatalf("expected applied+stale, got %+v", out)
	}
}

// The rebuild must include the org tier: an org publish builds the view
// from platform+org+user, so org skills already in the view survive a
// rebuild and the newly published org skill lands too.
func TestSkillApplyOrgPublishRebuildsOrgTier(t *testing.T) {
	orgDir := t.TempDir()
	userDir := t.TempDir()
	platformDir := t.TempDir()
	viewDir := t.TempDir()
	metaDir := t.TempDir()
	t.Setenv("HUB_SKILLS_STORE", userDir)
	t.Setenv("HUB_SKILLS_ORG", orgDir)
	t.Setenv("HUB_SKILLS_PLATFORM", platformDir)
	t.Setenv("HUB_SKILLS_EFFECTIVE_RW", viewDir)
	t.Setenv("HUB_SKILLS_META", metaDir)
	t.Setenv("HUB_ORGANIZATION_ROLE", "owner")
	t.Setenv("HUB_RUNTIME_AUTH", "runtime-secret")
	if err := skillstore.WriteViewMeta(metaDir, skillstore.ViewMeta{Capabilities: skillstore.Capabilities{
		Toolsets: map[string]bool{}, Connectors: map[string]bool{},
		Platform: "linux", Registries: map[string]bool{}, SkillCreds: map[string]map[string]bool{},
	}}); err != nil {
		t.Fatal(err)
	}
	org, err := skillstore.Open(orgDir, skillstore.ScopeOrg)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := org.Publish("existing-org", skillstore.Tree{
		skillstore.SkillFile: {Data: []byte("---\nname: existing-org\ndescription: d\n---\n\nBody.\n")},
	}, skillstore.IndexMeta{Author: "alice", Source: "org"}); err != nil {
		t.Fatal(err)
	}
	sources := []skillstore.Source{
		{Name: "platform", Dir: platformDir}, {Name: "org", Dir: orgDir}, {Name: "user", Dir: userDir},
	}
	if _, err := skillstore.Build(viewDir, sources); err != nil {
		t.Fatal(err)
	}
	draft, err := org.CreateDraft("new-org", skillstore.Tree{
		skillstore.SkillFile: {Data: []byte("---\nname: new-org\ndescription: d\n---\n\nBody.\n")},
	}, skillstore.DraftMeta{Author: "agent", Reason: "test"})
	if err != nil {
		t.Fatal(err)
	}
	body := `{"action":"publish","draft_id":"` + draft.ID + `","skill_name":"new-org","scope":"org","approver":"alice"}`
	req := httptest.NewRequest(http.MethodPost, "/v1/skill-apply", strings.NewReader(body))
	req.Header.Set("Authorization", "Bearer runtime-secret")
	rec := httptest.NewRecorder()
	runtimeHandler().ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("org apply status=%d body=%s", rec.Code, rec.Body.String())
	}
	for _, name := range []string{"existing-org", "new-org"} {
		if _, err := os.Stat(filepath.Join(viewDir, name, skillstore.SkillFile)); err != nil {
			t.Fatalf("view missing org skill %s after rebuild: %v", name, err)
		}
	}
}

// Two applies racing on the same view destination must serialize: each
// rebuild replaces skill dirs in place, so interleaved writes would leave
// half-written trees. The final view holds both skills and a valid index.
func TestSkillApplySerializesViewRebuild(t *testing.T) {
	storeDir := t.TempDir()
	viewDir := t.TempDir()
	metaDir := t.TempDir()
	t.Setenv("HUB_SKILLS_STORE", storeDir)
	t.Setenv("HUB_SKILLS_PLATFORM", t.TempDir())
	t.Setenv("HUB_SKILLS_EFFECTIVE_RW", viewDir)
	t.Setenv("HUB_SKILLS_META", metaDir)
	t.Setenv("HUB_RUNTIME_AUTH", "runtime-secret")
	if err := skillstore.WriteViewMeta(metaDir, skillstore.ViewMeta{Capabilities: skillstore.Capabilities{
		Toolsets: map[string]bool{}, Connectors: map[string]bool{},
		Platform: "linux", Registries: map[string]bool{}, SkillCreds: map[string]map[string]bool{},
	}}); err != nil {
		t.Fatal(err)
	}
	st, err := skillstore.Open(storeDir, skillstore.ScopeUser)
	if err != nil {
		t.Fatal(err)
	}
	post := func(body string, out chan<- int) {
		req := httptest.NewRequest(http.MethodPost, "/v1/skill-apply", strings.NewReader(body))
		req.Header.Set("Authorization", "Bearer runtime-secret")
		rec := httptest.NewRecorder()
		runtimeHandler().ServeHTTP(rec, req)
		out <- rec.Code
	}
	results := make(chan int, 2)
	for _, name := range []string{"skill-a", "skill-b"} {
		draft, err := st.CreateDraft(name, skillstore.Tree{
			skillstore.SkillFile: {Data: []byte("---\nname: " + name + "\ndescription: d\n---\n\nBody.\n")},
		}, skillstore.DraftMeta{Author: "agent", Reason: "test"})
		if err != nil {
			t.Fatal(err)
		}
		go post(`{"action":"publish","draft_id":"`+draft.ID+`","skill_name":"`+name+`","scope":"user","approver":"alice"}`, results)
	}
	for i := 0; i < 2; i++ {
		if code := <-results; code != http.StatusOK {
			t.Fatalf("concurrent apply got %d", code)
		}
	}
	idx, err := skillstore.ReadIndex(viewDir)
	if err != nil {
		t.Fatalf("index.json unreadable after concurrent rebuilds: %v", err)
	}
	for _, name := range []string{"skill-a", "skill-b"} {
		found := false
		for _, e := range idx.Skills {
			if e.Name == name {
				found = true
			}
		}
		if !found {
			t.Fatalf("view lost %s under concurrent rebuilds: %+v", name, idx.Skills)
		}
	}
}

// Unauthenticated apply is refused before any store work.
func TestSkillApplyRequiresAuth(t *testing.T) {
	t.Setenv("HUB_RUNTIME_AUTH", "runtime-secret")
	req := httptest.NewRequest(http.MethodPost, "/v1/skill-apply", strings.NewReader(`{"action":"publish"}`))
	rec := httptest.NewRecorder()
	runtimeHandler().ServeHTTP(rec, req)
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("status=%d", rec.Code)
	}
}

// Org-scope drafts publish only when the approver holds an org admin/owner
// role; a member role is refused at apply time (R5.2).
func TestSkillApplyOrgRequiresAdminRole(t *testing.T) {
	orgDir := t.TempDir()
	t.Setenv("HUB_SKILLS_ORG", orgDir)
	t.Setenv("HUB_ORGANIZATION_ROLE", "member")
	t.Setenv("HUB_RUNTIME_AUTH", "runtime-secret")
	org, err := skillstore.Open(orgDir, skillstore.ScopeOrg)
	if err != nil {
		t.Fatal(err)
	}
	draft, err := org.CreateDraft("org-skill", skillstore.Tree{
		skillstore.SkillFile: {Data: []byte("---\nname: org-skill\ndescription: o\n---\n\nOrg.\n")},
	}, skillstore.DraftMeta{Author: "agent", Reason: "test"})
	if err != nil {
		t.Fatal(err)
	}
	body := `{"action":"publish","draft_id":"` + draft.ID + `","skill_name":"org-skill","scope":"org","approver":"alice"}`
	req := httptest.NewRequest(http.MethodPost, "/v1/skill-apply", strings.NewReader(body))
	req.Header.Set("Authorization", "Bearer runtime-secret")
	rec := httptest.NewRecorder()
	runtimeHandler().ServeHTTP(rec, req)
	if rec.Code != http.StatusForbidden {
		t.Fatalf("member-role org apply must be forbidden, got %d", rec.Code)
	}
	if _, err := org.Tree("org-skill"); err == nil {
		t.Fatal("org skill went live for a member approver")
	}
	// An org owner may publish the same draft.
	t.Setenv("HUB_ORGANIZATION_ROLE", "owner")
	rec2 := httptest.NewRecorder()
	req2 := httptest.NewRequest(http.MethodPost, "/v1/skill-apply", strings.NewReader(body))
	req2.Header.Set("Authorization", "Bearer runtime-secret")
	runtimeHandler().ServeHTTP(rec2, req2)
	if rec2.Code != http.StatusOK {
		t.Fatalf("owner-role org apply status=%d body=%s", rec2.Code, rec2.Body.String())
	}
	if _, err := org.Tree("org-skill"); err != nil {
		t.Fatalf("org draft did not land in the org store: %v", err)
	}
	// An org admin may publish the same way.
	t.Setenv("HUB_ORGANIZATION_ROLE", "admin")
	draft2, err := org.CreateDraft("org-skill-2", skillstore.Tree{
		skillstore.SkillFile: {Data: []byte("---\nname: org-skill-2\ndescription: o\n---\n\nOrg two.\n")},
	}, skillstore.DraftMeta{Author: "agent", Reason: "test"})
	if err != nil {
		t.Fatal(err)
	}
	body2 := `{"action":"publish","draft_id":"` + draft2.ID + `","skill_name":"org-skill-2","scope":"org","approver":"alice"}`
	rec3 := httptest.NewRecorder()
	req3 := httptest.NewRequest(http.MethodPost, "/v1/skill-apply", strings.NewReader(body2))
	req3.Header.Set("Authorization", "Bearer runtime-secret")
	runtimeHandler().ServeHTTP(rec3, req3)
	if rec3.Code != http.StatusOK {
		t.Fatalf("admin-role org apply status=%d body=%s", rec3.Code, rec3.Body.String())
	}
}

// The undo link posts action=rollback with the draft's base_revision;
// apply must restore that revision (S5.3 undo re-arm).
func TestSkillApplyRollbackRestoresBase(t *testing.T) {
	storeDir := t.TempDir()
	t.Setenv("HUB_SKILLS_STORE", storeDir)
	t.Setenv("HUB_RUNTIME_AUTH", "runtime-secret")
	st, err := skillstore.Open(storeDir, skillstore.ScopeUser)
	if err != nil {
		t.Fatal(err)
	}
	baseMD := "---\nname: s\ndescription: v1\n---\n\nVersion one.\n"
	if _, err := st.Publish("s", skillstore.Tree{skillstore.SkillFile: {Data: []byte(baseMD)}}, skillstore.IndexMeta{Author: "alice", Source: "test"}); err != nil {
		t.Fatal(err)
	}
	baseRev := skillstore.Revision(mustTree(t, st, "s"))
	draft, err := st.CreateDraft("s", skillstore.Tree{
		skillstore.SkillFile: {Data: []byte("---\nname: s\ndescription: v2\n---\n\nVersion two.\n")},
	}, skillstore.DraftMeta{Author: "agent", Reason: "test", BaseRevision: baseRev})
	if err != nil {
		t.Fatal(err)
	}
	post := func(body string) *httptest.ResponseRecorder {
		req := httptest.NewRequest(http.MethodPost, "/v1/skill-apply", strings.NewReader(body))
		req.Header.Set("Authorization", "Bearer runtime-secret")
		rec := httptest.NewRecorder()
		runtimeHandler().ServeHTTP(rec, req)
		return rec
	}
	if rec := post(`{"action":"publish","draft_id":"` + draft.ID + `","skill_name":"s","scope":"user","approver":"alice"}`); rec.Code != http.StatusOK {
		t.Fatalf("publish status=%d", rec.Code)
	}
	rec := post(`{"action":"rollback","skill_name":"s","scope":"user","base_revision":"` + baseRev + `","approver":"alice"}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("rollback status=%d body=%s", rec.Code, rec.Body.String())
	}
	tree := mustTree(t, st, "s")
	if string(tree[skillstore.SkillFile].Data) != baseMD {
		t.Fatal("rollback did not restore the base revision")
	}
}

func mustTree(t *testing.T, st *skillstore.Store, name string) skillstore.Tree {
	t.Helper()
	tree, err := st.Tree(name)
	if err != nil {
		t.Fatal(err)
	}
	return tree
}

// SPEC-0084 R8.2: /v1/skill-creds is the managed credential sink — the
// communication form relays (skill, values) here so the owning runtime
// writes its own store. Cells read .state/credentials at exec time.
func TestSkillCredsWritesStoreAndRejectsBadInput(t *testing.T) {
	storeDir := t.TempDir()
	t.Setenv("HUB_SKILLS_STORE", storeDir)
	t.Setenv("HUB_RUNTIME_AUTH", "runtime-secret")
	body := `{"skill":"demo","values":{"API_KEY":"s3cr3t"}}`
	req := httptest.NewRequest(http.MethodPost, "/v1/skill-creds", strings.NewReader(body))
	req.Header.Set("Authorization", "Bearer runtime-secret")
	rec := httptest.NewRecorder()
	runtimeHandler().ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("creds status=%d body=%s", rec.Code, rec.Body.String())
	}
	got, err := skillstore.ReadCredentials(storeDir, "demo")
	if err != nil || got["API_KEY"] != "s3cr3t" {
		t.Fatalf("stored credentials: %v %v", got, err)
	}
	bad := httptest.NewRequest(http.MethodPost, "/v1/skill-creds", strings.NewReader(`{"skill":"demo","values":{"lower_case":"x"}}`))
	bad.Header.Set("Authorization", "Bearer runtime-secret")
	rec2 := httptest.NewRecorder()
	runtimeHandler().ServeHTTP(rec2, bad)
	if rec2.Code == http.StatusOK {
		t.Fatal("invalid credential name accepted")
	}
}

// An unauthenticated /v1/skill-creds call must not reach the store.
func TestSkillCredsRequiresAuth(t *testing.T) {
	storeDir := t.TempDir()
	t.Setenv("HUB_SKILLS_STORE", storeDir)
	t.Setenv("HUB_RUNTIME_AUTH", "runtime-secret")
	req := httptest.NewRequest(http.MethodPost, "/v1/skill-creds", strings.NewReader(`{"skill":"demo","values":{"API_KEY":"x"}}`))
	rec := httptest.NewRecorder()
	runtimeHandler().ServeHTTP(rec, req)
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("unauthenticated status=%d", rec.Code)
	}
	if got, _ := skillstore.ReadCredentials(storeDir, "demo"); len(got) != 0 {
		t.Fatalf("unauthenticated write landed in store: %v", got)
	}
}
