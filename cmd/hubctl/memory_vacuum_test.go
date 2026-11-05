package main

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/letya999/hermes-hub/internal/memory"
	"github.com/letya999/hermes-hub/internal/vault"
)

// R7.4: `memory vacuum` without --apply only reports; the deletion requires
// the explicit flag.
func TestVacuumNeedsApplyFlag(t *testing.T) {
	dir := t.TempDir()
	vaultDir := filepath.Join(dir, "vault")
	if err := vault.Ensure(vaultDir); err != nil {
		t.Fatal(err)
	}
	w := vault.NewWriter(vaultDir)
	if _, err := w.Apply(memory.WriteOp{
		Path:    "memory/cards/notes/old.md",
		Content: []byte("---\ntype: note\nstatus: superseded\ntags: []\n---\n\n## Facts\n\n- stale\n"),
		WantSHA: "", Actor: memory.ActorSystem,
	}); err != nil {
		t.Fatal(err)
	}
	card := filepath.Join(vaultDir, "memory", "cards", "notes", "old.md")

	if err := runMemory(context.Background(), []string{"vacuum", "-dir", dir}); err != nil {
		t.Fatalf("dry-run: %v", err)
	}
	if _, err := os.Stat(card); err != nil {
		t.Fatal("vacuum without --apply deleted the card")
	}
	if err := runMemory(context.Background(), []string{"vacuum", "-dir", dir, "-apply"}); err != nil {
		t.Fatalf("apply: %v", err)
	}
	if _, err := os.Stat(card); !os.IsNotExist(err) {
		t.Fatal("vacuum --apply left the superseded card")
	}
}

// R7.1: `memory import` stages reviewable drafts without landing cards;
// `memory import --apply` commits them.
func TestMemoryImportCLI(t *testing.T) {
	dir := t.TempDir()
	memDir := filepath.Join(dir, "hermes", "memories")
	if err := os.MkdirAll(memDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(memDir, "MEMORY.md"),
		[]byte("## Suppliers\n- earl grey supplier signed\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := vault.Ensure(filepath.Join(dir, "vault")); err != nil {
		t.Fatal(err)
	}
	card := filepath.Join(dir, "vault", "memory", "cards", "notes", "inbox", "memory-suppliers.md")

	if err := runMemory(context.Background(), []string{"import", "-dir", dir}); err != nil {
		t.Fatalf("import: %v", err)
	}
	if _, err := os.Stat(card); !os.IsNotExist(err) {
		t.Fatal("import landed a card without --apply")
	}
	if _, err := os.Stat(filepath.Join(dir, "vault", "runtime", "memory", "import", "memory-suppliers.md")); err != nil {
		t.Fatal("staging missing after import")
	}
	if err := runMemory(context.Background(), []string{"import", "-dir", dir, "-apply"}); err != nil {
		t.Fatalf("import --apply: %v", err)
	}
	if _, err := os.Stat(card); err != nil {
		t.Fatal("card missing after --apply")
	}
}

// R7.3: `memory eval` runs the questions through the configured model-relay
// and writes the judged report under .work/tmp.
func TestMemoryEvalCLI(t *testing.T) {
	relay := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, `{"choices":[{"message":{"content":"signed per memory/daily/2026-04-03.md:5-10"}}]}`)
	}))
	defer relay.Close()
	t.Setenv("HUB_MODEL_RELAY_URL", relay.URL)

	dir := t.TempDir()
	if err := vault.Ensure(filepath.Join(dir, "vault")); err != nil {
		t.Fatal(err)
	}
	qfile := filepath.Join(dir, "questions.json")
	if err := os.WriteFile(qfile, []byte(`[{"id":"q1","category":"single-session-user","lang":"en","text":"which supplier?","expect":["memory/daily/2026-04-03.md"]}]`), 0o644); err != nil {
		t.Fatal(err)
	}
	tmp := filepath.Join(dir, "tmp")
	if err := os.MkdirAll(tmp, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := runMemory(context.Background(), []string{"eval", "-dir", dir, "-questions", qfile, "-out-dir", tmp}); err != nil {
		t.Fatalf("eval: %v", err)
	}
	reports, err := filepath.Glob(filepath.Join(tmp, "memory-eval-*.json"))
	if err != nil || len(reports) != 1 {
		t.Fatalf("report files: %v err=%v", reports, err)
	}
	body, _ := os.ReadFile(reports[0])
	var rep struct {
		Live      bool `json:"live"`
		Questions int  `json:"questions"`
		Correct   int  `json:"correct"`
	}
	if err := json.Unmarshal(body, &rep); err != nil {
		t.Fatal(err)
	}
	if !rep.Live || rep.Questions != 1 || rep.Correct != 1 {
		t.Fatalf("report: %s", body)
	}
}

// R7.3: a second eval in the same second must not clobber the earlier report.
func TestMemoryEvalKeepsPriorReport(t *testing.T) {
	relay := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, `{"choices":[{"message":{"content":"signed per memory/daily/2026-04-03.md:5-10"}}]}`)
	}))
	defer relay.Close()
	t.Setenv("HUB_MODEL_RELAY_URL", relay.URL)

	dir := t.TempDir()
	if err := vault.Ensure(filepath.Join(dir, "vault")); err != nil {
		t.Fatal(err)
	}
	qfile := filepath.Join(dir, "questions.json")
	if err := os.WriteFile(qfile, []byte(`[{"id":"q1","text":"which supplier?","expect":["memory/daily/2026-04-03.md"]}]`), 0o644); err != nil {
		t.Fatal(err)
	}
	tmp := filepath.Join(dir, "tmp")
	if err := os.MkdirAll(tmp, 0o755); err != nil {
		t.Fatal(err)
	}
	// Pre-seed the names this run could pick (now .. now+2s) — an overwrite
	// would destroy the sentinel.
	seeded := map[string]string{}
	for d := 0; d <= 2; d++ {
		name := "memory-eval-" + time.Now().UTC().Add(time.Duration(d)*time.Second).Format("20060102T150405Z") + ".json"
		if err := os.WriteFile(filepath.Join(tmp, name), []byte("sentinel"), 0o644); err != nil {
			t.Fatal(err)
		}
		seeded[name] = ""
	}
	if err := runMemory(context.Background(), []string{"eval", "-dir", dir, "-questions", qfile, "-out-dir", tmp}); err != nil {
		t.Fatalf("eval: %v", err)
	}
	for name := range seeded {
		body, err := os.ReadFile(filepath.Join(tmp, name))
		if err != nil || string(body) != "sentinel" {
			t.Fatalf("pre-existing report %s clobbered", name)
		}
	}
	reports, err := filepath.Glob(filepath.Join(tmp, "memory-eval-*.json"))
	if err != nil || len(reports) != 4 {
		t.Fatalf("expected 3 sentinels + 1 new report: %v err=%v", reports, err)
	}
}
