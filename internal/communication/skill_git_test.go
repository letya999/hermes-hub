package communication

import (
	"archive/tar"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/letya999/hermes-hub/internal/skillstore/gitsource"
	"github.com/letya999/hermes-hub/internal/toolhub/api"
)

// skillGitStub answers /cli-exec requests the way a real git would: the
// archive call materialises the prepared tar inside the bound mirror, and
// ls-remote reports the pinned head.
type skillGitStub struct {
	store  string
	commit string
	tar    []byte
	names  string
	codes  map[string]int
	reqs   []api.CliExecRequest
}

func (s *skillGitStub) run(_ context.Context, req api.CliExecRequest) (api.CliExecResponse, error) {
	s.reqs = append(s.reqs, req)
	joined := strings.Join(req.Args, " ")
	for sub, code := range s.codes {
		if strings.Contains(joined, sub) {
			return api.CliExecResponse{ExitCode: code}, nil
		}
	}
	if strings.Contains(joined, "-o /work/.export.tar") {
		mirror := filepath.Join(s.store, ".sources", req.Workspace.Path)
		if err := os.WriteFile(filepath.Join(mirror, ".export.tar"), s.tar, 0o644); err != nil { // #nosec G703 -- test path under t.TempDir
			return api.CliExecResponse{}, err
		}
		return api.CliExecResponse{}, nil
	}
	if strings.Contains(joined, "ls-remote") {
		return api.CliExecResponse{Output: "ref: refs/heads/main\tHEAD\n" + s.commit + "\tHEAD\n" + s.commit + "\trefs/heads/main\n"}, nil
	}
	if strings.Contains(joined, "ls-tree") {
		return api.CliExecResponse{Output: s.names}, nil
	}
	return api.CliExecResponse{}, nil
}

func skillGitTar(t *testing.T, files map[string]string) []byte {
	t.Helper()
	var buf bytes.Buffer
	w := tar.NewWriter(&buf)
	for name, body := range files {
		if err := w.WriteHeader(&tar.Header{Name: name, Mode: 0o644, Size: int64(len(body)), Typeflag: tar.TypeReg}); err != nil {
			t.Fatal(err)
		}
		if _, err := w.Write([]byte(body)); err != nil {
			t.Fatal(err)
		}
	}
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

const testSkillMD = "---\nname: pdf\ndescription: test pdf\n---\nDo pdf things.\n"

// R10.6: an install relays through the bounded cell and lands as a scanned
// draft carrying the pinned commit — nothing publishes directly.
func TestSkillGitInstallProducesDraft(t *testing.T) {
	c := testConfig(t)
	c.ControlAuth = "control-token"
	c.SkillsStore = t.TempDir()
	commit := strings.Repeat("ab", 20)
	stub := &skillGitStub{store: c.SkillsStore, commit: commit,
		tar: skillGitTar(t, map[string]string{"skills/pdf/SKILL.md": testSkillMD})}
	c.CLIExec = stub.run
	g, err := New(c)
	if err != nil {
		t.Fatal(err)
	}
	body := `{"action":"install","url":"https://github.com/o/r/tree/main/skills/pdf"}`
	req := httptest.NewRequest(http.MethodPost, "/v1/skill-git", strings.NewReader(body))
	req.Header.Set("Authorization", "Bearer control-token")
	req.Header.Set("X-Hub-Principal", "alice")
	result := httptest.NewRecorder()
	g.Handler().ServeHTTP(result, req)
	if result.Code != http.StatusOK {
		t.Fatalf("install status=%d body=%s", result.Code, result.Body.String())
	}
	var out struct {
		DraftID  string `json:"draft_id"`
		Name     string `json:"name"`
		Commit   string `json:"commit"`
		SourceID string `json:"source_id"`
	}
	if err := json.Unmarshal(result.Body.Bytes(), &out); err != nil || out.DraftID == "" || out.Name != "pdf" || out.Commit != commit {
		t.Fatalf("bad install response: %s", result.Body.String())
	}
	if len(out.SourceID) != 16 {
		t.Fatalf("source id %q", out.SourceID)
	}
	for _, rq := range stub.reqs {
		if rq.Principal != "alice" || rq.Plan.DefinitionID != "skillgit" {
			t.Fatalf("request identity %+v", rq)
		}
	}
}

// R10.6: update refetches the tracked ref; a moved head lands as a draft and
// rewritten history surfaces as force_push.
func TestSkillGitUpdateProducesDraft(t *testing.T) {
	c := testConfig(t)
	c.ControlAuth = "control-token"
	c.SkillsStore = t.TempDir()
	prev := strings.Repeat("cd", 20)
	commit := strings.Repeat("ef", 20)
	repo := "https://github.com/o/r"
	sourceID := gitsource.SourceID(repo)
	if err := os.MkdirAll(filepath.Join(c.SkillsStore, "pdf"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(c.SkillsStore, "pdf", "SKILL.md"), []byte(testSkillMD), 0o644); err != nil {
		t.Fatal(err)
	}
	origin := fmt.Sprintf(`{"source_id":%q,"url":%q,"ref":"main","commit":%q,"path":"skills/pdf"}`, sourceID, repo, prev)
	if err := os.WriteFile(filepath.Join(c.SkillsStore, "pdf", ".origin.json"), []byte(origin), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(c.SkillsStore, ".sources", sourceID+".git"), 0o755); err != nil {
		t.Fatal(err)
	}
	stub := &skillGitStub{store: c.SkillsStore, commit: commit,
		tar: skillGitTar(t, map[string]string{"skills/pdf/SKILL.md": testSkillMD + "v2\n"})}
	stub.codes = map[string]int{"merge-base": 1}
	c.CLIExec = stub.run
	g, err := New(c)
	if err != nil {
		t.Fatal(err)
	}
	body := `{"action":"update","name":"pdf"}`
	req := httptest.NewRequest(http.MethodPost, "/v1/skill-git", strings.NewReader(body))
	req.Header.Set("Authorization", "Bearer control-token")
	req.Header.Set("X-Hub-Principal", "alice")
	result := httptest.NewRecorder()
	g.Handler().ServeHTTP(result, req)
	if result.Code != http.StatusOK {
		t.Fatalf("update status=%d body=%s", result.Code, result.Body.String())
	}
	var out struct {
		DraftID    string `json:"draft_id"`
		Name       string `json:"name"`
		Commit     string `json:"commit"`
		PrevCommit string `json:"prev_commit"`
		ForcePush  bool   `json:"force_push"`
	}
	if err := json.Unmarshal(result.Body.Bytes(), &out); err != nil || out.DraftID == "" || out.Name != "pdf" || out.Commit != commit || out.PrevCommit != prev || !out.ForcePush {
		t.Fatalf("bad update response: %s", result.Body.String())
	}
}

// R10.8: check_updates resolves each tracked ref head-only — no import —
// and reports which skills moved upstream.
func TestSkillGitCheckUpdates(t *testing.T) {
	c := testConfig(t)
	c.ControlAuth = "control-token"
	c.SkillsStore = t.TempDir()
	prev := strings.Repeat("cd", 20)
	commit := strings.Repeat("ef", 20)
	repo := "https://github.com/o/r"
	sourceID := gitsource.SourceID(repo)
	if err := os.MkdirAll(filepath.Join(c.SkillsStore, "pdf"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(c.SkillsStore, "pdf", "SKILL.md"), []byte(testSkillMD), 0o644); err != nil {
		t.Fatal(err)
	}
	origin := fmt.Sprintf(`{"source_id":%q,"url":%q,"ref":"main","commit":%q,"path":"skills/pdf"}`, sourceID, repo, prev)
	if err := os.WriteFile(filepath.Join(c.SkillsStore, "pdf", ".origin.json"), []byte(origin), 0o644); err != nil {
		t.Fatal(err)
	}
	stub := &skillGitStub{store: c.SkillsStore, commit: commit}
	c.CLIExec = stub.run
	g, err := New(c)
	if err != nil {
		t.Fatal(err)
	}
	body := `{"action":"check_updates"}`
	req := httptest.NewRequest(http.MethodPost, "/v1/skill-git", strings.NewReader(body))
	req.Header.Set("Authorization", "Bearer control-token")
	req.Header.Set("X-Hub-Principal", "alice")
	result := httptest.NewRecorder()
	g.Handler().ServeHTTP(result, req)
	if result.Code != http.StatusOK {
		t.Fatalf("check_updates status=%d body=%s", result.Code, result.Body.String())
	}
	var out struct {
		Reports []struct {
			Name    string `json:"name"`
			Current string `json:"current"`
			Latest  string `json:"latest"`
			Update  bool   `json:"update"`
		} `json:"reports"`
	}
	if err := json.Unmarshal(result.Body.Bytes(), &out); err != nil || len(out.Reports) != 1 || out.Reports[0].Name != "pdf" || out.Reports[0].Current != prev || out.Reports[0].Latest != commit || !out.Reports[0].Update {
		t.Fatalf("bad check_updates response: %s", result.Body.String())
	}
	for _, rq := range stub.reqs {
		if rq.Workspace.Scope != "" {
			t.Fatal("check_updates must never bind the mirror workspace")
		}
	}
}

// R10.7: sources reports the tracked mirrors with the skills each feeds.
func TestSkillGitSources(t *testing.T) {
	c := testConfig(t)
	c.ControlAuth = "control-token"
	c.SkillsStore = t.TempDir()
	repo := "https://github.com/o/r"
	sourceID := gitsource.SourceID(repo)
	if err := os.MkdirAll(filepath.Join(c.SkillsStore, ".sources", sourceID+".git"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(c.SkillsStore, "pdf"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(c.SkillsStore, "pdf", "SKILL.md"), []byte(testSkillMD), 0o644); err != nil {
		t.Fatal(err)
	}
	origin := fmt.Sprintf(`{"source_id":%q,"url":%q,"commit":%q,"path":"skills/pdf"}`, sourceID, repo, strings.Repeat("ab", 20))
	if err := os.WriteFile(filepath.Join(c.SkillsStore, "pdf", ".origin.json"), []byte(origin), 0o644); err != nil {
		t.Fatal(err)
	}
	c.CLIExec = (&skillGitStub{}).run
	g, err := New(c)
	if err != nil {
		t.Fatal(err)
	}
	body := `{"action":"sources"}`
	req := httptest.NewRequest(http.MethodPost, "/v1/skill-git", strings.NewReader(body))
	req.Header.Set("Authorization", "Bearer control-token")
	req.Header.Set("X-Hub-Principal", "alice")
	result := httptest.NewRecorder()
	g.Handler().ServeHTTP(result, req)
	if result.Code != http.StatusOK {
		t.Fatalf("sources status=%d body=%s", result.Code, result.Body.String())
	}
	var out struct {
		Sources []struct {
			ID     string   `json:"id"`
			URL    string   `json:"url"`
			Skills []string `json:"skills"`
		} `json:"sources"`
	}
	if err := json.Unmarshal(result.Body.Bytes(), &out); err != nil || len(out.Sources) != 1 || out.Sources[0].ID != sourceID || out.Sources[0].URL != repo || len(out.Sources[0].Skills) != 1 || out.Sources[0].Skills[0] != "pdf" {
		t.Fatalf("bad sources response: %s", result.Body.String())
	}
}

// R10.7: remove_source deletes the mirror; the installed skills keep their
// revisions but lose update tracking, which the response must name.
func TestSkillGitRemoveSource(t *testing.T) {
	c := testConfig(t)
	c.ControlAuth = "control-token"
	c.SkillsStore = t.TempDir()
	repo := "https://github.com/o/r"
	sourceID := gitsource.SourceID(repo)
	mirror := filepath.Join(c.SkillsStore, ".sources", sourceID+".git")
	if err := os.MkdirAll(mirror, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(c.SkillsStore, "pdf"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(c.SkillsStore, "pdf", "SKILL.md"), []byte(testSkillMD), 0o644); err != nil {
		t.Fatal(err)
	}
	origin := fmt.Sprintf(`{"source_id":%q,"url":%q,"commit":%q,"path":"skills/pdf"}`, sourceID, repo, strings.Repeat("ab", 20))
	if err := os.WriteFile(filepath.Join(c.SkillsStore, "pdf", ".origin.json"), []byte(origin), 0o644); err != nil {
		t.Fatal(err)
	}
	c.CLIExec = (&skillGitStub{}).run
	g, err := New(c)
	if err != nil {
		t.Fatal(err)
	}
	body := `{"action":"remove_source","name":"` + sourceID + `"}`
	req := httptest.NewRequest(http.MethodPost, "/v1/skill-git", strings.NewReader(body))
	req.Header.Set("Authorization", "Bearer control-token")
	req.Header.Set("X-Hub-Principal", "alice")
	result := httptest.NewRecorder()
	g.Handler().ServeHTTP(result, req)
	if result.Code != http.StatusOK {
		t.Fatalf("remove_source status=%d body=%s", result.Code, result.Body.String())
	}
	var out struct {
		Removed bool     `json:"removed"`
		Skills  []string `json:"skills"`
	}
	if err := json.Unmarshal(result.Body.Bytes(), &out); err != nil || !out.Removed || len(out.Skills) != 1 || out.Skills[0] != "pdf" {
		t.Fatalf("bad remove_source response: %s", result.Body.String())
	}
	if _, err := os.Stat(mirror); !os.IsNotExist(err) {
		t.Fatal("mirror still on disk")
	}
}

// R10.5: a repository URL with no subtree returns discovery candidates via
// names-only ls-tree — the caller must pick one; nothing is imported yet.
func TestSkillGitInstallCandidates(t *testing.T) {
	c := testConfig(t)
	c.ControlAuth = "control-token"
	c.SkillsStore = t.TempDir()
	commit := strings.Repeat("ab", 20)
	stub := &skillGitStub{store: c.SkillsStore, commit: commit,
		names: "skills/pdf/SKILL.md\nskills/xlsx/SKILL.md\nREADME.md\n"}
	c.CLIExec = stub.run
	g, err := New(c)
	if err != nil {
		t.Fatal(err)
	}
	body := `{"action":"install","url":"https://github.com/o/r"}`
	req := httptest.NewRequest(http.MethodPost, "/v1/skill-git", strings.NewReader(body))
	req.Header.Set("Authorization", "Bearer control-token")
	req.Header.Set("X-Hub-Principal", "alice")
	result := httptest.NewRecorder()
	g.Handler().ServeHTTP(result, req)
	if result.Code != http.StatusOK {
		t.Fatalf("install status=%d body=%s", result.Code, result.Body.String())
	}
	var out struct {
		DraftID    string `json:"draft_id"`
		Candidates []struct {
			Path string `json:"path"`
			Name string `json:"name"`
		} `json:"candidates"`
	}
	if err := json.Unmarshal(result.Body.Bytes(), &out); err != nil || out.DraftID != "" || len(out.Candidates) != 2 || out.Candidates[0].Name != "pdf" || out.Candidates[1].Name != "xlsx" {
		t.Fatalf("bad candidates response: %s", result.Body.String())
	}
}

// The gateway takes its gitlab_host and /cli-exec runner from env so the
// rendered stack needs no code path differences (R10.1, R10.3).
func TestSkillGitConfigFromEnv(t *testing.T) {
	t.Setenv("HUB_GITLAB_HOST", "gitlab.example.io")
	t.Setenv("HUB_TOOLHIVE_ADMISSION_ENDPOINT", "http://127.0.0.1:8545/admit")
	t.Setenv("TELEGRAM_ALLOWED_USERS", "42")
	t.Setenv("HUB_COMMUNICATION_SPOOL", t.TempDir())
	c, err := ConfigFromEnv()
	if err != nil {
		t.Fatal(err)
	}
	if c.GitLabHost != "gitlab.example.io" {
		t.Fatalf("gitlab host %q", c.GitLabHost)
	}
	if c.CLIExec == nil {
		t.Fatal("cli-exec runner missing — every skill-git call fails closed")
	}
}

func TestSkillGitConfigFromEnvYAMLPath(t *testing.T) {
	dir := t.TempDir()
	spool := filepath.Join(dir, "spool")
	state := filepath.Join(dir, "state")
	ws := filepath.Join(dir, "workspace")
	cfgPath := filepath.Join(dir, "comm.yaml")
	body := "organization_id: personal\nspool_dir: " + filepath.ToSlash(spool) + "\nusers:\n  - id: alice\n    enabled: true\n    telegram_ids: [42]\n    state_dir: " + filepath.ToSlash(state) + "\n    workspace_dir: " + filepath.ToSlash(ws) + "\n"
	if err := os.WriteFile(cfgPath, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("HUB_COMMUNICATION_CONFIG", cfgPath)
	t.Setenv("HUB_GITLAB_HOST", "gitlab.example.io")
	t.Setenv("HUB_TOOLHIVE_ADMISSION_ENDPOINT", "http://127.0.0.1:8545/admit")
	c, err := ConfigFromEnv()
	if err != nil {
		t.Fatal(err)
	}
	if c.GitLabHost != "gitlab.example.io" || c.CLIExec == nil {
		t.Fatal("yaml path lost the skill-git wiring")
	}
}
