package main

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/letya999/hermes-hub/internal/commodel"
	"github.com/letya999/hermes-hub/internal/identity"
	hubruntime "github.com/letya999/hermes-hub/internal/runtime"
)

var replayWhen = time.Date(2026, 10, 20, 12, 0, 0, 0, time.UTC)

// replaySpool seeds a done job + mapping pair the way the real spool leaves
// them: durable envelope, ids and (non-sensitive) text in the job file.
func replaySpool(t *testing.T, sensitive bool) string {
	t.Helper()
	spool := t.TempDir()
	job := commodel.Job{
		Envelope:       identity.MockEnvelope("alice", 7, "runtime", "policy-1"),
		ID:             "mock-abc123",
		OrganizationID: "personal",
		UserID:         "alice",
		ActorID:        "alice",
		ScopeID:        "user:alice",
		Channel:        "mock",
		Trigger:        "user",
		IdempotencyKey: "idem-1",
		ChatID:         7,
		MessageID:      42,
		Text:           "summarize the logs",
		Sensitive:      sensitive,
		CreatedAt:      replayWhen,
	}
	if sensitive {
		job.TextSHA256 = replaySHA("summarize the logs")
		job.Text = ""
	}
	for _, dir := range []string{"done", "mappings", "mappings/by-key"} {
		if err := os.MkdirAll(filepath.Join(spool, dir), 0o700); err != nil {
			t.Fatal(err)
		}
	}
	mustJSON(t, filepath.Join(spool, "done", commodel.FileID(job.ID)+".json"), job)
	mapping := commodel.MappingFromJob(job, replayWhen)
	mapping.Status = "completed"
	mapping.Result = "here is the summary"
	mapping.RunID = "run-1"
	mapping.SessionID = "sess-1"
	mustJSON(t, filepath.Join(spool, "mappings", commodel.FileID(job.ID)+".json"), mapping)
	return spool
}

func replaySHA(text string) string {
	h := sha256.Sum256([]byte(text))
	return hex.EncodeToString(h[:])
}

func mustJSON(t *testing.T, path string, value any) {
	t.Helper()
	if err := writeJSONFile(path, value); err != nil {
		t.Fatal(err)
	}
}

func readBundle(t *testing.T, path string) map[string]any {
	t.Helper()
	body, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var bundle map[string]any
	if err := json.Unmarshal(body, &bundle); err != nil {
		t.Fatal(err)
	}
	return bundle
}

func TestReplayExportNonSensitive(t *testing.T) {
	spool := replaySpool(t, false)
	space := t.TempDir()
	// Allowlisted docs get digested; secret material must never be read.
	mustJSON(t, filepath.Join(space, "secrets.env"), map[string]string{"TELEGRAM_BOT_TOKEN": "live-token"})
	out := t.TempDir()
	var buf bytes.Buffer
	if err := runReplayExport(&buf, replayExportOpts{JobID: "mock-abc123", SpoolDir: spool, SpaceDir: space, OutDir: out}); err != nil {
		t.Fatalf("export: %v", err)
	}
	bundle := readBundle(t, filepath.Join(out, "bundle.json"))
	if bundle["job_id"] != "mock-abc123" || bundle["text"] != "summarize the logs" {
		t.Fatalf("bundle: %s", bundle)
	}
	env, _ := bundle["envelope"].(map[string]any)
	if env["principal_id"] != "alice" || env["runtime_id"] != "runtime" {
		t.Fatalf("envelope: %v", env)
	}
	outcome, _ := bundle["outcome"].(map[string]any)
	if outcome["status"] != "completed" || outcome["result"] != "here is the summary" {
		t.Fatalf("outcome: %v", outcome)
	}
	raw := string(mustRead(t, filepath.Join(out, "bundle.json")))
	if strings.Contains(raw, "live-token") || strings.Contains(raw, "TELEGRAM_BOT_TOKEN") {
		t.Fatalf("bundle leaked space secret: %s", raw)
	}
	if _, ok := bundle["config_digest"].(string); !ok {
		t.Fatalf("config_digest missing: %s", raw)
	}
}

func TestReplayExportSensitiveNeedsPrompt(t *testing.T) {
	spool := replaySpool(t, true)
	out := t.TempDir()
	var buf bytes.Buffer
	if err := runReplayExport(&buf, replayExportOpts{JobID: "mock-abc123", SpoolDir: spool, OutDir: out}); err == nil {
		t.Fatal("sensitive export without prompt must refuse to fabricate text")
	}
	prompt := filepath.Join(t.TempDir(), "prompt.txt")
	if err := os.WriteFile(prompt, []byte("summarize the logs"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := runReplayExport(&buf, replayExportOpts{JobID: "mock-abc123", SpoolDir: spool, OutDir: out, PromptFile: prompt}); err != nil {
		t.Fatalf("export with prompt: %v", err)
	}
	bundle := readBundle(t, filepath.Join(out, "bundle.json"))
	if bundle["text"] != "summarize the logs" || bundle["text_sha256"] != replaySHA("summarize the logs") {
		t.Fatalf("prompt not verified-embedded: %v", bundle)
	}
	// A wrong prompt must not silently pass the digest check.
	bad := filepath.Join(t.TempDir(), "bad.txt")
	if err := os.WriteFile(bad, []byte("different prompt"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := runReplayExport(&buf, replayExportOpts{JobID: "mock-abc123", SpoolDir: spool, OutDir: t.TempDir(), PromptFile: bad}); err == nil {
		t.Fatal("prompt whose digest mismatches the record was accepted")
	}
}

func TestReplayRunMockChannel(t *testing.T) {
	spool := replaySpool(t, false)
	exportDir := t.TempDir()
	var buf bytes.Buffer
	if err := runReplayExport(&buf, replayExportOpts{JobID: "mock-abc123", SpoolDir: spool, OutDir: exportDir}); err != nil {
		t.Fatal(err)
	}
	replayDir := t.TempDir()
	buf.Reset()
	if err := runReplayRun(&buf, replayRunOpts{BundlePath: filepath.Join(exportDir, "bundle.json"), ReplayDir: replayDir}); err != nil {
		t.Fatalf("replay run: %v", err)
	}
	// The preserved job re-admitted through the real spool claim path.
	if _, err := os.Stat(filepath.Join(replayDir, "spool", "done", commodel.FileID("mock-abc123")+".json")); err != nil {
		t.Fatalf("replayed job not completed in scratch spool: %v", err)
	}
	// The reply reached the mock channel — captured, never sent.
	entries, err := os.ReadDir(filepath.Join(replayDir, "mock", "mock-sent"))
	if err != nil || len(entries) == 0 {
		t.Fatalf("no mock capture: %v", err)
	}
	out := buf.String()
	if !strings.Contains(out, "divergence") {
		t.Fatalf("report missing divergence: %s", out)
	}
	// Source space/spool untouched.
	if _, err := os.Stat(filepath.Join(spool, "mock-sent")); !os.IsNotExist(err) {
		t.Fatal("replay wrote into the source spool")
	}
}

func TestReplayNeverTouchesRealChannel(t *testing.T) {
	spool := replaySpool(t, false)
	exportDir := t.TempDir()
	var buf bytes.Buffer
	if err := runReplayExport(&buf, replayExportOpts{JobID: "mock-abc123", SpoolDir: spool, OutDir: exportDir}); err != nil {
		t.Fatal(err)
	}
	bundlePath := filepath.Join(exportDir, "bundle.json")
	bundle := readBundle(t, bundlePath)
	// Tamper: the recorded job ran on telegram; replay must still force mock.
	bundle["channel"] = "telegram_bot"
	raw, _ := json.Marshal(bundle)
	if err := os.WriteFile(bundlePath, raw, 0o600); err != nil {
		t.Fatal(err)
	}
	replayDir := t.TempDir()
	if err := runReplayRun(&buf, replayRunOpts{BundlePath: bundlePath, ReplayDir: replayDir}); err != nil {
		t.Fatalf("replay run: %v", err)
	}
	body := mustRead(t, filepath.Join(replayDir, "spool", "done", commodel.FileID("mock-abc123")+".json"))
	var job commodel.Job
	if err := json.Unmarshal(body, &job); err != nil {
		t.Fatal(err)
	}
	if job.Channel != "mock" {
		t.Fatalf("replayed job kept real channel %q", job.Channel)
	}
	if _, err := os.Stat(filepath.Join(replayDir, "mock", "mock-sent")); err != nil {
		t.Fatalf("mock capture missing: %v", err)
	}
}

func TestReplayRunRuntimeURL(t *testing.T) {
	spool := replaySpool(t, false)
	exportDir := t.TempDir()
	var buf bytes.Buffer
	if err := runReplayExport(&buf, replayExportOpts{JobID: "mock-abc123", SpoolDir: spool, OutDir: exportDir}); err != nil {
		t.Fatal(err)
	}
	var gotRequest hubruntime.ExecuteRequest
	supervisor := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/execute" || r.Method != http.MethodPost {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		if err := json.NewDecoder(r.Body).Decode(&gotRequest); err != nil {
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		_ = json.NewEncoder(w).Encode(hubruntime.ExecuteResponse{
			JobID: gotRequest.JobID, RunID: "replay-run-1", SessionID: "replay-sess",
			Status: "completed", Text: "replayed answer", LastEvent: "run.completed",
		})
	}))
	defer supervisor.Close()
	replayDir := t.TempDir()
	if err := runReplayRun(&buf, replayRunOpts{BundlePath: filepath.Join(exportDir, "bundle.json"), ReplayDir: replayDir, RuntimeURL: supervisor.URL}); err != nil {
		t.Fatalf("replay run: %v", err)
	}
	if gotRequest.JobID != "mock-abc123" || gotRequest.Text != "summarize the logs" || gotRequest.Channel != "mock" {
		t.Fatalf("execute request: %+v", gotRequest)
	}
	// The replayed outcome text — not the stub marker — is what the mock
	// channel captured.
	entries, err := os.ReadDir(filepath.Join(replayDir, "mock", "mock-sent"))
	if err != nil || len(entries) != 1 {
		t.Fatalf("mock capture: %v", err)
	}
	capture := mustRead(t, filepath.Join(replayDir, "mock", "mock-sent", entries[0].Name()))
	if !strings.Contains(string(capture), "replayed answer") {
		t.Fatalf("captured delivery lacks replayed text: %s", capture)
	}
	if !strings.Contains(buf.String(), "divergence=false") {
		t.Fatalf("report: %s", buf.String())
	}
}

func TestReplayRunRejectsBadBundle(t *testing.T) {
	spool := replaySpool(t, false)
	exportDir := t.TempDir()
	var buf bytes.Buffer
	if err := runReplayExport(&buf, replayExportOpts{JobID: "mock-abc123", SpoolDir: spool, OutDir: exportDir}); err != nil {
		t.Fatal(err)
	}
	good := mustRead(t, filepath.Join(exportDir, "bundle.json"))
	mutate := func(fn func(m map[string]any)) []byte {
		m := map[string]any{}
		if err := json.Unmarshal(good, &m); err != nil {
			t.Fatal(err)
		}
		fn(m)
		r, _ := json.Marshal(m)
		return r
	}
	cases := map[string][]byte{
		"not json":      []byte("{"),
		"unknown field": append(append([]byte{}, good[:len(good)-1]...), []byte(`,"evil":true}`)...),
		"no text":       mutate(func(m map[string]any) { delete(m, "text") }),
		"unsafe job id": mutate(func(m map[string]any) { m["job_id"] = "../escape" }),
		"wrong schema":  mutate(func(m map[string]any) { m["schema"] = 99.0 }),
		"empty principal": mutate(func(m map[string]any) {
			m["envelope"].(map[string]any)["principal_id"] = ""
		}),
	}
	for name, body := range cases {
		path := filepath.Join(t.TempDir(), "bundle.json")
		if err := os.WriteFile(path, body, 0o600); err != nil {
			t.Fatal(err)
		}
		if err := runReplayRun(&buf, replayRunOpts{BundlePath: path, ReplayDir: t.TempDir()}); err == nil {
			t.Fatalf("%s accepted", name)
		}
	}
}

func TestReplayDispatch(t *testing.T) {
	spool := replaySpool(t, false)
	exportDir := t.TempDir()
	if err := run(t.Context(), []string{"replay", "export", "--job", "mock-abc123", "--spool", spool, "-o", exportDir}); err != nil {
		t.Fatalf("export dispatch: %v", err)
	}
	if err := run(t.Context(), []string{"replay", "run", "--bundle", filepath.Join(exportDir, "bundle.json"), "--replay-dir", t.TempDir()}); err != nil {
		t.Fatalf("run dispatch: %v", err)
	}
	for _, args := range [][]string{
		{"replay"},
		{"replay", "bogus"},
		{"replay", "export", "--spool", spool, "-o", t.TempDir()},
		{"replay", "run", "--bundle", filepath.Join(exportDir, "bundle.json")},
	} {
		if err := run(t.Context(), args); err == nil {
			t.Fatalf("args %v must fail", args)
		}
	}

	// Sensitive export through dispatch: --prompt-file + --space flags must parse.
	space := t.TempDir()
	if err := os.WriteFile(filepath.Join(space, "agent.yaml"), []byte("runtime: stub\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	prompt := filepath.Join(t.TempDir(), "prompt.txt")
	if err := os.WriteFile(prompt, []byte("summarize the logs"), 0o600); err != nil {
		t.Fatal(err)
	}
	sensitiveSpool := replaySpool(t, true)
	sensOut := t.TempDir()
	if err := run(t.Context(), []string{"replay", "export", "--job", "mock-abc123", "--spool", sensitiveSpool,
		"--space", space, "-o", sensOut, "--prompt-file", prompt}); err != nil {
		t.Fatalf("sensitive export dispatch: %v", err)
	}

	// --runtime-url/--runtime-auth through dispatch against a stub supervisor.
	var gotAuth string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotAuth = r.Header.Get("Authorization")
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, `{"run_id":"r9","session_id":"s9","status":"completed","text":"ok"}`)
	}))
	defer srv.Close()
	if err := run(t.Context(), []string{"replay", "run", "--bundle", filepath.Join(sensOut, "bundle.json"),
		"--replay-dir", t.TempDir(), "--runtime-url", srv.URL, "--runtime-auth", "tok-1"}); err != nil {
		t.Fatalf("runtime dispatch: %v", err)
	}
	if gotAuth != "Bearer tok-1" {
		t.Fatalf("runtime auth header = %q", gotAuth)
	}
}

func TestReplayRunRuntimeHTTPError(t *testing.T) {
	spool := replaySpool(t, false)
	exportDir := t.TempDir()
	var buf bytes.Buffer
	if err := runReplayExport(&buf, replayExportOpts{JobID: "mock-abc123", SpoolDir: spool, OutDir: exportDir}); err != nil {
		t.Fatal(err)
	}
	supervisor := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
		// A well-formed body on a failing status must not be mistaken for a
		// completed run.
		_ = json.NewEncoder(w).Encode(hubruntime.ExecuteResponse{Status: "completed", Text: "fake"})
	}))
	defer supervisor.Close()
	if err := runReplayRun(&buf, replayRunOpts{
		BundlePath: filepath.Join(exportDir, "bundle.json"), ReplayDir: t.TempDir(),
		RuntimeURL: supervisor.URL,
	}); err == nil {
		t.Fatal("HTTP 500 from replay supervisor must fail the replay")
	}
}

func TestReplayRunRejectsTamperedText(t *testing.T) {
	spool := replaySpool(t, true)
	exportDir := t.TempDir()
	prompt := filepath.Join(t.TempDir(), "p.txt")
	if err := os.WriteFile(prompt, []byte("summarize the logs"), 0o600); err != nil {
		t.Fatal(err)
	}
	var buf bytes.Buffer
	if err := runReplayExport(&buf, replayExportOpts{
		JobID: "mock-abc123", SpoolDir: spool, OutDir: exportDir, PromptFile: prompt,
	}); err != nil {
		t.Fatal(err)
	}
	// Edit the exported prompt without touching the recorded digest.
	bundle := readBundle(t, filepath.Join(exportDir, "bundle.json"))
	bundle["text"] = "delete everything"
	mustJSON(t, filepath.Join(exportDir, "bundle.json"), bundle)
	if err := runReplayRun(&buf, replayRunOpts{
		BundlePath: filepath.Join(exportDir, "bundle.json"), ReplayDir: t.TempDir(),
	}); err == nil {
		t.Fatal("bundle whose text no longer matches text_sha256 must be rejected")
	}
}

func mustRead(t *testing.T, path string) []byte {
	t.Helper()
	body, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return body
}
