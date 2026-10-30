package main

import (
	"archive/tar"
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/letya999/hermes-hub/internal/skillstore/gitsource"
	"github.com/letya999/hermes-hub/internal/toolhub/api"
)

// skillGitServer plays the controller's /cli-exec over HTTP the way
// skillGitStub plays it in-process for the communication tests.
type skillGitServer struct {
	store  string
	commit string
	tar    []byte
	names  string
	codes  map[string]int
	reqs   []api.CliExecRequest
}

func (s *skillGitServer) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path != "/cli-exec" {
		w.WriteHeader(http.StatusNotFound)
		return
	}
	var req api.CliExecRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		w.WriteHeader(http.StatusBadRequest)
		return
	}
	s.reqs = append(s.reqs, req)
	joined := strings.Join(req.Args, " ")
	for sub, code := range s.codes {
		if strings.Contains(joined, sub) {
			json.NewEncoder(w).Encode(api.CliExecResponse{ExitCode: code})
			return
		}
	}
	if strings.Contains(joined, "-o /work/.export.tar") {
		mirror := filepath.Join(s.store, ".sources", req.Workspace.Path)
		if err := os.WriteFile(filepath.Join(mirror, ".export.tar"), s.tar, 0o644); err != nil {
			w.WriteHeader(http.StatusInternalServerError)
			return
		}
		json.NewEncoder(w).Encode(api.CliExecResponse{})
		return
	}
	if strings.Contains(joined, "ls-remote") {
		ref := req.Args[len(req.Args)-1]
		if ref != "HEAD" {
			json.NewEncoder(w).Encode(api.CliExecResponse{Output: s.commit + "\trefs/heads/" + ref + "\n"})
			return
		}
		json.NewEncoder(w).Encode(api.CliExecResponse{Output: "ref: refs/heads/main\tHEAD\n" + s.commit + "\tHEAD\n" + s.commit + "\trefs/heads/main\n"})
		return
	}
	if strings.Contains(joined, "ls-tree") {
		json.NewEncoder(w).Encode(api.CliExecResponse{Output: s.names})
		return
	}
	json.NewEncoder(w).Encode(api.CliExecResponse{})
}

func (s *skillGitServer) start(t *testing.T) {
	t.Helper()
	srv := httptest.NewServer(s)
	t.Cleanup(srv.Close)
	t.Setenv("HUB_TOOLHIVE_ADMISSION_ENDPOINT", srv.URL+"/admit")
	t.Setenv("HUB_TOOLHIVE_ADMISSION_TOKEN", "")
}

func skillGitTarball(t *testing.T, files map[string]string) []byte {
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

const gitSkillMD = "---\nname: pdf\ndescription: test pdf\n---\nDo pdf things.\n"

// R10.6: the operator path runs the same gitsource flow — the bounded cell
// fetches, the scanned draft lands in the owner store, output is the result.
func TestSkillGitInstallURLDraft(t *testing.T) {
	dir := t.TempDir()
	commit := strings.Repeat("ab", 20)
	stub := &skillGitServer{store: filepath.Join(dir, "skills"), commit: commit,
		tar: skillGitTarball(t, map[string]string{"skills/pdf/SKILL.md": gitSkillMD})}
	stub.start(t)
	out := captureOutput(t, func() error {
		return run(context.Background(), []string{"skill", "install", "--dir", dir, "--user", "owner",
			"--url", "https://github.com/o/r/tree/main/skills/pdf"})
	})
	var result struct {
		DraftID string `json:"draft_id"`
		Name    string `json:"name"`
		Commit  string `json:"commit"`
	}
	if err := json.Unmarshal([]byte(out), &result); err != nil || result.DraftID == "" || result.Name != "pdf" || result.Commit != commit {
		t.Fatalf("install output: %v %s", err, out)
	}
	for _, rq := range stub.reqs {
		if rq.Principal != "owner" || rq.Plan.DefinitionID != "skillgit" {
			t.Fatalf("request identity %+v", rq)
		}
	}
}

// --ref pins the upstream ref and --path/--name pick the subtree and the
// draft name — the operator overrides ride the same InstallOptions.
func TestSkillGitInstallURLOverrides(t *testing.T) {
	dir := t.TempDir()
	commit := strings.Repeat("cd", 20)
	stub := &skillGitServer{store: filepath.Join(dir, "skills"), commit: commit,
		tar: skillGitTarball(t, map[string]string{"skills/pdf/SKILL.md": gitSkillMD})}
	stub.start(t)
	out := captureOutput(t, func() error {
		return run(context.Background(), []string{"skill", "install", "--dir", dir, "--user", "owner",
			"--url", "https://github.com/o/r", "--ref", "v2", "--path", "skills/pdf", "--name", "my-pdf"})
	})
	var result struct {
		DraftID string `json:"draft_id"`
		Name    string `json:"name"`
	}
	if err := json.Unmarshal([]byte(out), &result); err != nil || result.DraftID == "" || result.Name != "my-pdf" {
		t.Fatalf("install output: %v %s", err, out)
	}
	var sawRefFetch bool
	for _, rq := range stub.reqs {
		joined := strings.Join(rq.Args, " ")
		if strings.Contains(joined, "ls-remote") && strings.Contains(joined, " v2") {
			sawRefFetch = true
		}
	}
	if !sawRefFetch {
		t.Fatal("no ls-remote for the pinned ref")
	}
}

// R10.6: the operator update refetches the tracked ref; a moved head lands
// as a draft with prev_commit and the force-push verdict.
func TestSkillGitUpdateName(t *testing.T) {
	dir := t.TempDir()
	store := filepath.Join(dir, "skills")
	prev := strings.Repeat("cd", 20)
	commit := strings.Repeat("ef", 20)
	repo := "https://github.com/o/r"
	sourceID := gitsource.SourceID(repo)
	if err := os.MkdirAll(filepath.Join(store, "pdf"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(store, "pdf", "SKILL.md"), []byte(gitSkillMD), 0o644); err != nil {
		t.Fatal(err)
	}
	origin := `{"source_id":"` + sourceID + `","url":"` + repo + `","ref":"main","commit":"` + prev + `","path":"skills/pdf"}`
	if err := os.WriteFile(filepath.Join(store, "pdf", ".origin.json"), []byte(origin), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(store, ".sources", sourceID+".git"), 0o755); err != nil {
		t.Fatal(err)
	}
	stub := &skillGitServer{store: store, commit: commit,
		tar:   skillGitTarball(t, map[string]string{"skills/pdf/SKILL.md": gitSkillMD + "v2\n"}),
		codes: map[string]int{"merge-base": 1}}
	stub.start(t)
	out := captureOutput(t, func() error {
		return run(context.Background(), []string{"skill", "update", "--dir", dir, "--user", "owner", "--name", "pdf"})
	})
	var result struct {
		DraftID    string `json:"draft_id"`
		Name       string `json:"name"`
		PrevCommit string `json:"prev_commit"`
		ForcePush  bool   `json:"force_push"`
	}
	if err := json.Unmarshal([]byte(out), &result); err != nil || result.DraftID == "" || result.Name != "pdf" || result.PrevCommit != prev || !result.ForcePush {
		t.Fatalf("update output: %v %s", err, out)
	}
}

// --all sweeps every source-tracked skill; untracked ones are skipped and
// the per-skill results land in one JSON list.
func TestSkillGitUpdateAll(t *testing.T) {
	dir := t.TempDir()
	store := filepath.Join(dir, "skills")
	prev := strings.Repeat("cd", 20)
	commit := strings.Repeat("ef", 20)
	repo := "https://github.com/o/r"
	sourceID := gitsource.SourceID(repo)
	for _, name := range []string{"pdf", "xlsx"} {
		if err := os.MkdirAll(filepath.Join(store, name), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(store, name, "SKILL.md"), []byte(gitSkillMD), 0o644); err != nil {
			t.Fatal(err)
		}
		origin := `{"source_id":"` + sourceID + `","url":"` + repo + `","ref":"main","commit":"` + prev + `","path":"skills/` + name + `"}`
		if err := os.WriteFile(filepath.Join(store, name, ".origin.json"), []byte(origin), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	// An untracked skill has no .origin.json — it must not break the sweep.
	if err := os.MkdirAll(filepath.Join(store, "local"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(store, "local", "SKILL.md"), []byte(gitSkillMD), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(store, ".sources", sourceID+".git"), 0o755); err != nil {
		t.Fatal(err)
	}
	stub := &skillGitServer{store: store, commit: commit,
		tar: skillGitTarball(t, map[string]string{
			"skills/pdf/SKILL.md":  gitSkillMD + "v2\n",
			"skills/xlsx/SKILL.md": gitSkillMD + "v2\n",
		})}
	stub.start(t)
	out := captureOutput(t, func() error {
		return run(context.Background(), []string{"skill", "update", "--dir", dir, "--user", "owner", "--all"})
	})
	var results []struct {
		Name    string `json:"name"`
		DraftID string `json:"draft_id"`
	}
	if err := json.Unmarshal([]byte(out), &results); err != nil || len(results) != 2 {
		t.Fatalf("update --all output: %v %s", err, out)
	}
	if results[0].DraftID == "" || results[1].DraftID == "" {
		t.Fatalf("missing drafts: %s", out)
	}
}

// sources list reports each tracked mirror with the skills it feeds —
// pure store inventory, no cell traffic.
func TestSkillGitSourcesList(t *testing.T) {
	dir := t.TempDir()
	store := filepath.Join(dir, "skills")
	repo := "https://github.com/o/r"
	sourceID := gitsource.SourceID(repo)
	if err := os.MkdirAll(filepath.Join(store, ".sources", sourceID+".git"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(store, "pdf"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(store, "pdf", "SKILL.md"), []byte(gitSkillMD), 0o644); err != nil {
		t.Fatal(err)
	}
	origin := `{"source_id":"` + sourceID + `","url":"` + repo + `","commit":"` + strings.Repeat("ab", 20) + `","path":"skills/pdf"}`
	if err := os.WriteFile(filepath.Join(store, "pdf", ".origin.json"), []byte(origin), 0o644); err != nil {
		t.Fatal(err)
	}
	out := captureOutput(t, func() error {
		return run(context.Background(), []string{"skill", "sources", "list", "--dir", dir, "--user", "owner"})
	})
	var sources []struct {
		ID     string   `json:"id"`
		URL    string   `json:"url"`
		Skills []string `json:"skills"`
	}
	if err := json.Unmarshal([]byte(out), &sources); err != nil || len(sources) != 1 || sources[0].ID != sourceID || sources[0].URL != repo || len(sources[0].Skills) != 1 || sources[0].Skills[0] != "pdf" {
		t.Fatalf("sources list output: %v %s", err, out)
	}
}

// sources remove deletes the mirror; the response names the skills that
// lose update tracking (R10.7) and the directory is gone from the store.
func TestSkillGitSourcesRemove(t *testing.T) {
	dir := t.TempDir()
	store := filepath.Join(dir, "skills")
	repo := "https://github.com/o/r"
	sourceID := gitsource.SourceID(repo)
	mirror := filepath.Join(store, ".sources", sourceID+".git")
	if err := os.MkdirAll(mirror, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(store, "pdf"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(store, "pdf", "SKILL.md"), []byte(gitSkillMD), 0o644); err != nil {
		t.Fatal(err)
	}
	origin := `{"source_id":"` + sourceID + `","url":"` + repo + `","commit":"` + strings.Repeat("ab", 20) + `","path":"skills/pdf"}`
	if err := os.WriteFile(filepath.Join(store, "pdf", ".origin.json"), []byte(origin), 0o644); err != nil {
		t.Fatal(err)
	}
	out := captureOutput(t, func() error {
		return run(context.Background(), []string{"skill", "sources", "remove", "--dir", dir, "--user", "owner", "--id", sourceID})
	})
	var result struct {
		Removed bool     `json:"removed"`
		Skills  []string `json:"skills"`
	}
	if err := json.Unmarshal([]byte(out), &result); err != nil || !result.Removed || len(result.Skills) != 1 || result.Skills[0] != "pdf" {
		t.Fatalf("sources remove output: %v %s", err, out)
	}
	if _, err := os.Stat(mirror); !os.IsNotExist(err) {
		t.Fatal("mirror still on disk")
	}
}

// Without the container env pair the operator path falls back to the
// rendered toolhub.auth token and the published loopback endpoint.
func TestSkillGitCellToolhubAuthFallback(t *testing.T) {
	dir := t.TempDir()
	commit := strings.Repeat("ab", 20)
	stub := &skillGitServer{store: filepath.Join(dir, "skills"), commit: commit,
		tar: skillGitTarball(t, map[string]string{"skills/pdf/SKILL.md": gitSkillMD})}
	srv := httptest.NewServer(stub)
	defer srv.Close()
	t.Setenv("HUB_TOOLHIVE_ADMISSION_ENDPOINT", "")
	t.Setenv("HUB_TOOLHIVE_ADMISSION_TOKEN", "")
	if err := os.WriteFile(filepath.Join(dir, "toolhub.auth"), []byte("HUB_TOOLHIVE_ADMISSION_TOKEN=secret\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	old := skillGitAdmissionEndpoint
	skillGitAdmissionEndpoint = srv.URL + "/admit"
	defer func() { skillGitAdmissionEndpoint = old }()
	out := captureOutput(t, func() error {
		return run(context.Background(), []string{"skill", "install", "--dir", dir, "--user", "owner",
			"--url", "https://github.com/o/r/tree/main/skills/pdf"})
	})
	var result struct {
		DraftID string `json:"draft_id"`
	}
	if err := json.Unmarshal([]byte(out), &result); err != nil || result.DraftID == "" {
		t.Fatalf("fallback install output: %v %s", err, out)
	}
	if len(stub.reqs) == 0 {
		t.Fatal("no cell traffic")
	}
}

func TestSkillGitUpdateRequiresTarget(t *testing.T) {
	dir := t.TempDir()
	err := run(context.Background(), []string{"skill", "update", "--dir", dir, "--user", "owner"})
	if err == nil || !strings.Contains(err.Error(), "--name or --all") {
		t.Fatalf("update without target: %v", err)
	}
}

// A private repo authenticates through the operator's GIT_TOKEN env — it
// reaches the cell only as env + credential helper, never argv.
func TestSkillGitCellTokenFromEnv(t *testing.T) {
	dir := t.TempDir()
	commit := strings.Repeat("ab", 20)
	stub := &skillGitServer{store: filepath.Join(dir, "skills"), commit: commit,
		tar: skillGitTarball(t, map[string]string{"skills/pdf/SKILL.md": gitSkillMD})}
	stub.start(t)
	t.Setenv("GIT_TOKEN", "tok-secret")
	out := captureOutput(t, func() error {
		return run(context.Background(), []string{"skill", "install", "--dir", dir, "--user", "owner",
			"--url", "https://github.com/o/private/tree/main/skills/pdf"})
	})
	var result struct {
		DraftID string `json:"draft_id"`
	}
	if err := json.Unmarshal([]byte(out), &result); err != nil || result.DraftID == "" {
		t.Fatalf("private install output: %v %s", err, out)
	}
	var sawToken bool
	for _, rq := range stub.reqs {
		for _, e := range rq.Env {
			if e == "GIT_TOKEN=tok-secret" {
				sawToken = true
			}
			if strings.Contains(e, "tok-secret") && !strings.HasPrefix(e, "GIT_TOKEN=") {
				t.Fatalf("token leaked outside GIT_TOKEN env: %v", rq.Env)
			}
		}
		for _, a := range rq.Args {
			if strings.Contains(a, "tok-secret") {
				t.Fatalf("token in argv: %v", rq.Args)
			}
		}
	}
	if !sawToken {
		t.Fatal("GIT_TOKEN never reached the cell env")
	}
}

// The shared workload-controller authenticates only the infra-owner's key —
// a secondary space's own toolhub.auth token is rejected (401). The
// operator path resolves the infra sibling's file the same way the shared
// governance document does.
func TestSkillGitTokenInfraSibling(t *testing.T) {
	root := t.TempDir()
	spaces := filepath.Join(root, "spaces")
	local := filepath.Join(spaces, "local")
	member := filepath.Join(spaces, "member")
	for _, d := range []string{local, member} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(filepath.Join(local, "agent.yaml"), []byte("user: me\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(member, "agent.yaml"), []byte("infra: false\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(local, "toolhub.auth"), []byte("HUB_TOOLHIVE_ADMISSION_TOKEN=infra-tok\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(member, "toolhub.auth"), []byte("HUB_TOOLHIVE_ADMISSION_TOKEN=member-tok\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	tok, err := skillGitToken(member)
	if err != nil || tok != "infra-tok" {
		t.Fatalf("secondary space must present the infra token, got %q err=%v", tok, err)
	}
	tok, err = skillGitToken(local)
	if err != nil || tok != "infra-tok" {
		t.Fatalf("infra space presents its own token, got %q err=%v", tok, err)
	}
}
