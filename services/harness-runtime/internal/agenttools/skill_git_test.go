package agenttools

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// R10.8/R5.3: skill_manage install relays to the gateway's /v1/skill-git;
// the returned draft then gets the same protected approval card as every
// other write — the tool never publishes directly.
func TestSkillManageInstallRelaysToSkillGit(t *testing.T) {
	tools, _ := skillSpace(t, skillTree(demoSkillMD, nil))
	var gitBody map[string]any
	var draftBody map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/v1/skill-git":
			json.NewDecoder(r.Body).Decode(&gitBody)
			w.Header().Set("Content-Type", "application/json")
			w.Write([]byte(`{"draft_id":"d1","name":"pdf","commit":"` + strings.Repeat("ab", 20) + `","source_id":"97393e7e6b5ab8df"}`))
		case "/v1/skill-drafts":
			json.NewDecoder(r.Body).Decode(&draftBody)
			w.Header().Set("Content-Type", "application/json")
			w.Write([]byte(`{"input":"protected-form","form_url":"http://comm.local/skill-drafts/d1?nonce=n"}`))
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()
	tools.CommunicationURL = srv.URL
	tools.CommunicationAuth = "comm-secret"
	out := execJSON(t, tools, "skill_manage", map[string]any{
		"action": "install", "url": "https://github.com/o/r/tree/main/skills/pdf", "reason": "want pdf",
	})
	if gitBody["action"] != "install" || gitBody["url"] != "https://github.com/o/r/tree/main/skills/pdf" {
		t.Fatalf("git request %v", gitBody)
	}
	if out["draft_id"] != "d1" || out["name"] != "pdf" || out["state"] != "draft" {
		t.Fatalf("install result %v", out)
	}
	if out["confirm_url"] != "http://comm.local/skill-drafts/d1?nonce=n" || out["link_sent"] != true {
		t.Fatalf("approval card missing: %v", out)
	}
	if draftBody["draft_id"] != "d1" || draftBody["skill_name"] != "pdf" {
		t.Fatalf("card payload %v", draftBody)
	}
}

func TestSkillManageUpdateRelaysName(t *testing.T) {
	tools, _ := skillSpace(t, skillTree(demoSkillMD, nil))
	var gitBody map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/v1/skill-git":
			json.NewDecoder(r.Body).Decode(&gitBody)
			w.Header().Set("Content-Type", "application/json")
			w.Write([]byte(`{"draft_id":"d2","name":"pdf","commit":"` + strings.Repeat("ef", 20) + `","prev_commit":"` + strings.Repeat("cd", 20) + `","force_push":true}`))
		case "/v1/skill-drafts":
			w.Header().Set("Content-Type", "application/json")
			w.Write([]byte(`{"input":"protected-form","form_url":"http://comm.local/skill-drafts/d2?nonce=n"}`))
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()
	tools.CommunicationURL = srv.URL
	tools.CommunicationAuth = "comm-secret"
	out := execJSON(t, tools, "skill_manage", map[string]any{
		"action": "update", "name": "pdf",
	})
	if gitBody["action"] != "update" || gitBody["name"] != "pdf" {
		t.Fatalf("git request %v", gitBody)
	}
	if out["draft_id"] != "d2" || out["state"] != "draft" || out["force_push"] != true || out["confirm_url"] == "" {
		t.Fatalf("update result %v", out)
	}
}

func TestSkillManageCheckUpdatesReports(t *testing.T) {
	tools, _ := skillSpace(t, skillTree(demoSkillMD, nil))
	var gitBody map[string]any
	draftCalls := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/v1/skill-git":
			json.NewDecoder(r.Body).Decode(&gitBody)
			w.Header().Set("Content-Type", "application/json")
			w.Write([]byte(`{"reports":[{"name":"pdf","current":"` + strings.Repeat("cd", 20) + `","latest":"` + strings.Repeat("ef", 20) + `","update":true}]}`))
		case "/v1/skill-drafts":
			draftCalls++
			http.Error(w, "no card expected", http.StatusTeapot)
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()
	tools.CommunicationURL = srv.URL
	tools.CommunicationAuth = "comm-secret"
	out := execJSON(t, tools, "skill_manage", map[string]any{"action": "check_updates"})
	if gitBody["action"] != "check_updates" {
		t.Fatalf("git request %v", gitBody)
	}
	reports, _ := out["reports"].([]any)
	if len(reports) != 1 || reports[0].(map[string]any)["name"] != "pdf" || reports[0].(map[string]any)["update"] != true {
		t.Fatalf("check_updates result %v", out)
	}
	if draftCalls != 0 {
		t.Fatal("a read-only check must not mint an approval card")
	}
}

// A repository URL without a subtree returns the discovered candidates so
// the agent can retry with an explicit path (R10.5) — no card, no draft.
func TestSkillManageInstallCandidatesPassthrough(t *testing.T) {
	tools, _ := skillSpace(t, skillTree(demoSkillMD, nil))
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{"commit":"` + strings.Repeat("ab", 20) + `","source_id":"97393e7e6b5ab8df","candidates":[{"path":"skills/pdf","name":"pdf"},{"path":"skills/xlsx","name":"xlsx"}]}`))
	}))
	defer srv.Close()
	tools.CommunicationURL = srv.URL
	tools.CommunicationAuth = "comm-secret"
	out := execJSON(t, tools, "skill_manage", map[string]any{
		"action": "install", "url": "https://github.com/o/r",
	})
	candidates, _ := out["candidates"].([]any)
	if len(candidates) != 2 || candidates[0].(map[string]any)["name"] != "pdf" {
		t.Fatalf("candidates result %v", out)
	}
	if out["draft_id"] != nil || out["confirm_url"] != nil {
		t.Fatalf("a candidates response must not mint a card: %v", out)
	}
}

func TestSkillManageInstallHubError(t *testing.T) {
	tools, _ := skillSpace(t, skillTree(demoSkillMD, nil))
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, `{"error":"git ref main not found"}`, http.StatusBadGateway)
	}))
	defer srv.Close()
	tools.CommunicationURL = srv.URL
	tools.CommunicationAuth = "comm-secret"
	if _, err := tools.skillManage(context.Background(), map[string]any{
		"action": "install", "url": "https://github.com/o/r",
	}); err == nil || !strings.Contains(err.Error(), "git ref main not found") {
		t.Fatalf("hub error must surface: %v", err)
	}
}

// After a candidates response the agent retries with an explicit subtree
// (path), a pin (ref) and the draft name — the relay must forward all three.
func TestSkillManageInstallForwardsRefPathName(t *testing.T) {
	tools, _ := skillSpace(t, skillTree(demoSkillMD, nil))
	var gitBody map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		json.NewDecoder(r.Body).Decode(&gitBody)
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{"draft_id":"d4","name":"my-pdf"}`))
	}))
	defer srv.Close()
	tools.CommunicationURL = srv.URL
	tools.CommunicationAuth = "comm-secret"
	out := execJSON(t, tools, "skill_manage", map[string]any{
		"action": "install", "url": "https://github.com/o/r",
		"path": "skills/pdf", "ref": "v2", "name": "my-pdf",
	})
	if gitBody["path"] != "skills/pdf" || gitBody["ref"] != "v2" || gitBody["name"] != "my-pdf" {
		t.Fatalf("git request %v", gitBody)
	}
	if out["draft_id"] != "d4" || out["name"] != "my-pdf" {
		t.Fatalf("install result %v", out)
	}
}

// A private repository rides on a broker grant: the agent names the grant,
// the hub leases the token — the secret never crosses the agent channel.
func TestSkillManageInstallForwardsGrant(t *testing.T) {
	tools, _ := skillSpace(t, skillTree(demoSkillMD, nil))
	var gitBody map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		json.NewDecoder(r.Body).Decode(&gitBody)
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{"draft_id":"d5","name":"pdf"}`))
	}))
	defer srv.Close()
	tools.CommunicationURL = srv.URL
	tools.CommunicationAuth = "comm-secret"
	execJSON(t, tools, "skill_manage", map[string]any{
		"action": "install", "url": "https://github.com/o/private", "grant": "gh-token",
	})
	if gitBody["grant"] != "gh-token" {
		t.Fatalf("grant not forwarded: %v", gitBody)
	}
}
