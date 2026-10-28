package runtime

import (
	"net/http"
	"net/http/httptest"
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
