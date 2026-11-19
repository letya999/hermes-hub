package supervisor

import (
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	task "github.com/letya999/hermes-hub/contracts/task/v1"
	"github.com/letya999/hermes-hub/contracts/task/v1/conformance"
	"github.com/letya999/hermes-hub/internal/commodel"
	"github.com/letya999/hermes-hub/internal/identity"
	"github.com/letya999/hermes-hub/internal/identityhub"
	hubruntime "github.com/letya999/hermes-hub/internal/runtime"
)

func TestSupervisorTaskContractRequiresBearer(t *testing.T) {
	for _, tc := range []struct{ name, token, header string }{
		{"raw_token", "expected", "expected"},
		{"empty_config", "", "Bearer "},
		{"no_header", "expected", ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			manager := &Manager{cfg: Config{RuntimeAuth: tc.token}}
			request := httptest.NewRequest(http.MethodGet, "/v1/jobs/job-1", nil)
			request.Header.Set("Authorization", tc.header)
			response := httptest.NewRecorder()
			manager.Handler().ServeHTTP(response, request)
			if response.Code != http.StatusUnauthorized {
				t.Fatalf("invalid authentication reached the job route: %d", response.Code)
			}
		})
	}
}

func TestContractConformanceInProcAndHTTP(t *testing.T) {
	envelope := identity.TelegramEnvelope("alice", 11, "alice", "policy-1")
	manager := &Manager{
		cfg: Config{Environment: "dev"},
		jobs: map[string]jobRecord{"job-1": {
			AdmissionID: "admission-1", RequestTextSHA256: "input-hash", Generation: "gen-1", Status: "running",
			Request: hubruntime.ExecuteRequest{Envelope: envelope, UserID: "alice", ScopeID: "user:alice", Trigger: "cron", Channel: "telegram_bot",
				TaskID: "named-task", Assignment: true, AssignmentID: "asg-owner", AssignmentPlanRevision: 2},
			RunDeadline: time.Now().UTC().Add(time.Hour).Truncate(time.Second),
			Response:    hubruntime.ExecuteResponse{JobID: "job-1", RunID: "run-1", SessionID: "session-1"},
		}},
		items: map[string]*runtimeEntry{runtimeKey(Binding{PrincipalID: "alice", ContextID: "alice", RuntimeMode: "gateway"}): {Runtime: Runtime{Generation: "gen-1"}}},
	}
	server, ok := any(manager).(task.Client)
	if !ok {
		t.Fatal("supervisor does not implement the task contract")
	}
	known := task.RunRecord{JobID: "job-1", AdmissionID: "admission-1", RunID: "run-1", SessionID: "session-1",
		RuntimeGeneration: "gen-1", CurrentGeneration: "gen-1", Environment: "dev", Status: "running",
		ScopeID: "user:alice", Trigger: "cron", Channel: "telegram_bot", UserID: "alice", TextSHA256: "input-hash",
		TaskID: "named-task", Assignment: true, AssignmentID: "asg-owner", AssignmentPlanRevision: 2, Deadline: manager.jobs["job-1"].RunDeadline,
		ResponseSHA256: "e3b0c44298fc1c149afbf4c8996fb92427ae41e4649b934ca495991b7852b855", Envelope: envelope}
	conformance.Both(t, server, func(token string) http.Handler {
		manager.cfg.RuntimeAuth = token
		return manager.Handler()
	}, known)
}

func TestTaskRunReportsRevocationBeforeTerminalOutcome(t *testing.T) {
	auth := identity.TelegramEnvelope("alice", 11, "alice", "policy-1")
	now := time.Now()
	for _, reason := range []string{"cancel requested", "administrative hold", "epoch advanced", "deadline elapsed"} {
		t.Run(reason, func(t *testing.T) {
			record := jobRecord{Status: "running", Generation: "gen-1", RunDeadline: now.Add(time.Hour),
				Request: hubruntime.ExecuteRequest{Envelope: auth}, Response: hubruntime.ExecuteResponse{RunID: "run-1"}}
			var hold AdministrativeHold
			switch reason {
			case "cancel requested":
				record.CancelRequested = true
			case "administrative hold":
				hold.Active = true
			case "epoch advanced":
				hold.Epoch = 1
			case "deadline elapsed":
				record.RunDeadline = now.Add(-time.Second)
			}
			m := &Manager{jobs: map[string]jobRecord{"job-1": record}, holds: map[string]AdministrativeHold{holdKey("alice", "alice"): hold}}
			got, err := m.Run(t.Context(), "job-1")
			if err != nil || !got.Revoked || got.Status != "running" {
				t.Fatalf("run revocation not visible before terminal state: %v", err)
			}
		})
	}
}

func TestTaskRunRevokesBoundGroupActorWhileRuntimeIsCurrent(t *testing.T) {
	registry, mappings := fixtureIdentity(t)
	const group = "team"
	_, err := registry.ImportLegacyConfiguration(t.Context(), identityhub.LegacyConfiguration{
		Name: "group", Organization: "personal", Users: []commodel.User{{ID: group, Enabled: true}},
		SharedSpaces: []identityhub.LegacySharedSpace{{ID: group, Members: []int64{7}}},
	})
	if err != nil {
		t.Fatal(err)
	}
	m := &Manager{cfg: Config{Identity: registry, Environment: "dev"}}
	binding := Binding{PrincipalID: group, ContextID: group, RuntimeID: group, RuntimeMode: "gateway", ScopeID: "group:" + group,
		SubjectID: "alice", AgentID: mappings[0].Agent, OrganizationID: "personal"}
	_, revision, err := m.identityTemplate(t.Context(), binding)
	if err != nil {
		t.Fatal(err)
	}
	auth := identity.TelegramEnvelope(group, 7, group, "policy-1")
	m.jobs = map[string]jobRecord{"job-group": {
		Status: "running", Generation: "gen-1",
		Request:        hubruntime.ExecuteRequest{Envelope: auth, ScopeID: binding.ScopeID},
		ActingIdentity: task.ActingIdentity{Subject: binding.SubjectID, Actor: binding.AgentID, Organization: "personal", TemplateRevision: revision},
		Response:       hubruntime.ExecuteResponse{RunID: "run-group"},
	}}
	m.items = map[string]*runtimeEntry{runtimeKey(binding): {Runtime: Runtime{Generation: "gen-1"}}}
	if got, err := m.Run(t.Context(), "job-group"); err != nil || got.Revoked || got.ActingIdentity.Subject != "alice" {
		t.Fatalf("live shared-resource run lost its human actor: %v", err)
	}
	if err := registry.DeactivateHumanAsOperator(t.Context(), "alice"); err != nil {
		t.Fatal(err)
	}
	if got, err := m.Run(t.Context(), "job-group"); err != nil || !got.Revoked || got.CurrentGeneration != "gen-1" {
		t.Fatalf("revoked group human kept authority in current runtime: %v", err)
	}
}

func TestPreUpgradeJobReceiptReplaysWithoutAcquiringNewLineage(t *testing.T) {
	old := hubruntime.ExecuteRequest{Envelope: identity.TelegramEnvelope("alice", 11, "alice", "policy-1"),
		JobID: "job-1", IdempotencyKey: "job-1", Text: "original"}
	record := jobRecord{Fingerprint: executeFingerprint(old), Request: old, Status: "completed",
		Response: hubruntime.ExecuteResponse{Status: "completed", RunID: "run-1"}}
	m := &Manager{jobs: map[string]jobRecord{old.JobID: record}}
	request := old
	request.TaskID = "named-task"
	if result, replay, err := m.beginJob(request); err != nil || !replay || result.RunID != "run-1" {
		t.Fatalf("legacy terminal receipt lost: %v", err)
	}
	if got := m.jobs[old.JobID].Request.TaskID; got != "" {
		t.Fatal("a retry assigned new lineage to a legacy receipt")
	}
	request.Text += " changed"
	if _, replay, err := m.beginJob(request); err == nil || replay {
		t.Fatal("changed legacy payload replayed")
	}
}
