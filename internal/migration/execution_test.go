package migration

import (
	"encoding/json"
	"github.com/letya999/hermes-hub/internal/communication"
	"github.com/letya999/hermes-hub/internal/identity"
	"github.com/letya999/hermes-hub/internal/stack"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// publishExecutionRevision stamps a loadable managed revision whose stack
// declares no hermes-runtime service, so the execution-selection file —
// not a supervised declaration — decides s.ExecutionMode.
func publishExecutionRevision(t *testing.T, dir, user, revision string) {
	t.Helper()
	revDir := filepath.Join(dir, "generated", "passports", revision)
	if err := os.MkdirAll(revDir, 0700); err != nil {
		t.Fatal(err)
	}
	env := "apiVersion: six-passport/v2\nmetadata: {space: " + user + ", environment: prod, revision: " + revision + ", generated_at: 2026-11-02T00:00:00Z}\n"
	docs := map[string]string{
		"stack": "kind: stack\nidentity: {user: " + user + ", infra: false}\nservices:\n" +
			"  model-relay: {mode: per-space}\n" +
			"  toolhub-relay: {mode: per-space}\n" +
			"  toolhub: {mode: shared}\n" +
			"  credential-broker: {mode: shared}\n",
		"config":    "kind: config\nruntime: {timezone: UTC}\nports: {oauth-callback: 8008, browser-vnc: 6081}\n",
		"models":    "kind: models\ndefault: m\nroutes:\n  m: {ladder: [cliproxy]}\nservices:\n  cliproxy: {via: cliproxy, endpoint: http://cliproxy:8317/v1, models: [{id: m}]}\n",
		"workspace": "kind: workspace\nwritable: [/workspace]\nread_only: [/vault/memory]\n",
		"tools":     "kind: tools\nbinding: {profile_id: " + user + "-default, generation: 1}\ncore:\n  files: {via: toolhub}\n",
		"grants":    "kind: grants\nallow: {}\n",
	}
	for name, body := range docs {
		if err := os.WriteFile(filepath.Join(revDir, name+".yaml"), []byte(env+body), 0600); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(filepath.Join(dir, "generated", "passports", "current"), []byte(revision+"\n"), 0600); err != nil {
		t.Fatal(err)
	}
	view := filepath.Join(dir, "generated", "config-view", "prod")
	if err := os.MkdirAll(view, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(view, "revision"), []byte(revision+"\n"), 0600); err != nil {
		t.Fatal(err)
	}
}

func TestExecutionSelectionPreservesQueueAndRollsBackOnlyOneContext(t *testing.T) {
	root := t.TempDir()
	alice, bob := filepath.Join(root, "alice"), filepath.Join(root, "bob")
	for _, dir := range []string{alice, bob} {
		if err := stack.Init(dir, filepath.Base(dir)); err != nil {
			t.Fatal(err)
		}
		publishExecutionRevision(t, dir, filepath.Base(dir), "r-0000000000000001")
	}
	spoolDir := filepath.Join(root, "spool")
	spool, err := communication.NewSpool(spoolDir)
	if err != nil {
		t.Fatal(err)
	}
	job := communication.Job{Envelope: identity.TelegramEnvelope("alice", 11, "alice", "policy-1"), ID: "queued", IdempotencyKey: "queued", OrganizationID: "personal", UserID: "alice", ActorID: "alice", ScopeID: "user:alice", Channel: "telegram_bot", Trigger: "message", ChatID: 11, Text: "keep this input", CreatedAt: time.Now()}
	if _, err := spool.Enqueue(job); err != nil {
		t.Fatal(err)
	}
	if err := spool.EnqueueDelivery(communication.Delivery{ID: "saved-final", ChatID: 11, Text: "saved result"}); err != nil {
		t.Fatal(err)
	}
	before, err := os.ReadFile(filepath.Join(spoolDir, "pending", "queued.json"))
	if err != nil {
		t.Fatal(err)
	}
	audit, err := AuditExecutionSpool(spoolDir, "alice")
	if err != nil || audit.Pending != 1 || audit.PendingDeliveries != 1 {
		t.Fatalf("audit=%+v %v", audit, err)
	}
	audit.NativeCron = &NativeCronSnapshot{HermesPin: HermesMigrationPin, ActiveJobs: 0}
	selection := stack.ExecutionSelection{Schema: 1, User: "alice", Environment: "prod", Mode: "supervisor", SupervisorURL: "http://host.docker.internal:8765", NativeCron: "disabled", CompatibilityRelease: "0.2.0"}
	if report, err := SelectExecution(alice, selection, audit, false); err != nil || report.Mode != "dry-run" {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(alice, stack.ExecutionPath("prod"))); !os.IsNotExist(err) {
		t.Fatal("dry run wrote routing")
	}
	if report, err := SelectExecution(alice, selection, audit, true); err != nil || report.Mode != "applied" {
		t.Fatal(err)
	}
	s, err := stack.ReadEnvironment(alice, "prod")
	if err != nil {
		t.Fatal(err)
	}
	// A managed space never renders a resident hermes-runtime: the
	// observable selection state is the effective execution mode.
	if s.ExecutionMode != "supervisor" {
		t.Fatalf("supervisor selection not effective: %q", s.ExecutionMode)
	}
	if _, ok := stack.Compose(s, "", alice)["services"].(stack.M)["hermes-runtime"]; ok {
		t.Fatal("migrated context resident")
	}
	bobSettings, err := stack.ReadEnvironment(bob, "prod")
	if err != nil || bobSettings.ExecutionMode != "" {
		t.Fatal("Bob selection changed")
	}
	if _, err := os.Stat(filepath.Join(bob, stack.ExecutionPath("prod"))); !os.IsNotExist(err) {
		t.Fatal("Bob selection file written")
	}
	selection.Mode, selection.SupervisorURL = "static", ""
	if report, err := SelectExecution(alice, selection, audit, true); err != nil || report.Previous == nil || report.Previous.Mode != "supervisor" {
		t.Fatal(err)
	}
	s, err = stack.ReadEnvironment(alice, "prod")
	if err != nil {
		t.Fatal(err)
	}
	if s.ExecutionMode != "static" {
		t.Fatalf("rollback selection not effective: %q", s.ExecutionMode)
	}
	// A managed space rolled back to a resident-runtime mode is a refused
	// render, not a silent static stack.
	if err := stack.RenderEnvironment(alice, root, "prod"); err == nil || !strings.Contains(err.Error(), "supervisor-executed") {
		t.Fatalf("managed static render accepted: %v", err)
	}
	after, _ := os.ReadFile(filepath.Join(spoolDir, "pending", "queued.json"))
	if string(after) != string(before) {
		t.Fatal("queue rewritten")
	}
	claimed, err := spool.ClaimJob()
	if err != nil || claimed.ID != job.ID {
		t.Fatal("queued job lost")
	}
	if _, err := AuditExecutionSpool(spoolDir, "alice"); err == nil {
		t.Fatal("claimed job accepted")
	}
}

func TestExecutionAuditRejectsUncertainAdmittedAndForgedMappings(t *testing.T) {
	for _, status := range []string{"uncertain", "admitted", "forged"} {
		t.Run(status, func(t *testing.T) {
			root := t.TempDir()
			dir := filepath.Join(root, "alice")
			if err := stack.Init(dir, "alice"); err != nil {
				t.Fatal(err)
			}
			spoolDir := filepath.Join(root, "spool")
			spool, err := communication.NewSpool(spoolDir)
			if err != nil {
				t.Fatal(err)
			}
			job := communication.Job{Envelope: identity.TelegramEnvelope("alice", 11, "alice", "policy-1"), ID: "job", IdempotencyKey: "job", OrganizationID: "personal", UserID: "alice", ActorID: "alice", ScopeID: "user:alice", Channel: "telegram_bot", Trigger: "message", ChatID: 11, Text: "input"}
			if _, err := spool.Enqueue(job); err != nil {
				t.Fatal(err)
			}
			mapping, _, _ := spool.Mapping(job.ID)
			switch status {
			case "uncertain":
				mapping.Status = "uncertain"
			case "admitted":
				mapping.RunID = "original-run"
				mapping.Status = "uncertain"
			case "forged":
				mapping.DeliveryTargetID = "telegram-22"
			}
			body, _ := json.Marshal(mapping)
			if err := os.WriteFile(filepath.Join(spoolDir, "mappings", "job.json"), body, 0600); err != nil {
				t.Fatal(err)
			}
			audit, err := AuditExecutionSpool(spoolDir, "alice")
			if status != "uncertain" {
				if err == nil {
					t.Fatal("unsafe mapping accepted")
				}
				return
			}
			if err != nil || audit.Uncertain != 1 {
				t.Fatal(err)
			}
			selection := stack.ExecutionSelection{Schema: 1, User: "alice", Environment: "prod", Mode: "supervisor", SupervisorURL: "http://localhost:8765", NativeCron: "disabled", CompatibilityRelease: "0.2.0"}
			if _, err := SelectExecution(dir, selection, audit, true); err == nil {
				t.Fatal("uncertain executor transfer accepted")
			}
		})
	}
}

func TestExecutionSelectionRejectsWrongOwnerCronAndConcurrentSelectors(t *testing.T) {
	root := t.TempDir()
	if err := stack.Init(root, "alice"); err != nil {
		t.Fatal(err)
	}
	selection := stack.ExecutionSelection{Schema: 1, User: "alice", Environment: "prod", Mode: "supervisor", SupervisorURL: "http://localhost:8765", NativeCron: "disabled", CompatibilityRelease: "0.2.0"}
	for _, field := range []string{"user", "audit", "cron", "mode", "url", "release", "environment"} {
		t.Run(field, func(t *testing.T) {
			bad := selection
			audit := ExecutionReport{User: "alice"}
			switch field {
			case "user":
				bad.User = "bob"
			case "audit":
				audit.User = "bob"
			case "cron":
				bad.NativeCron = "unmigrated"
			case "mode":
				bad.Mode = "guess"
			case "url":
				origin := url.URL{Scheme: "http", Host: "localhost:8765", User: url.UserPassword("test-user", "test-password")}
				bad.SupervisorURL = origin.String()
			case "release":
				bad.CompatibilityRelease = ""
			case "environment":
				bad.Environment = "unknown"
			}
			if _, err := SelectExecution(root, bad, audit, true); err == nil {
				t.Fatal("unsafe selection accepted")
			}
		})
	}
	if err := os.WriteFile(filepath.Join(root, ".execution-lock"), nil, 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := SelectExecution(root, selection, ExecutionReport{User: "alice", NativeCron: &NativeCronSnapshot{HermesPin: HermesMigrationPin}}, true); err == nil {
		t.Fatal("concurrent selector accepted")
	}
}
