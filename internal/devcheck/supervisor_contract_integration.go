//go:build integration

package devcheck

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/letya999/hermes-hub/internal/communication"
	"github.com/letya999/hermes-hub/internal/identity"
	hubruntime "github.com/letya999/hermes-hub/internal/runtime"
	"github.com/letya999/hermes-hub/internal/stack"
	"github.com/letya999/hermes-hub/internal/supervisor"
)

// SupervisorSmoke exercises the real host supervisor against the pinned image:
// one context is started, kept ready, reaped, and cold-started from the same home.
func supervisorSmoke(ctx context.Context, image, providerURL string) error {
	suffix := make([]byte, 4)
	if _, err := rand.Read(suffix); err != nil {
		return err
	}
	network := fmt.Sprintf("hermes-supervisor-%x", suffix)
	userID := fmt.Sprintf("probe-%x", suffix)
	root, err := os.MkdirTemp("", "hermes-supervisor-smoke-")
	if err != nil {
		return err
	}
	defer os.RemoveAll(root)
	cleanup := func() { _ = exec.Command("docker", "network", "rm", network).Run() }
	defer cleanup()
	if err := exec.CommandContext(ctx, "docker", "network", "create", network).Run(); err != nil {
		return err
	}
	contextRoot := filepath.Join(root, userID)
	for _, name := range []string{"runtime", "hermes", "workspace", "connections", "connections/google", "connections/telegram", "connections/browser", "home", "cache", "archive"} {
		if err := os.MkdirAll(filepath.Join(contextRoot, name), 0777); err != nil {
			return err
		}
	}
	auth := "context-auth-0123456789abcdef0123456789abcdef"
	if err := os.WriteFile(filepath.Join(contextRoot, "settings.yaml"), []byte("schema: 1\nuser: "+userID+"\ntimezone: UTC\nbrowser_port: 9222\nfeatures: [workspace]\n"), 0600); err != nil {
		return err
	}
	settings, err := stack.ReadEnvironment(contextRoot, "prod")
	if err != nil {
		return err
	}
	policy := stack.PolicyVersion(settings)
	if err := os.WriteFile(filepath.Join(contextRoot, "runtime.auth"), []byte("HUB_RUNTIME_AUTH="+auth+"\nOPENAI_API_KEY=probe-openai-key-0123456789\nOPENAI_BASE_URL="+providerURL+"\n"), 0600); err != nil {
		return err
	}
	if err := os.WriteFile(filepath.Join(contextRoot, "hermes.prod.yaml"), []byte("model:\n  provider: custom\n  default: gpt-4o-mini\n  base_url: "+providerURL+"\n"), 0644); err != nil {
		return err
	}
	marker := filepath.Join(contextRoot, "workspace", "cold-restore-marker")
	if err := os.WriteFile(marker, []byte("keep"), 0600); err != nil {
		return err
	}
	canaries := []string{"home/memory-canary", "hermes/session-canary", "connections/credential-canary", "hermes/schedule-canary"}
	for _, name := range canaries {
		path := filepath.Join(contextRoot, filepath.FromSlash(name))
		if err := os.MkdirAll(filepath.Dir(path), 0777); err != nil {
			return err
		}
		if err := os.WriteFile(path, []byte("synthetic-preservation-canary"), 0600); err != nil {
			return err
		}
	}
	control := "supervisor-control-0123456789abcdef"
	// Synthetic bind directories must be writable by container uid10001 on Linux.
	if err := filepath.Walk(contextRoot, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		if info.IsDir() {
			return os.Chmod(path, 0777)
		}
		return nil
	}); err != nil {
		return err
	}
	cfg := supervisor.Config{SpacesRoot: root, Image: image, Network: network, RuntimeAuth: control, WarmTTL: time.Second, PortBase: 28000}
	var starts atomic.Int32
	cfg.Command = func(callCtx context.Context, args ...string) ([]byte, error) {
		if len(args) != 0 && args[0] == "run" {
			starts.Add(1)
		}
		output, err := exec.CommandContext(callCtx, "docker", args...).CombinedOutput()
		if err != nil && len(args) != 0 && args[0] == "run" {
			return output, fmt.Errorf("synthetic runtime launch: %w: %s", err, output)
		}
		return output, err
	}
	m, err := supervisor.New(cfg)
	if err != nil {
		return err
	}
	binding := supervisor.Binding{PrincipalID: userID, ContextID: userID, RuntimeID: userID, RuntimeMode: "gateway", UserID: userID, OrganizationID: "personal", PolicyVersion: policy, ContextRoot: contextRoot}
	defer func() {
		cleanupCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		if runtime, _, err := m.Status(binding); err == nil {
			if id, err := smokeOwnedContainerID(cleanupCtx, m, runtime); err == nil {
				_ = exec.CommandContext(cleanupCtx, "docker", "rm", "-f", id).Run()
			}
		}
	}()
	first, err := m.Ensure(ctx, binding)
	if err != nil {
		return err
	}
	if first.State != supervisor.Busy {
		return fmt.Errorf("supervisor runtime not busy: %s", first.State)
	}
	if err := m.ReleaseBinding(binding); err != nil {
		return err
	}
	if err := m.Reap(ctx, time.Now().Add(2*time.Second)); err != nil {
		return err
	}
	stopped, _, err := m.Status(binding)
	if err != nil || stopped.State != supervisor.Stopped {
		return fmt.Errorf("runtime did not stop: state=%s err=%v", stopped.State, err)
	}
	cold, err := m.Ensure(ctx, binding)
	if err != nil {
		return err
	}
	if cold.RuntimeID != first.RuntimeID || cold.Generation == first.Generation {
		return fmt.Errorf("cold start changed logical identity: first=%+v cold=%+v", first, cold)
	}
	if body, err := os.ReadFile(marker); err != nil || strings.TrimSpace(string(body)) != "keep" {
		return fmt.Errorf("context state was not preserved: %v", err)
	}
	streamLease, _, err := m.Acquire(ctx, binding, supervisor.LeaseStream)
	if err != nil {
		return err
	}
	if err := m.ReleaseBinding(binding); err != nil {
		return err
	}
	if err := m.Reap(ctx, time.Now().Add(2*time.Second)); err != nil {
		return err
	}
	held, _, err := m.Status(binding)
	if err != nil || held.State != supervisor.Busy || held.Generation != cold.Generation {
		return fmt.Errorf("active stream was reaped: %s %v", held.State, err)
	}
	if err := m.ReleaseLease(streamLease.ID); err != nil {
		return err
	}
	spool, err := communication.NewSpool(filepath.Join(root, "probe-spool"))
	if err != nil {
		return err
	}
	job := communication.Job{Envelope: identity.TelegramEnvelope(userID, 11, userID, policy), ID: "real-stream-probe", OrganizationID: "personal", UserID: userID, ActorID: userID, ScopeID: "user:" + userID, Channel: "telegram_bot", Trigger: "message", IdempotencyKey: "real-stream-probe", ChatID: 11, Text: "contract probe"}
	if _, err := spool.Enqueue(job); err != nil {
		return err
	}
	server := httptest.NewServer(m.Handler())
	defer server.Close()
	runner := communication.HTTPRunner{URL: server.URL, Auth: control, Spool: spool, Limit: time.Minute}
	outcome, err := runner.RunOutcome(ctx, job)
	if err != nil || outcome.Status != "completed" || outcome.Text != "contract final answer" {
		mapping, _, _ := spool.Mapping(job.ID)
		reason := "native outcome unavailable"
		if runtime, _, statusErr := m.Status(binding); statusErr == nil && mapping.RunID != "" {
			if id, ownErr := smokeOwnedContainerID(ctx, m, runtime); ownErr == nil {
				out := func(args ...string) ([]byte, error) { return dockerContractOutput(ctx, args...) }
				if native, nativeErr := hermesHTTP(ctx, out, id, auth, http.MethodGet, "/v1/runs/"+mapping.RunID, "", ""); nativeErr == nil {
					reason, _ = jsonString(native.body, "error")
				}
			}
		}
		return fmt.Errorf("real supervisor/runtime/Hermes stream failed: status=%s event=%s native=%s error=%v", outcome.Status, mapping.LastKnownEvent, reason, err)
	}
	finalCount := 0
	for range 12 {
		delivery, err := spool.ClaimDelivery()
		if err != nil {
			return err
		}
		if delivery == nil {
			break
		}
		if delivery.Text == "contract final answer" {
			finalCount++
		} else if delivery.JobID != "" {
			return fmt.Errorf("unexpected real-stream final payload")
		}
		if err := spool.CompleteDelivery(delivery.ID); err != nil {
			return err
		}
	}
	if finalCount != 1 {
		return fmt.Errorf("real stream final count=%d", finalCount)
	}
	receipts, err := os.ReadDir(filepath.Join(root, "probe-spool", "events"))
	if err != nil {
		return err
	}
	for _, receipt := range receipts {
		body, err := os.ReadFile(filepath.Join(root, "probe-spool", "events", receipt.Name()))
		if err != nil || strings.Contains(string(body), "HUB_TOOL_SECRET_CANARY") {
			return fmt.Errorf("real tool arguments/output leaked into delivery receipts")
		}
	}
	spool, err = communication.NewSpool(filepath.Join(root, "probe-spool"))
	if err != nil {
		return err
	}
	runner.Spool, runner.Resume = spool, true
	if _, err := runner.RunOutcome(ctx, job); err != nil {
		return fmt.Errorf("real cached terminal resume failed: %w", err)
	}
	if duplicate, err := spool.ClaimDelivery(); err != nil || duplicate != nil {
		return fmt.Errorf("real stream duplicate final after restart: %v", err)
	}
	if err := supervisorApprovalSmoke(ctx, m, binding, runner, spool, job, auth); err != nil {
		return err
	}
	interruptedJob := job
	interruptedJob.ID, interruptedJob.IdempotencyKey, interruptedJob.Text = "real-active-restart", "real-active-restart", "approval probe"
	if _, err := spool.Enqueue(interruptedJob); err != nil {
		return err
	}
	runner.Resume = false
	streamCtx, disconnect := context.WithCancel(ctx)
	finished := make(chan error, 1)
	go func() {
		_, err := runner.RunOutcome(streamCtx, interruptedJob)
		finished <- err
	}()
	defer disconnect()
	var admitted communication.JobMapping
	deadline := time.Now().Add(30 * time.Second)
	for time.Now().Before(deadline) {
		mapping, ok, err := spool.Mapping(interruptedJob.ID)
		if err != nil {
			return err
		}
		if ok && mapping.RunID != "" && mapping.Status == "waiting_for_approval" {
			admitted = mapping
			break
		}
		if err := contractSleep(ctx, 20*time.Millisecond); err != nil {
			return err
		}
	}
	if admitted.RunID == "" {
		return fmt.Errorf("real disconnect probe was not admitted")
	}
	disconnect()
	select {
	case err := <-finished:
		if err == nil {
			return fmt.Errorf("disconnected active stream unexpectedly completed")
		}
	case <-time.After(10 * time.Second):
		return fmt.Errorf("disconnected stream did not return")
	}
	before, _, err := m.Status(binding)
	if err != nil || before.Leases == 0 {
		return fmt.Errorf("disconnect released unresolved runtime hold")
	}
	containerID, err := smokeOwnedContainerID(ctx, m, before)
	if err != nil {
		return err
	}
	if err := exec.CommandContext(ctx, "docker", "kill", containerID).Run(); err != nil {
		return err
	}
	if err := exec.CommandContext(ctx, "docker", "rm", containerID).Run(); err != nil {
		return err
	}
	server.Close()
	m, err = supervisor.New(cfg)
	if err != nil {
		return err
	}
	m.ReconcileHealth(ctx, time.Now())
	loss, _, _ := m.Status(binding)
	if loss.CrashCount != 1 || !loss.Desired {
		return fmt.Errorf("real crash not counted/desired: count=%d desired=%t", loss.CrashCount, loss.Desired)
	}
	startsBeforeRecovery := starts.Load()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-time.After(3 * time.Second):
	}
	var recoveries sync.WaitGroup
	for range 4 {
		recoveries.Add(1)
		go func() { defer recoveries.Done(); m.RecoverMissingWork(ctx) }()
	}
	recoveries.Wait()
	replacement, _, err := m.Status(binding)
	if err != nil || replacement.Generation == before.Generation || replacement.State != supervisor.Busy {
		return fmt.Errorf("real automatic replacement generation failed: state=%s error=%v", replacement.State, err)
	}
	m.RecoverMissingWork(ctx)
	stable, _, _ := m.Status(binding)
	if stable.Generation != replacement.Generation {
		return fmt.Errorf("automatic recovery created duplicate replacement")
	}
	if starts.Load() != startsBeforeRecovery+1 {
		return fmt.Errorf("automatic recovery performed multiple Docker starts")
	}
	server = httptest.NewServer(m.Handler())
	defer server.Close()
	runner.URL, runner.Resume = server.URL, true
	recovered, err := runner.RunOutcome(ctx, interruptedJob)
	if err == nil || recovered.Status != "interrupted" || recovered.RunID != admitted.RunID || recovered.RuntimeGeneration != replacement.Generation {
		return fmt.Errorf("original run recovery across generation failed: status=%s error=%v", recovered.Status, err)
	}
	staleApproval := hubruntime.RunControl{RunReference: hubruntime.RunReference{ExecuteRequest: hubruntime.ExecuteRequest{Envelope: interruptedJob.Envelope, JobID: interruptedJob.ID, OrganizationID: interruptedJob.OrganizationID, UserID: interruptedJob.UserID, ActorID: interruptedJob.ActorID, ScopeID: interruptedJob.ScopeID, Channel: interruptedJob.Channel, Trigger: interruptedJob.Trigger, IdempotencyKey: interruptedJob.IdempotencyKey}, RunID: admitted.RunID, SessionID: admitted.SessionID, RuntimeGeneration: before.Generation}, Action: "approve", RequestID: admitted.ApprovalID, Choice: "once"}
	staleBody, _ := json.Marshal(staleApproval)
	staleRequest := httptest.NewRequest(http.MethodPost, "/v1/control", bytes.NewReader(staleBody))
	staleRequest.Header.Set("Authorization", "Bearer "+control)
	staleResponse := httptest.NewRecorder()
	m.Handler().ServeHTTP(staleResponse, staleRequest)
	if staleResponse.Code != 409 {
		return fmt.Errorf("replaced native approval accepted stale response: %d", staleResponse.Code)
	}
	late := hubruntime.ExecuteResponse{JobID: interruptedJob.ID, RunID: admitted.RunID, SessionID: admitted.SessionID, RuntimeGeneration: before.Generation, Status: "completed", LastEvent: "run.completed", EventID: "late-old-generation", Text: "obsolete final"}
	if err := spool.RecordStreamEvent(interruptedJob, late); err == nil {
		return fmt.Errorf("obsolete real runtime generation published a final")
	}
	errorCount := 0
	for range 12 {
		delivery, err := spool.ClaimDelivery()
		if err != nil {
			return err
		}
		if delivery == nil {
			break
		}
		if delivery.ID == "job-"+interruptedJob.ID+"-error" {
			errorCount++
		}
		if delivery.JobID != "" {
			return fmt.Errorf("recovery produced a duplicate/replacement final")
		}
		if err := spool.CompleteDelivery(delivery.ID); err != nil {
			return err
		}
	}
	if errorCount != 1 {
		return fmt.Errorf("interrupted run error delivery count=%d", errorCount)
	}
	if _, err := smokeWaitIdle(ctx, m, binding); err != nil {
		return err
	}
	_ = m.ReleaseBinding(binding)
	_ = m.Reap(ctx, time.Now().Add(2*time.Second))
	if _, err := smokeWaitIdle(ctx, m, binding); err != nil {
		return err
	}
	if err := supervisorDesiredSourcesSmoke(ctx, m, binding, runner, job, root, image, auth, &starts); err != nil {
		return err
	}
	for _, name := range canaries {
		body, err := os.ReadFile(filepath.Join(contextRoot, filepath.FromSlash(name)))
		if err != nil || string(body) != "synthetic-preservation-canary" {
			return fmt.Errorf("recovery purged %s: %v", name, err)
		}
	}
	fmt.Println("Host supervisor Docker smoke passed: warm reuse, idle reap, cold restore, logical runtime ID")
	return nil
}

func smokeWaitIdle(ctx context.Context, m *supervisor.Manager, binding supervisor.Binding) (supervisor.Runtime, error) {
	deadline := time.Now().Add(10 * time.Second)
	for {
		runtime, _, err := m.Status(binding)
		if err != nil {
			return runtime, err
		}
		if runtime.Leases == 0 && (runtime.State == supervisor.Idle || runtime.State == supervisor.Stopped) {
			return runtime, nil
		}
		if time.Now().After(deadline) {
			return runtime, fmt.Errorf("terminal lease did not settle: state=%s leases=%d", runtime.State, runtime.Leases)
		}
		if err := contractSleep(ctx, 20*time.Millisecond); err != nil {
			return runtime, err
		}
	}
}

func supervisorDesiredSourcesSmoke(ctx context.Context, m *supervisor.Manager, binding supervisor.Binding, runner communication.HTTPRunner, template communication.Job, root, image, nativeAuth string, starts *atomic.Int32) error {
	request := hubruntime.ExecuteRequest{Envelope: template.Envelope, UserID: template.UserID, ActorID: template.ActorID, OrganizationID: template.OrganizationID, ScopeID: template.ScopeID}
	if err := m.SetPin(request, true); err != nil {
		return err
	}
	m.ReconcilePins(ctx)
	pinned, _, err := m.Status(binding)
	if err != nil || pinned.State != supervisor.Idle {
		return fmt.Errorf("real pin did not wake: %v", err)
	}
	m.ReconcileHealth(ctx, time.Now())
	pinned, _, err = m.Status(binding)
	if err != nil || pinned.RuntimeHealth != "running" || pinned.ConnectorHealth == "not_checked" {
		return fmt.Errorf("real layered health failed: process=%s hermes=%s connector=%s error=%v", pinned.RuntimeHealth, pinned.HermesReadiness, pinned.ConnectorHealth, err)
	}
	nativeID, err := smokeOwnedContainerID(ctx, m, pinned)
	if err != nil {
		return err
	}
	nativeHealth, err := hermesHTTP(ctx, func(args ...string) ([]byte, error) { return dockerContractOutput(ctx, args...) }, nativeID, nativeAuth, http.MethodGet, "/health/detailed", "", "")
	var health struct {
		Readiness struct {
			Status string `json:"status"`
		} `json:"readiness"`
	}
	if err != nil || nativeHealth.status != 200 || json.Unmarshal(nativeHealth.body, &health) != nil {
		return fmt.Errorf("real native health comparison unavailable")
	}
	expected := "unavailable"
	if health.Readiness.Status == "ok" {
		expected = "ready"
	}
	if pinned.HermesReadiness != expected {
		return fmt.Errorf("layered readiness differs from native observation")
	}
	fmt.Printf("Real layered health passed: process running; native readiness %s; connections %s\n", health.Readiness.Status, pinned.ConnectorHealth)
	if err := supervisorOrphanSmoke(ctx, m, pinned, image); err != nil {
		return err
	}
	if err := m.Reap(ctx, time.Now().Add(time.Hour)); err != nil {
		return err
	}
	kept, _, _ := m.Status(binding)
	if kept.Generation != pinned.Generation || kept.State == supervisor.Stopped {
		return fmt.Errorf("real pin was reaped")
	}
	if err := m.SetPin(request, false); err != nil {
		return err
	}
	raceSpool, err := communication.NewSpool(filepath.Join(root, "reap-race-spool"))
	if err != nil {
		return err
	}
	raceJob := template
	raceJob.ID, raceJob.IdempotencyKey, raceJob.Text = "real-reap-race", "real-reap-race", "contract probe"
	if _, err := raceSpool.Enqueue(raceJob); err != nil {
		return err
	}
	raceRunner := runner
	raceRunner.Spool, raceRunner.Resume = raceSpool, false
	start := make(chan struct{})
	results := make(chan error, 2)
	beforeRace := starts.Load()
	go func() { <-start; results <- m.Reap(ctx, time.Now().Add(time.Hour)) }()
	go func() {
		<-start
		outcome, err := raceRunner.RunOutcome(ctx, raceJob)
		// Pinned Hermes conservatively retains a crashed turn lease for 300s when
		// Docker reuses its PID. Keep the admitted run and use production resume;
		// never delete upstream locks or resubmit the prompt to make a fast test.
		admittedRun := outcome.RunID
		settleDeadline := time.Now().Add(6 * time.Minute)
		if errors.Is(err, communication.ErrUncertain) && admittedRun != "" {
			fmt.Println("Real native crashed-turn wait: original run held; resume without input replay")
		}
		for errors.Is(err, communication.ErrUncertain) && admittedRun != "" && time.Now().Before(settleDeadline) {
			m.ReconcileHealth(ctx, time.Now())
			current, _, statusErr := m.Status(binding)
			if statusErr != nil || current.Leases == 0 || !current.Desired {
				err = fmt.Errorf("waiting native lease lost desired work/hold")
				break
			}
			if reapErr := m.Reap(ctx, time.Now().Add(time.Hour)); reapErr != nil {
				err = reapErr
				break
			}
			if waitErr := contractSleep(ctx, 5*time.Second); waitErr != nil {
				err = waitErr
				break
			}
			outcome, err = raceRunner.RunOutcome(ctx, raceJob)
			if outcome.RunID != admittedRun {
				err = fmt.Errorf("waiting native lease changed admitted run")
				break
			}
		}
		if err != nil {
			mapping, _, _ := raceSpool.Mapping(raceJob.ID)
			err = fmt.Errorf("status=%s event=%s run=%s mapping=%s/%s: %w", outcome.Status, outcome.LastEvent, outcome.RunID, mapping.Status, mapping.LastKnownEvent, err)
			if current, _, e := m.Status(binding); e == nil && mapping.RunID != "" {
				if id, e := smokeOwnedContainerID(ctx, m, current); e == nil {
					if response, e := hermesHTTP(ctx, func(args ...string) ([]byte, error) { return dockerContractOutput(ctx, args...) }, id, nativeAuth, http.MethodGet, "/v1/runs/"+mapping.RunID, "", ""); e == nil {
						var native struct {
							Status string `json:"status"`
							Error  string `json:"error"`
						}
						if json.Unmarshal(response.body, &native) == nil {
							if len(native.Error) > 512 {
								native.Error = native.Error[:512]
							}
							err = fmt.Errorf("native=%s/%s: %w", native.Status, native.Error, err)
						}
					}
				}
			}
		}
		if err == nil && outcome.Status != "completed" {
			err = fmt.Errorf("race job did not complete")
		}
		results <- err
	}()
	close(start)
	for range 2 {
		if err := <-results; err != nil {
			return fmt.Errorf("real new-job/reap race: %w", err)
		}
	}
	if starts.Load() > beforeRace+1 {
		return fmt.Errorf("reap race started duplicate replacement")
	}
	kept, err = smokeWaitIdle(ctx, m, binding)
	if err != nil {
		return err
	}
	id, err := smokeOwnedContainerID(ctx, m, kept)
	if err != nil {
		return err
	}
	if err := exec.CommandContext(ctx, "docker", "rm", "-f", id).Run(); err != nil {
		return err
	}
	m.ReconcileHealth(ctx, time.Now())
	m.RecoverMissingWork(ctx)
	missingIdle, _, _ := m.Status(binding)
	if missingIdle.Generation != kept.Generation || missingIdle.RuntimeHealth != "missing" {
		return fmt.Errorf("missing idle registration woke")
	}
	if err := m.Reap(ctx, time.Now().Add(time.Hour)); err != nil {
		return err
	}
	m.ReconcileHealth(ctx, time.Now())
	m.RecoverMissingWork(ctx)
	idle, _, _ := m.Status(binding)
	if idle.State != supervisor.Stopped {
		return fmt.Errorf("idle registration woke without work")
	}
	spool, err := communication.NewSpool(filepath.Join(root, "routine-spool"))
	if err != nil {
		return err
	}
	occurrence := communication.RoutineOccurrence{ScheduleID: "synthetic-one-shot", Revision: 1, DueAt: time.Now().UTC(), Job: template}
	occurrence.Job.Text = "contract probe"
	if err := spool.PutOccurrence(occurrence, template.Envelope); err != nil {
		return err
	}
	for range 2 {
		if err := spool.DispatchDueOccurrences(time.Now(), func(job communication.Job) error {
			if job.Envelope != template.Envelope {
				return fmt.Errorf("routine authority changed")
			}
			return nil
		}); err != nil {
			return err
		}
	}
	job, err := spool.ClaimJob()
	if err != nil || job == nil {
		return fmt.Errorf("due routine did not enqueue: %v", err)
	}
	runner.Spool, runner.Resume = spool, false
	if outcome, err := runner.RunOutcome(ctx, *job); err != nil || outcome.Status != "completed" {
		mapping, _, _ := spool.Mapping(job.ID)
		return fmt.Errorf("real due routine wake failed: status=%s event=%s run=%s mapping=%s/%s error=%v", outcome.Status, outcome.LastEvent, outcome.RunID, mapping.Status, mapping.LastKnownEvent, err)
	}
	if err := spool.CompleteJob(job.ID); err != nil {
		return err
	}
	finalFound := false
	for range 16 {
		delivery, err := spool.ClaimDelivery()
		if err != nil {
			return fmt.Errorf("routine durable final missing: %v", err)
		}
		if delivery == nil {
			break
		}
		if delivery.Text == "contract final answer" && delivery.JobID == job.ID {
			finalFound = true
		} else if delivery.JobID != "" {
			return fmt.Errorf("routine published an unexpected final")
		}
		if err := spool.CompleteDelivery(delivery.ID); err != nil {
			return err
		}
		if finalFound {
			break
		}
	}
	if !finalFound {
		return fmt.Errorf("routine durable final missing: progress exhausted")
	}
	if duplicate, err := spool.ClaimJob(); err != nil || duplicate != nil {
		return fmt.Errorf("routine dispatched twice")
	}
	if _, err := smokeWaitIdle(ctx, m, binding); err != nil {
		return err
	}
	if err := m.Reap(ctx, time.Now().Add(time.Hour)); err != nil {
		return err
	}
	settled, _, _ := m.Status(binding)
	if settled.State != supervisor.Stopped {
		return fmt.Errorf("routine runtime did not sleep after handoff")
	}
	fmt.Println("Real desired-state sources passed: operator pin, idle no-wake, due routine wake/final/handoff/sleep and preservation canaries")
	return nil
}

func supervisorOrphanSmoke(ctx context.Context, m *supervisor.Manager, current supervisor.Runtime, image string) error {
	id, err := smokeOwnedContainerID(ctx, m, current)
	if err != nil {
		return err
	}
	raw, err := exec.CommandContext(ctx, "docker", "inspect", "--format", "{{json .Config.Labels}}", id).Output()
	var labels map[string]string
	if err != nil || json.Unmarshal(raw, &labels) != nil {
		return fmt.Errorf("real ownership labels unavailable")
	}
	var created []string
	defer func() {
		for _, id := range created {
			_ = exec.Command("docker", "rm", "-f", id).Run()
		}
	}()
	for _, foreign := range []bool{false, true} {
		owner := labels["hermes-hub.owner"]
		if foreign {
			owner = "synthetic-other-supervisor"
		}
		out, err := exec.CommandContext(ctx, "docker", "create", "--name", fmt.Sprintf("%s-inventory-%t", current.Container, foreign), "--label", "hermes-hub.owner="+owner, "--label", "hermes-hub.context="+labels["hermes-hub.context"], "--label", "hermes-hub.generation="+current.Generation, image).Output()
		fixtureID := strings.TrimSpace(string(out))
		if err != nil || len(fixtureID) != 64 {
			return fmt.Errorf("real orphan fixture create failed: %v", err)
		}
		if _, err := hex.DecodeString(fixtureID); err != nil {
			return err
		}
		created = append(created, fixtureID)
	}
	if err := m.DetectOrphans(ctx); err != nil {
		return err
	}
	orphans := m.Orphans()
	if len(orphans) != 1 || !strings.HasPrefix(created[0], orphans[0].Container) {
		return fmt.Errorf("real same-generation orphan or foreign isolation failed: count=%d", len(orphans))
	}
	m.ReapOrphans(ctx)
	for _, id := range created {
		if err := exec.CommandContext(ctx, "docker", "inspect", id).Run(); err != nil {
			return fmt.Errorf("unknown or foreign orphan was removed")
		}
		if err := exec.CommandContext(ctx, "docker", "rm", "-f", id).Run(); err != nil {
			return err
		}
	}
	created = nil
	if err := m.DetectOrphans(ctx); err != nil {
		return err
	}
	if len(m.Orphans()) != 0 {
		return fmt.Errorf("orphan inventory did not settle after verified test cleanup")
	}
	fmt.Println("Real orphan inventory passed: duplicate generation detected; unknown and foreign compute preserved")
	return nil
}

// supervisorApprovalSmoke uses the production HTTP runner and durable audience,
// with a real upstream dangerous-command approval and native continuation.
func supervisorApprovalSmoke(ctx context.Context, m *supervisor.Manager, binding supervisor.Binding, runner communication.HTTPRunner, spool *communication.Spool, template communication.Job, runtimeAuth string) error {
	policyBefore, err := os.ReadFile(filepath.Join(binding.ContextRoot, "settings.yaml"))
	if err != nil {
		return err
	}
	for _, expire := range []bool{false, true} {
		job := template
		job.ID = fmt.Sprintf("real-approval-%t", expire)
		job.IdempotencyKey, job.Text = job.ID, "approval probe"
		if _, err := spool.Enqueue(job); err != nil {
			return err
		}
		runner.Resume = false
		streamCtx, cancel := context.WithCancel(ctx)
		type result struct {
			outcome communication.RunOutcome
			err     error
		}
		done := make(chan result, 1)
		go func() { outcome, err := runner.RunOutcome(streamCtx, job); done <- result{outcome, err} }()
		var mapping communication.JobMapping
		deadline := time.Now().Add(40 * time.Second)
		for time.Now().Before(deadline) {
			current, _, err := spool.Mapping(job.ID)
			if err != nil {
				cancel()
				return err
			}
			if current.ApprovalID != "" && current.Status == "waiting_for_approval" {
				mapping = current
				break
			}
			select {
			case result := <-done:
				cancel()
				return fmt.Errorf("native approval never became actionable: %s %v", result.outcome.Status, result.err)
			default:
			}
			if err := contractSleep(ctx, 20*time.Millisecond); err != nil {
				cancel()
				return err
			}
		}
		if mapping.ApprovalID == "" {
			cancel()
			return fmt.Errorf("native approval missing")
		}
		if err := m.Reap(ctx, time.Now().Add(10*time.Second)); err != nil {
			cancel()
			return err
		}
		if runtime, _, err := m.Status(binding); err != nil || runtime.State != supervisor.Busy || runtime.Leases == 0 {
			cancel()
			return fmt.Errorf("actionable native approval lost its hold: %v", err)
		}
		control := hubruntime.RunControl{RunReference: hubruntime.RunReference{ExecuteRequest: hubruntime.ExecuteRequest{Envelope: job.Envelope, JobID: job.ID, OrganizationID: job.OrganizationID, UserID: job.UserID, ActorID: job.ActorID, ScopeID: job.ScopeID, Channel: job.Channel, Trigger: job.Trigger, IdempotencyKey: job.IdempotencyKey}, RunID: mapping.RunID, SessionID: mapping.SessionID, RuntimeGeneration: mapping.RuntimeGeneration}, Action: "approve", RequestID: mapping.ApprovalID, Choice: "once"}
		call := func(request hubruntime.RunControl) (int, error) {
			body, _ := json.Marshal(request)
			r, err := http.NewRequestWithContext(ctx, http.MethodPost, runner.URL+"/v1/control", bytes.NewReader(body))
			if err != nil {
				return 0, err
			}
			r.Header.Set("Authorization", "Bearer "+runner.Auth)
			response, err := http.DefaultClient.Do(r)
			if err != nil {
				return 0, err
			}
			defer response.Body.Close()
			return response.StatusCode, nil
		}
		other := job.Envelope
		other.ConversationID = "other"
		if err := spool.RequestApproval(job.ID, other, mapping.ApprovalID, "once"); err == nil {
			cancel()
			return fmt.Errorf("native approval accepted another conversation")
		}
		bad := control
		bad.RuntimeGeneration = "obsolete"
		if code, err := call(bad); err != nil || code != 409 {
			cancel()
			return fmt.Errorf("native stale-generation approval accepted: %d %v", code, err)
		}
		if expire {
			// Exercise the expiry sweep's explicit clock at the saved deadline.
			m.ExpireApprovals(ctx, mapping.ApprovalDeadline.Add(time.Second))
			m.ExpireApprovals(ctx, mapping.ApprovalDeadline.Add(time.Second))
			if code, err := call(control); err != nil || code != 409 {
				cancel()
				return fmt.Errorf("late native approval accepted: %d %v", code, err)
			}
		} else {
			if err := spool.RequestApproval(job.ID, job.Envelope, mapping.ApprovalID, "once"); err != nil {
				cancel()
				return err
			}
			communication.ProcessControlProbe(ctx, spool, communication.Config{RuntimeURL: runner.URL, RuntimeAuth: runner.Auth})
			decided, _, err := spool.Mapping(job.ID)
			if err != nil || decided.Control == nil || (decided.Control.State != "applied" && decided.Control.State != "closed") {
				cancel()
				return fmt.Errorf("native durable approval dispatch failed: %v", err)
			}
			if code, err := call(control); err != nil || code != 200 {
				cancel()
				return fmt.Errorf("native approval duplicate failed: %d %v", code, err)
			}
		}
		select {
		case result := <-done:
			if (!expire && (result.err != nil || result.outcome.Status != "completed" || result.outcome.Text != "contract final answer")) || (expire && result.outcome.Status != "cancelled") {
				cancel()
				reason := "unavailable"
				if runtime, _, err := m.Status(binding); err == nil {
					if id, err := smokeOwnedContainerID(ctx, m, runtime); err == nil {
						out := func(args ...string) ([]byte, error) { return dockerContractOutput(ctx, args...) }
						if native, err := hermesHTTP(ctx, out, id, runtimeAuth, http.MethodGet, "/v1/runs/"+mapping.RunID, "", ""); err == nil {
							reason, _ = jsonString(native.body, "error")
						}
					}
				}
				return fmt.Errorf("native approval terminal mismatch: expiry=%t status=%s native=%s err=%v", expire, result.outcome.Status, reason, result.err)
			}
		case <-time.After(30 * time.Second):
			cancel()
			return fmt.Errorf("native approval did not settle")
		}
		cancel()
		if runtime, _, err := m.Status(binding); err != nil || runtime.Leases != 0 {
			return fmt.Errorf("settled native approval retained runtime holds: expiry=%t err=%v", expire, err)
		}
		approvals := 0
		for range 16 {
			delivery, err := spool.ClaimDelivery()
			if err != nil {
				return err
			}
			if delivery == nil {
				break
			}
			if delivery.ChatID != job.ChatID || strings.Contains(delivery.Text, "HUB_TOOL_SECRET_CANARY") {
				return fmt.Errorf("native approval escaped original private audience")
			}
			if strings.Contains(delivery.Text, mapping.ApprovalID) {
				approvals++
			}
			if err := spool.CompleteDelivery(delivery.ID); err != nil {
				return err
			}
		}
		if approvals != 1 {
			return fmt.Errorf("native approval audience count=%d", approvals)
		}
	}
	fmt.Println("Real native approval passed: original audience, once/duplicate, generation fence, idle hold, expiry and late rejection")
	policyAfter, err := os.ReadFile(filepath.Join(binding.ContextRoot, "settings.yaml"))
	if err != nil || !bytes.Equal(policyBefore, policyAfter) {
		return fmt.Errorf("native approval changed host authorization policy")
	}
	return nil
}

func smokeOwnedContainerID(ctx context.Context, m *supervisor.Manager, runtime supervisor.Runtime) (string, error) {
	raw, err := exec.CommandContext(ctx, "docker", "inspect", "--format", "{{.Id}}", runtime.Container).Output()
	id := strings.TrimSpace(string(raw))
	if err != nil || len(id) != 64 {
		return "", fmt.Errorf("smoke container ID unavailable")
	}
	if _, err := hex.DecodeString(id); err != nil {
		return "", err
	}
	runtime.Container = id
	if err := m.VerifyOwnership(ctx, runtime); err != nil {
		return "", err
	}
	return id, nil
}
