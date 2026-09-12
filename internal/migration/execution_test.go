package migration

import (
	"encoding/json"
	"github.com/letya999/hermes-hub/internal/communication"
	"github.com/letya999/hermes-hub/internal/identity"
	"github.com/letya999/hermes-hub/internal/stack"
	"net/url"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestExecutionSelectionPreservesQueueAndRollsBackOnlyOneContext(t *testing.T) {
	root := t.TempDir()
	alice, bob := filepath.Join(root, "alice"), filepath.Join(root, "bob")
	for _, dir := range []string{alice, bob} {
		if err := stack.Init(dir, filepath.Base(dir)); err != nil {
			t.Fatal(err)
		}
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
	s.Features = []string{"telegram"}
	if _, ok := stack.Compose(s, "", alice)["services"].(stack.M)["hermes-runtime"]; ok {
		t.Fatal("migrated context resident")
	}
	bobSettings, err := stack.ReadEnvironment(bob, "prod")
	if err != nil || bobSettings.ExecutionMode != "" {
		t.Fatal("Bob selection changed")
	}
	selection.Mode, selection.SupervisorURL = "static", ""
	if report, err := SelectExecution(alice, selection, audit, true); err != nil || report.Previous == nil || report.Previous.Mode != "supervisor" {
		t.Fatal(err)
	}
	s, err = stack.ReadEnvironment(alice, "prod")
	if err != nil {
		t.Fatal(err)
	}
	s.Features = []string{"telegram"}
	t.Setenv("HUB_RUNTIME_SUPERVISOR_URL", "http://global-selection:8765")
	services := stack.Compose(s, "", alice)["services"].(stack.M)
	if _, ok := services["hermes-runtime"]; !ok {
		t.Fatal("rollback overridden by global env")
	}
	if services["communication-hub"].(stack.M)["environment"].(stack.M)["HUB_RUNTIME_SUPERVISOR_URL"] != "" {
		t.Fatal("rollback still chooses supervisor")
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
