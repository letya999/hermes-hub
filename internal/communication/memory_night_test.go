package communication

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/letya999/hermes-hub/internal/locale"
	"github.com/letya999/hermes-hub/internal/memory"
	"github.com/letya999/hermes-hub/internal/memory/prune"
	"github.com/letya999/hermes-hub/internal/vault"
)

// SPEC-0085 R5.1: the rollup fires once per space-local day after the
// configured rollup_time; a same-day rerun is a no-op.
func TestNightDueGate(t *testing.T) {
	now := time.Date(2026, 4, 20, 5, 0, 0, 0, time.Local)
	if !nightDue(now, "04:00", "2026-04-19") {
		t.Fatal("past rollup_time with stale marker should be due")
	}
	if nightDue(now, "04:00", "2026-04-20") {
		t.Fatal("same-day rerun must not fire")
	}
	early := time.Date(2026, 4, 20, 3, 59, 0, 0, time.Local)
	if nightDue(early, "04:00", "2026-04-19") {
		t.Fatal("before rollup_time must not fire")
	}
	if nightDue(now, "", "2026-04-19") {
		t.Fatal("disabled rollup must not fire")
	}
}

// SPEC-0085 R5.1/R5.6: a due tick runs the gated rollup end to end —
// summary + journal land, the marker pins the day, the digest is spooled
// to the owner's DM, and intents become draft assignments.
func TestNightJobRunsRollup(t *testing.T) {
	c := baseTestConfig(t)
	root := filepath.Join(t.TempDir(), "vault")
	if err := vault.Ensure(root); err != nil {
		t.Fatal(err)
	}
	c.VaultDir = root
	c.MemoryRollupTime = "00:00"
	c.ControlAuth = "control-token"
	g, host, _ := automationGatewayFixture(t, c)
	now := time.Now()
	// The rollup folds the last closed local day — an entry captured
	// yesterday is what the run tonight promotes.
	closed := now.AddDate(0, 0, -1)
	noon := time.Date(closed.Year(), closed.Month(), closed.Day(), 12, 0, 0, 0, time.Local)
	daily, err := vault.OpenDaily(root, time.Local)
	if err != nil {
		t.Fatal(err)
	}
	entry := memory.Entry{ID: "turn-n1", Kind: memory.KindInteractive, Provenance: memory.ProvenanceOwner,
		Audience: memory.AudiencePrivate, Body: "I only drink earl grey tea", ObservedAt: noon}
	if err := daily.Append(entry, "telegram_bot"); err != nil {
		t.Fatal(err)
	}
	g.nightRelay = func(ctx context.Context, system, user string) (string, error) {
		return `{"summary":"tea day","facts":[{"card":"notes/tea","fact":"prefers earl grey","quote":"I only drink earl grey tea"}],"intents":[{"text":"call the bank Friday","quote":"I only drink earl grey tea"}]}`, nil
	}
	g.tickNightRollup(now)
	dayKey := noon.Format("2006-01-02")
	if _, err := os.Stat(filepath.Join(root, "memory", "summaries", "daily", dayKey+".md")); err != nil {
		t.Fatalf("day summary missing: %v", err)
	}
	marker, err := os.ReadFile(filepath.Join(root, "runtime", "memory", "night", "last-run.txt"))
	if err != nil || string(marker) != now.Format("2006-01-02") {
		t.Fatalf("last-run marker wrong: %q %v", marker, err)
	}
	pending, err := os.ReadDir(filepath.Join(g.spool.root, "outbox", "pending"))
	if err != nil || len(pending) == 0 {
		t.Fatalf("digest delivery was not spooled: %v", err)
	}
	if _, err := os.Stat(filepath.Join(root, "memory", "REVIEW.md")); err != nil {
		t.Fatalf("REVIEW.md missing: %v", err)
	}
	// The intent became a draft assignment in the owner's envelope —
	// never a stored fact, never activated (R5.7). ToolHub is the sole
	// assignment writer, so the draft lands on the automation host.
	items, err := host.ListAssignments(context.Background(), g.user("alice").Envelope(11))
	if err != nil || len(items) != 1 || items[0].State != "draft" || items[0].Intent != "call the bank Friday" {
		t.Fatalf("intent did not land as a draft assignment on the ToolHub owner: %+v %v", items, err)
	}
}

// R5.1/R5.8: a failed rollup does not mark the day done — the next tick
// resumes from the cached steps and spends only the remaining budget.
func TestNightErrorLeavesMarkerUnset(t *testing.T) {
	c := baseTestConfig(t)
	root := filepath.Join(t.TempDir(), "vault")
	if err := vault.Ensure(root); err != nil {
		t.Fatal(err)
	}
	c.VaultDir = root
	c.MemoryRollupTime = "00:00"
	g, err := New(c)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now()
	closed := now.AddDate(0, 0, -1)
	noon := time.Date(closed.Year(), closed.Month(), closed.Day(), 12, 0, 0, 0, time.Local)
	daily, err := vault.OpenDaily(root, time.Local)
	if err != nil {
		t.Fatal(err)
	}
	if err := daily.Append(memory.Entry{ID: "turn-n1", Kind: memory.KindInteractive, Provenance: memory.ProvenanceOwner,
		Audience: memory.AudiencePrivate, Body: "I only drink earl grey tea", ObservedAt: noon}, "telegram_bot"); err != nil {
		t.Fatal(err)
	}
	g.nightRelay = func(ctx context.Context, system, user string) (string, error) {
		return "", context.DeadlineExceeded
	}
	g.tickNightRollup(now)
	if _, err := os.Stat(filepath.Join(root, "runtime", "memory", "night", "last-run.txt")); !os.IsNotExist(err) {
		t.Fatal("failed rollup marked the day done")
	}
}

// SPEC-0085 R5.1: with no injected fixture the tick binds the managed
// model-relay seam — POST /v1/chat/completions on HUB_MODEL_RELAY_URL
// with the HUB_MODEL_RELAY_AUTH bearer and the relay-side model
// selection (an explicit empty model field).
func TestNightProductionRelay(t *testing.T) {
	var sawPath, sawAuth string
	var sawModel bool
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		sawPath, sawAuth = r.URL.Path, r.Header.Get("Authorization")
		var body map[string]json.RawMessage
		if json.NewDecoder(r.Body).Decode(&body) == nil {
			_, sawModel = body["model"]
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"choices":[{"message":{"content":"{\"summary\":\"relay ok\",\"facts\":[]}"}}]}`))
	}))
	defer srv.Close()
	t.Setenv("HUB_MODEL_RELAY_URL", srv.URL)
	t.Setenv("HUB_MODEL_RELAY_AUTH", "test-auth")

	c := baseTestConfig(t)
	root := filepath.Join(t.TempDir(), "vault")
	if err := vault.Ensure(root); err != nil {
		t.Fatal(err)
	}
	c.VaultDir = root
	c.MemoryRollupTime = "00:00"
	g, err := New(c)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now()
	closed := now.AddDate(0, 0, -1)
	noon := time.Date(closed.Year(), closed.Month(), closed.Day(), 12, 0, 0, 0, time.Local)
	daily, err := vault.OpenDaily(root, time.Local)
	if err != nil {
		t.Fatal(err)
	}
	if err := daily.Append(memory.Entry{ID: "turn-n1", Kind: memory.KindInteractive, Provenance: memory.ProvenanceOwner,
		Audience: memory.AudiencePrivate, Body: "I only drink earl grey tea", ObservedAt: noon}, "telegram_bot"); err != nil {
		t.Fatal(err)
	}
	g.tickNightRollup(now)
	if sawPath != "/v1/chat/completions" {
		t.Fatalf("relay path = %q", sawPath)
	}
	if sawAuth != "Bearer test-auth" {
		t.Fatalf("relay auth = %q", sawAuth)
	}
	if !sawModel {
		t.Fatal("request body missing the model field")
	}
	if _, err := os.Stat(filepath.Join(root, "memory", "summaries", "daily", noon.Format("2006-01-02")+".md")); err != nil {
		t.Fatalf("production relay run wrote no summary: %v", err)
	}
}

// SPEC-0085 R5.1: the vault mount and the rollup clock reach the gateway
// config through env on BOTH config paths — the env-only bootstrap and
// the YAML branch read the same rendered variables.
func TestConfigFromEnvVaultAndRollup(t *testing.T) {
	t.Setenv("TELEGRAM_BOT_TOKEN", "token")
	t.Setenv("TELEGRAM_ALLOWED_USERS", "11")
	t.Setenv("HUB_VAULT", "/vault")
	t.Setenv("HUB_MEMORY_ROLLUP_TIME", "02:30")
	config, err := ConfigFromEnv()
	if err != nil {
		t.Fatal(err)
	}
	if config.VaultDir != "/vault" {
		t.Fatalf("env branch VaultDir = %q", config.VaultDir)
	}
	if config.MemoryRollupTime != "02:30" {
		t.Fatalf("env branch MemoryRollupTime = %q", config.MemoryRollupTime)
	}

	dir := t.TempDir()
	yamlPath := filepath.Join(dir, "communication.users.yaml")
	body := "organization_id: acme\nspool_dir: " + filepath.ToSlash(filepath.Join(dir, "spool")) +
		"\nusers:\n  - id: me\n    enabled: true\n    telegram_ids: [11]\n    state_dir: " +
		filepath.ToSlash(filepath.Join(dir, "st")) + "\n    workspace_dir: " +
		filepath.ToSlash(filepath.Join(dir, "ws")) + "\n"
	if err := os.WriteFile(yamlPath, []byte(body), 0600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("HUB_COMMUNICATION_CONFIG", yamlPath)
	config, err = ConfigFromEnv()
	if err != nil {
		t.Fatal(err)
	}
	if config.VaultDir != "/vault" {
		t.Fatalf("yaml branch VaultDir = %q", config.VaultDir)
	}
	if config.MemoryRollupTime != "02:30" {
		t.Fatalf("yaml branch MemoryRollupTime = %q", config.MemoryRollupTime)
	}
}

// SPEC-0085 R5.1: the running gateway schedules the rollup — not just an
// exported tick: a due clock plus the feature env produces the last-run
// marker without any test calling the tick by hand.
func TestGatewayRunSchedulesNightRollup(t *testing.T) {
	c := baseTestConfig(t)
	root := filepath.Join(t.TempDir(), "vault")
	if err := vault.Ensure(root); err != nil {
		t.Fatal(err)
	}
	c.VaultDir = root
	c.MemoryRollupTime = "00:00"
	g, err := New(c)
	if err != nil {
		t.Fatal(err)
	}
	g.nightRelay = func(ctx context.Context, system, user string) (string, error) {
		return `{"summary":"scheduled","facts":[]}`, nil
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	api := &runAPI{cancel: cancel}
	g.api = api
	done := make(chan error, 1)
	go func() { done <- g.Run(ctx) }()
	marker := filepath.Join(root, "runtime", "memory", "night", "last-run.txt")
	waitExit := func() {
		select {
		case err := <-done:
			if err != context.Canceled {
				t.Fatalf("Run returned %v", err)
			}
		case <-time.After(10 * time.Second):
			t.Fatal("Run did not stop after cancel")
		}
	}
	deadline := time.Now().Add(10 * time.Second)
	for {
		if b, err := os.ReadFile(marker); err == nil && len(b) > 0 {
			break
		}
		if time.Now().After(deadline) {
			cancel()
			waitExit()
			t.Fatal("running gateway never fired the night rollup")
		}
		time.Sleep(20 * time.Millisecond)
	}
	cancel()
	waitExit()
}

// SPEC-0085 R5.9: a successful night folds the just-closed week — the
// day's own run supplies Sunday's terminal state (no leaf → no-data
// short-circuit), so the weekly summary lands in the same tick with
// exactly one model call: the fold's.
func TestNightTickFoldsClosedWeek(t *testing.T) {
	c := baseTestConfig(t)
	root := filepath.Join(t.TempDir(), "vault")
	if err := vault.Ensure(root); err != nil {
		t.Fatal(err)
	}
	c.VaultDir = root
	c.MemoryRollupTime = "04:00"
	g, err := New(c)
	if err != nil {
		t.Fatal(err)
	}
	// Today is Monday 2026-04-20; the run folds Sunday 2026-04-19 and the
	// week Mon 04-13 … Sun 04-19 closes. Seed Mon–Sat terminal states;
	// Sunday's arrives through the run itself.
	for i := 0; i < 6; i++ {
		d := time.Date(2026, 4, 13+i, 12, 0, 0, 0, time.Local).Format("2006-01-02")
		cache := filepath.Join(root, "runtime", "memory", "night", d+".json")
		if err := os.MkdirAll(filepath.Dir(cache), 0700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(cache, []byte(`{"entries":{},"steps":{"facts":true,"links":true,"truths":true,"intents":true,"core":true,"summary":true,"journal":true}}`), 0600); err != nil {
			t.Fatal(err)
		}
	}
	calls := 0
	g.nightRelay = func(ctx context.Context, system, user string) (string, error) {
		calls++
		return `{"summary":"quiet week","facts":[]}`, nil
	}
	g.tickNightRollup(time.Date(2026, 4, 20, 5, 0, 0, 0, time.Local))
	if _, err := os.Stat(filepath.Join(root, "memory", "summaries", "weekly", "2026-W16.md")); err != nil {
		t.Fatalf("closed week not folded: %v", err)
	}
	if calls != 1 {
		t.Fatalf("expected exactly the fold's model call, got %d", calls)
	}
}

// SPEC-0085 R5.9: on the year's first night the sweep cascades — the
// run closes December 31 (no leaf → no-data state), which completes
// December's day states, whose monthly summary completes the year's
// twelve. One tick writes week, month and year in dependency order.
func TestNightTickFoldsClosedMonthAndYear(t *testing.T) {
	c := baseTestConfig(t)
	root := filepath.Join(t.TempDir(), "vault")
	if err := vault.Ensure(root); err != nil {
		t.Fatal(err)
	}
	c.VaultDir = root
	c.MemoryRollupTime = "04:00"
	g, err := New(c)
	if err != nil {
		t.Fatal(err)
	}
	// Tick on Monday 2029-01-01; the run folds Sunday 2028-12-31. Seed
	// every December day state except the 31st — the run supplies it —
	// and January…November monthly summaries; December's arrives through
	// the sweep itself.
	for d := 1; d <= 30; d++ {
		key := fmt.Sprintf("2028-12-%02d", d)
		cache := filepath.Join(root, "runtime", "memory", "night", key+".json")
		if err := os.MkdirAll(filepath.Dir(cache), 0700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(cache, []byte(`{"entries":{},"steps":{"facts":true,"links":true,"truths":true,"intents":true,"core":true,"summary":true,"journal":true}}`), 0600); err != nil {
			t.Fatal(err)
		}
	}
	for m := 1; m <= 11; m++ {
		abs := filepath.Join(root, "memory", "summaries", "monthly", fmt.Sprintf("2028-%02d.md", m))
		if err := os.MkdirAll(filepath.Dir(abs), 0700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(abs, []byte("month\n"), 0600); err != nil {
			t.Fatal(err)
		}
	}
	calls := 0
	g.nightRelay = func(ctx context.Context, system, user string) (string, error) {
		calls++
		return `{"summary":"folded","facts":[]}`, nil
	}
	g.tickNightRollup(time.Date(2029, 1, 1, 5, 0, 0, 0, time.Local))
	// The just-closed ISO week is Dec 24–30 (2028-W52); December and the
	// whole year close on the same night.
	wy, ww := time.Date(2028, 12, 25, 0, 0, 0, 0, time.Local).ISOWeek()
	for _, rel := range []string{
		fmt.Sprintf("memory/summaries/weekly/%04d-W%02d.md", wy, ww),
		"memory/summaries/monthly/2028-12.md",
		"memory/summaries/yearly/2028.md",
	} {
		if _, err := os.Stat(filepath.Join(root, filepath.FromSlash(rel))); err != nil {
			t.Fatalf("closed period not folded: %s: %v", rel, err)
		}
	}
	if calls != 3 {
		t.Fatalf("expected one model call per fired fold, got %d", calls)
	}
}

// SPEC-0085 R7.5: the leaf prune knobs reach the gateway config through env
// on both bootstrap paths, same lane as the rollup clock.
func TestConfigFromEnvLeafKnobs(t *testing.T) {
	t.Setenv("TELEGRAM_BOT_TOKEN", "token")
	t.Setenv("TELEGRAM_ALLOWED_USERS", "11")
	t.Setenv("HUB_MEMORY_LEAF_RETENTION_DAYS", "7")
	t.Setenv("HUB_MEMORY_LEAF_FOLD_BYTES", "65536")
	config, err := ConfigFromEnv()
	if err != nil {
		t.Fatal(err)
	}
	if config.MemoryLeafRetentionDays != 7 || config.MemoryLeafFoldBytes != 65536 {
		t.Fatalf("leaf knobs = %d/%d, want 7/65536", config.MemoryLeafRetentionDays, config.MemoryLeafFoldBytes)
	}
}

// SPEC-0085 R7.5: after the rollup the same tick prunes — a retention-age
// leaf whose distillation completed is expired through the governed lane.
func TestNightTickPrunes(t *testing.T) {
	c := baseTestConfig(t)
	root := filepath.Join(t.TempDir(), "vault")
	if err := vault.Ensure(root); err != nil {
		t.Fatal(err)
	}
	c.VaultDir = root
	c.MemoryRollupTime = "00:00"
	c.MemoryLeafRetentionDays = 7

	old := time.Now().AddDate(0, 0, -30)
	daily, err := vault.OpenDaily(root, time.Local)
	if err != nil {
		t.Fatal(err)
	}
	if err := daily.Append(memory.Entry{ID: "old-1", Kind: memory.KindInteractive,
		Provenance: memory.ProvenanceOwner, Audience: memory.AudiencePrivate,
		Body: "old day content", ObservedAt: old}, "test"); err != nil {
		t.Fatal(err)
	}
	dayKey := old.Format("2006-01-02")
	cache := filepath.Join(root, "runtime", "memory", "night", dayKey+".json")
	if err := os.MkdirAll(filepath.Dir(cache), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(cache, []byte(`{"entries":{},"steps":{"facts":true,"links":true,"truths":true,"intents":true,"core":true,"summary":true,"journal":true}}`), 0600); err != nil {
		t.Fatal(err)
	}
	w := vault.NewWriter(root)
	if _, err := w.Apply(memory.WriteOp{Path: "memory/summaries/daily/" + dayKey + ".md",
		Content: []byte("---\ntype: daily-summary\nstatus: active\ntags: []\ncreated: " + dayKey + "\nupdated: " + dayKey + "\n---\n\nold digest\n"),
		Actor:   memory.ActorSystem}); err != nil {
		t.Fatal(err)
	}

	g, err := New(c)
	if err != nil {
		t.Fatal(err)
	}
	g.nightRelay = func(ctx context.Context, system, user string) (string, error) {
		return `{"summary":"night","facts":[]}`, nil
	}
	g.tickNightRollup(time.Now())

	if _, err := os.Stat(filepath.Join(root, "memory", "daily", dayKey+".md")); !os.IsNotExist(err) {
		t.Fatal("retention-age leaf still present after night tick")
	}
}

// SPEC-0085 R7.5: the fold knob reaches the tick — an oversized completed
// leaf inside the retention window gets its audit bodies stubbed.
func TestNightTickFolds(t *testing.T) {
	c := baseTestConfig(t)
	root := filepath.Join(t.TempDir(), "vault")
	if err := vault.Ensure(root); err != nil {
		t.Fatal(err)
	}
	c.VaultDir = root
	c.MemoryRollupTime = "00:00"
	c.MemoryLeafFoldBytes = 1

	old := time.Now().AddDate(0, 0, -3)
	daily, err := vault.OpenDaily(root, time.Local)
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range []memory.Entry{
		{ID: "own-1", Kind: memory.KindInteractive, Provenance: memory.ProvenanceOwner,
			Audience: memory.AudiencePrivate, Body: "interactive words stay", ObservedAt: old},
		{ID: "cron-1", Kind: memory.KindRoutine, Provenance: memory.ProvenanceSystem,
			Audience: memory.AudiencePrivate, Body: strings.Repeat("cron chatter ", 30), ObservedAt: old},
	} {
		if err := daily.Append(e, "test"); err != nil {
			t.Fatal(err)
		}
	}
	dayKey := old.Format("2006-01-02")
	cache := filepath.Join(root, "runtime", "memory", "night", dayKey+".json")
	if err := os.MkdirAll(filepath.Dir(cache), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(cache, []byte(`{"entries":{},"steps":{"facts":true,"links":true,"truths":true,"intents":true,"core":true,"summary":true,"journal":true}}`), 0600); err != nil {
		t.Fatal(err)
	}

	g, err := New(c)
	if err != nil {
		t.Fatal(err)
	}
	g.nightRelay = func(ctx context.Context, system, user string) (string, error) {
		return `{"summary":"night","facts":[]}`, nil
	}
	g.tickNightRollup(time.Now())

	b, err := os.ReadFile(filepath.Join(root, "memory", "daily", dayKey+".md"))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(b), "cron chatter") {
		t.Fatal("audit body survived the tick fold")
	}
	if !strings.Contains(string(b), "interactive words stay") {
		t.Fatal("interactive body folded")
	}
}

// SPEC-0085 R7.5: the fold knob also reaches the rollup itself — when
// yesterday's eligible text exceeds it, the proposal asks for long_summary
// so the detailed digest lands before any byte folding.
func TestNightTickBigDayAsksLongSummary(t *testing.T) {
	c := baseTestConfig(t)
	root := filepath.Join(t.TempDir(), "vault")
	if err := vault.Ensure(root); err != nil {
		t.Fatal(err)
	}
	c.VaultDir = root
	c.MemoryRollupTime = "00:00"
	c.MemoryLeafFoldBytes = 64

	yesterday := time.Now().AddDate(0, 0, -1)
	daily, err := vault.OpenDaily(root, time.Local)
	if err != nil {
		t.Fatal(err)
	}
	if err := daily.Append(memory.Entry{ID: "y-1", Kind: memory.KindInteractive,
		Provenance: memory.ProvenanceOwner, Audience: memory.AudiencePrivate,
		Body: strings.Repeat("decisions all day long ", 20), ObservedAt: yesterday}, "test"); err != nil {
		t.Fatal(err)
	}

	g, err := New(c)
	if err != nil {
		t.Fatal(err)
	}
	var prompt string
	g.nightRelay = func(ctx context.Context, system, user string) (string, error) {
		prompt = user
		return `{"summary":"night","facts":[]}`, nil
	}
	g.tickNightRollup(time.Now())
	if !strings.Contains(prompt, "long_summary") {
		t.Fatal("big yesterday did not ask for long_summary")
	}
}

// SPEC-0085 R7.5: prune actions surface in the morning digest as data
// lines under the promoted facts — folded, expired and kept days all
// named, so housekeeping is never silent.
func TestPruneLine(t *testing.T) {
	got := pruneLine("- fact one", prune.Report{
		Folded:  []prune.FoldResult{{Day: "2026-04-10", Before: 4096, After: 512, Stubs: 3}},
		Expired: []string{"memory/daily/2026-03-01.md"},
		Kept:    []prune.KeptDay{{Day: "2026-03-02", Reason: "rollup incomplete"}},
	}, locale.LangEN)
	for _, want := range []string{"- fact one", "folded leaf", "expired leaf 2026-03-01", "kept leaf 2026-03-02"} {
		if !strings.Contains(got, want) {
			t.Fatalf("digest %q missing %q", got, want)
		}
	}
	// Housekeeping lines localize like every owner-visible reply.
	ru := pruneLine("", prune.Report{
		Folded:  []prune.FoldResult{{Day: "2026-04-10", Before: 4096, After: 512, Stubs: 3}},
		Expired: []string{"memory/daily/2026-03-01.md"},
		Kept:    []prune.KeptDay{{Day: "2026-03-02", Reason: "rollup incomplete"}},
	}, locale.LangRU)
	for _, want := range []string{"сжал лист 2026-04-10", "удалил лист 2026-03-01", "оставил лист 2026-03-02"} {
		if !strings.Contains(ru, want) {
			t.Fatalf("ru digest %q missing %q", ru, want)
		}
	}
	// The missing-summary keep names its own reason, not the rollup one.
	miss := pruneLine("", prune.Report{Kept: []prune.KeptDay{{Day: "2026-03-03", Reason: "day summary missing"}}}, locale.LangEN)
	if !strings.Contains(miss, "day summary missing") {
		t.Fatalf("missing-summary keep mislabeled: %q", miss)
	}
}

// SPEC-0085 R7.5: the morning DM carries prune actions merged under the
// facts — an expired leaf lands in the owner's digest, not just REVIEW.
func TestNightTickDigestIncludesPrune(t *testing.T) {
	c := baseTestConfig(t)
	root := filepath.Join(t.TempDir(), "vault")
	if err := vault.Ensure(root); err != nil {
		t.Fatal(err)
	}
	c.VaultDir = root
	c.MemoryRollupTime = "00:00"
	c.MemoryLeafRetentionDays = 7

	old := time.Now().AddDate(0, 0, -30)
	daily, err := vault.OpenDaily(root, time.Local)
	if err != nil {
		t.Fatal(err)
	}
	if err := daily.Append(memory.Entry{ID: "old-1", Kind: memory.KindInteractive,
		Provenance: memory.ProvenanceOwner, Audience: memory.AudiencePrivate,
		Body: "old day content", ObservedAt: old}, "test"); err != nil {
		t.Fatal(err)
	}
	dayKey := old.Format("2006-01-02")
	cache := filepath.Join(root, "runtime", "memory", "night", dayKey+".json")
	if err := os.MkdirAll(filepath.Dir(cache), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(cache, []byte(`{"entries":{},"steps":{"facts":true,"links":true,"truths":true,"intents":true,"core":true,"summary":true,"journal":true}}`), 0600); err != nil {
		t.Fatal(err)
	}
	w := vault.NewWriter(root)
	if _, err := w.Apply(memory.WriteOp{Path: "memory/summaries/daily/" + dayKey + ".md",
		Content: []byte("---\ntype: daily-summary\nstatus: active\ntags: []\ncreated: " + dayKey + "\nupdated: " + dayKey + "\n---\n\nold digest\n"),
		Actor:   memory.ActorSystem}); err != nil {
		t.Fatal(err)
	}

	g, err := New(c)
	if err != nil {
		t.Fatal(err)
	}
	g.nightRelay = func(ctx context.Context, system, user string) (string, error) {
		return `{"summary":"night","facts":[]}`, nil
	}
	g.tickNightRollup(time.Now())

	key := "night-digest-" + time.Now().Local().Format("2006-01-02")
	b, err := os.ReadFile(filepath.Join(c.SpoolDir, "outbox", "pending", key+".json"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(b), dayKey) {
		t.Fatalf("digest missing prune line: %s", b)
	}
}
