package main

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/letya999/hermes-hub/internal/communication"
	"github.com/letya999/hermes-hub/internal/identity"
)

// hubctl apply -f describes desired state declaratively: routines and
// knowledge files land on the local spool, reapplying is a no-op, and a
// divergent spec is a loud conflict instead of a silent overwrite.
func TestApplyFile(t *testing.T) {
	root := t.TempDir()
	spoolDir := filepath.Join(root, "spool")
	src := filepath.Join(root, "faq.md")
	if err := os.WriteFile(src, []byte("answers"), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg := filepath.Join(root, "setup.yaml")
	yaml := `
routines:
  - organization_id: personal
    runtime_id: alice
    policy_version: policy-1
    chat_id: 11
    channel: telegram_bot
    id: price-watch
    kind: watcher
    timezone: UTC
    expression: "*/15 * * * *"
    input: watch the price
    url: https://example.com/price
    regex: "price: (\\d+)"
  - organization_id: personal
    runtime_id: alice
    policy_version: policy-1
    chat_id: 11
    channel: telegram_bot
    id: standup
    kind: reminder
    timezone: UTC
    expression: "0 9 * * *"
    input: standup in 10
knowledge:
  - name: faq.md
    source: ` + src + `
`
	if err := os.WriteFile(cfg, []byte(yaml), 0o600); err != nil {
		t.Fatal(err)
	}
	rep, err := applyFile(spoolDir, "alice", cfg)
	if err != nil {
		t.Fatalf("apply: %v", err)
	}
	if rep.Routines["price-watch"] != "created" || rep.Routines["standup"] != "created" || rep.Knowledge["faq.md"] != "created" {
		t.Fatalf("report: %+v", rep)
	}
	spool, err := communication.NewSpool(spoolDir)
	if err != nil {
		t.Fatal(err)
	}
	caller := identity.TelegramEnvelope("alice", 11, "alice", "policy-1")
	items, err := spool.ListSchedules(caller)
	if err != nil || len(items) != 2 {
		t.Fatalf("schedules: %v %d", err, len(items))
	}
	body, err := os.ReadFile(filepath.Join(spoolDir, "knowledge", "alice", "faq.md"))
	if err != nil || string(body) != "answers" {
		t.Fatalf("knowledge file: %v %q", err, body)
	}
	// Reapplying the same file creates nothing — it is a pure no-op.
	rep, err = applyFile(spoolDir, "alice", cfg)
	if err != nil {
		t.Fatalf("reapply: %v", err)
	}
	for id, action := range rep.Routines {
		if action != "unchanged" {
			t.Fatalf("reapply recreated %s: %s", id, action)
		}
	}
	if rep.Knowledge["faq.md"] != "unchanged" {
		t.Fatalf("reapply knowledge: %+v", rep.Knowledge)
	}
	items, _ = spool.ListSchedules(caller)
	if len(items) != 2 {
		t.Fatalf("reapply duplicated: %d", len(items))
	}
	// A changed spec is an explicit conflict, never a silent mutation.
	yaml2 := `
routines:
  - organization_id: personal
    runtime_id: alice
    policy_version: policy-1
    chat_id: 11
    channel: telegram_bot
    id: standup
    kind: reminder
    timezone: UTC
    expression: "0 10 * * *"
    input: standup in 10
`
	if err := os.WriteFile(cfg, []byte(yaml2), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := applyFile(spoolDir, "alice", cfg); err == nil {
		t.Fatal("divergent reapply silently succeeded")
	}
	// A new schedule id in the same file still applies.
	yaml3 := `
routines:
  - organization_id: personal
    runtime_id: alice
    policy_version: policy-1
    chat_id: 11
    channel: telegram_bot
    id: deploy-check
    kind: reminder
    timezone: UTC
    expression: "once:2099-01-01T00:00:00Z"
    input: deploy friday
`
	if err := os.WriteFile(cfg, []byte(yaml3), 0o600); err != nil {
		t.Fatal(err)
	}
	rep, err = applyFile(spoolDir, "alice", cfg)
	if err != nil || rep.Routines["deploy-check"] != "created" {
		t.Fatalf("additive apply: %v %+v", err, rep)
	}
	// Invalid entries fail clearly, not halfway.
	bad := filepath.Join(root, "bad.yaml")
	if err := os.WriteFile(bad, []byte("routines:\n  - organization_id: personal\n    runtime_id: alice\n    policy_version: policy-1\n    chat_id: 11\n    channel: telegram_bot\n    id: x\n    expression: not-cron\n    input: hi\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := applyFile(spoolDir, "alice", bad); err == nil {
		t.Fatal("invalid routine accepted")
	}
	// kind=check needs a live baseline probe the offline path cannot run;
	// apply must refuse rather than file a silently-skipping schedule.
	chk := filepath.Join(root, "chk.yaml")
	if err := os.WriteFile(chk, []byte("routines:\n  - organization_id: personal\n    runtime_id: alice\n    policy_version: policy-1\n    chat_id: 11\n    channel: telegram_bot\n    id: chk1\n    kind: check\n    expression: \"0 * * * *\"\n    input: c\n    url: https://example.com/x\n    expect: changed\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := applyFile(spoolDir, "alice", chk); err == nil {
		t.Fatal("check routine accepted without baseline")
	}
	// The hubctl surface: `hubctl apply -f cfg --spool dir --user alice`.
	cfg4 := filepath.Join(root, "cli.yaml")
	if err := os.WriteFile(cfg4, []byte("routines:\n  - organization_id: personal\n    runtime_id: alice\n    policy_version: policy-1\n    chat_id: 11\n    channel: telegram_bot\n    id: cli-rem\n    kind: reminder\n    expression: \"once:2099-01-01T00:00:00Z\"\n    input: ping\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := run(t.Context(), []string{"apply", "-f", cfg4, "--spool", spoolDir, "--user", "alice"}); err != nil {
		t.Fatalf("hubctl apply: %v", err)
	}
	if err := run(t.Context(), []string{"apply"}); err == nil {
		t.Fatal("apply without flags accepted")
	}
	_ = time.Now
}

func TestApplyIgnoresServerOwnedPriorRoutingHistory(t *testing.T) {
	base := communication.Schedule{ScheduleID: "fixture", Envelope: identity.TelegramEnvelope("alice", 11, "alice", "policy-1")}
	withHistory := base
	withHistory.PriorEnvelopes = []identity.Envelope{identity.TelegramEnvelope("alice", 12, "alice", "policy-1")}
	if !sameRoutine(base, withHistory) {
		t.Fatal("server-owned prior audience changes declarative apply intent")
	}
}
