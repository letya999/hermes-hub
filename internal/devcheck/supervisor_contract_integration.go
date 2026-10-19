//go:build integration

package devcheck

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/json"
	"errors"
	"fmt"
	"github.com/letya999/hermes-hub/internal/communication"
	"github.com/letya999/hermes-hub/internal/identity"
	hubruntime "github.com/letya999/hermes-hub/internal/runtime"
	"github.com/letya999/hermes-hub/internal/stack"
	"github.com/letya999/hermes-hub/internal/supervisor"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

// SupervisorContract uses its own provider so it can run beside the native and gateway gates.
func SupervisorContract(ctx context.Context, image string) error {
	provider, url, err := nativeContractProvider()
	if err != nil {
		return err
	}
	defer func() { provider.CloseClientConnections(); provider.Close() }()
	return supervisorSmoke(ctx, image, url)
}

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
	cleanup := func() {
		_ = exec.Command("docker", "network", "rm", network).Run()
		_ = exec.Command("docker", "volume", "rm", "hermes-hub-"+userID+"-prod_broker-secrets-runtime").Run()
	}
	defer cleanup()
	if err := exec.CommandContext(ctx, "docker", "network", "create", network).Run(); err != nil {
		return err
	}
	// Layout mirrors production: <root>/spaces/<user> so that
	// Dir(Dir(ContextRoot)) resolves to this root and hub plugin materialization
	// finds root/config/plugins — the web feature exercises the real mount.
	contextRoot := filepath.Join(root, "spaces", userID)
	for _, name := range []string{"runtime", "hermes", "workspace", "connections", "connections/google", "connections/telegram", "connections/browser", "home", "cache", "archive"} {
		if err := os.MkdirAll(filepath.Join(contextRoot, name), 0777); err != nil {
			return err
		}
	}
	pluginFixture := filepath.Join(root, "config", "plugins", "hub-web")
	for rel, body := range map[string]string{
		"plugin.yaml": "name: hub-web\nkind: backend\nversion: '1'\n",
		"__init__.py": "def register(ctx):\n    pass\n",
	} {
		if err := os.MkdirAll(pluginFixture, 0777); err != nil {
			return err
		}
		if err := os.WriteFile(filepath.Join(pluginFixture, rel), []byte(body), 0644); err != nil {
			return err
		}
	}
	auth := "context-auth-0123456789abcdef0123456789abcdef"
	if err := os.WriteFile(filepath.Join(contextRoot, "settings.yaml"), []byte("schema: 1\nuser: "+userID+"\ntimezone: UTC\nbrowser_port: 9222\nfeatures: [workspace, web]\n"), 0600); err != nil {
		return err
	}
	settings, err := stack.ReadEnvironment(contextRoot, "prod")
	if err != nil {
		return err
	}
	settings.DefaultGlobalSkillsDir(filepath.Join(root, "config", "skills"))
	if err := os.MkdirAll(settings.GlobalSkillsDir, 0755); err != nil {
		return err
	}
	policy := stack.PolicyVersion(settings)
	if err := os.WriteFile(filepath.Join(contextRoot, "runtime.auth"), []byte("HUB_RUNTIME_AUTH="+auth+"\nOPENAI_API_KEY=probe-openai-key-0123456789\nOPENAI_BASE_URL="+providerURL+"\n"), 0600); err != nil {
		return err
	}
	if err := os.WriteFile(filepath.Join(contextRoot, "runtime.prod.env"), []byte("OPENAI_API_KEY=probe-openai-key-0123456789\n"), 0600); err != nil {
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
	cfg := supervisor.Config{SpacesRoot: filepath.Join(root, "spaces"), Image: image, Network: network, RuntimeAuth: control, WarmTTL: time.Second, PortBase: 28000}
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
	if err := smokeWebPluginIsolation(ctx, m, cold, contextRoot); err != nil {
		return err
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
	// Exercise metadata-only recovery against pinned Hermes, after completion.
	// A constant run ID alone would miss a duplicate execution: count real tools.
	current, _, err := m.Status(binding)
	if err != nil {
		return err
	}
	readmitContainerID, err := smokeOwnedContainerID(ctx, m, current)
	if err != nil {
		return err
	}
	countEffects := func() (string, error) {
		data, err := exec.CommandContext(ctx, "docker", "exec", readmitContainerID, "cat", "/workspace/contract-executions").Output()
		return string(data), err
	}
	if effects, err := countEffects(); err != nil || effects != "1\n" {
		return fmt.Errorf("readmit initial tool count: %q err=%v", effects, err)
	}
	var ledger struct {
		Jobs map[string]struct {
			Request hubruntime.ExecuteRequest `json:"request"`
		} `json:"jobs"`
	}
	raw, err := os.ReadFile(filepath.Join(cfg.SpacesRoot, ".control", "supervisor.json"))
	if err != nil {
		return err
	}
	if err := json.Unmarshal(raw, &ledger); err != nil {
		return err
	}
	replayRequest := ledger.Jobs[job.ID].Request
	if replayRequest.JobID != job.ID || replayRequest.Text != "" {
		return errors.New("readmit fixture needs the durable metadata-only request")
	}
	body, err := json.Marshal(replayRequest)
	if err != nil {
		return err
	}
	replayCall, err := http.NewRequestWithContext(ctx, http.MethodPost, current.Address+"/v1/readmit", bytes.NewReader(body))
	if err != nil {
		return err
	}
	replayCall.Header.Set("Authorization", "Bearer "+auth)
	replayCall.Header.Set("Content-Type", "application/json")
	replayResponse, err := http.DefaultClient.Do(replayCall)
	if err != nil {
		return err
	}
	var replayOutcome hubruntime.ExecuteResponse
	err = json.NewDecoder(replayResponse.Body).Decode(&replayOutcome)
	replayResponse.Body.Close()
	readmitMapping, _, mappingErr := spool.Mapping(job.ID)
	if err != nil || mappingErr != nil || replayResponse.StatusCode != http.StatusOK || replayOutcome.Status != "completed" || replayOutcome.RunID != readmitMapping.RunID || replayOutcome.SessionID != readmitMapping.SessionID {
		return fmt.Errorf("real metadata-only readmit: HTTP %d status=%s same_run=%v error=%v", replayResponse.StatusCode, replayOutcome.Status, replayOutcome.RunID == readmitMapping.RunID, err)
	}
	if err := contractSleep(ctx, 2*time.Second); err != nil {
		return err
	}
	if effects, err := countEffects(); err != nil || effects != "1\n" {
		return fmt.Errorf("readmit repeated side effect: %q err=%v", effects, err)
	}
	fmt.Println("Real readmit passed: original completed run/session, exactly one tool side effect")
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

type smokeMount struct {
	Type        string
	Source      string
	Destination string
	RW          bool
}

// smokeBindSource normalizes the Source Docker reports for a bind mount:
// Docker Desktop sometimes hands back the translated Linux view
// (/run/desktop/mnt/host/c/...) instead of the Windows path verbatim.
func smokeBindSource(src string) string {
	s := filepath.ToSlash(src)
	if rest, ok := strings.CutPrefix(s, "/run/desktop/mnt/host/"); ok && len(rest) > 2 && rest[1] == '/' {
		s = strings.ToUpper(rest[:1]) + ":" + rest[1:]
	}
	return s
}
