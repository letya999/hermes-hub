//go:build integration

package devcheck

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/json"
	"errors"
	"fmt"
	"github.com/letya999/hermes-hub/internal/catalog"
	"github.com/letya999/hermes-hub/internal/commodel"
	"github.com/letya999/hermes-hub/internal/communication"
	"github.com/letya999/hermes-hub/internal/identity"
	"github.com/letya999/hermes-hub/internal/passport"
	hubruntime "github.com/letya999/hermes-hub/internal/runtime"
	"github.com/letya999/hermes-hub/internal/stack"
	"github.com/letya999/hermes-hub/internal/supervisor"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"gopkg.in/yaml.v3"
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
	agentNet := "hermes-hub-agent-" + userID + "-prod"
	root, err := os.MkdirTemp("", "hermes-supervisor-smoke-")
	if err != nil {
		return err
	}
	defer os.RemoveAll(root)
	cleanup := func() {
		_ = exec.Command("docker", "rm", "-f", network+"-model", network+"-toolhub").Run()
		_ = exec.Command("docker", "network", "rm", agentNet).Run()
		_ = exec.Command("docker", "network", "rm", network).Run()
		_ = exec.Command("docker", "volume", "rm", "hermes-hub-"+userID+"-prod_broker-secrets-runtime").Run()
	}
	defer cleanup()
	if err := exec.CommandContext(ctx, "docker", "network", "create", network).Run(); err != nil {
		return err
	}
	if err := exec.CommandContext(ctx, "docker", "network", "create", "--internal", agentNet).Run(); err != nil {
		return err
	}
	const toolhubFixture = `import json
from http.server import BaseHTTPRequestHandler,HTTPServer
class H(BaseHTTPRequestHandler):
 def do_POST(self):
  request=json.loads(self.rfile.read(int(self.headers.get('Content-Length') or 0)) or b'{}')
  method=request.get('method','')
  result={'protocolVersion':'2025-03-26','capabilities':{'tools':{}},'serverInfo':{'name':'toolhub-stub','version':'0.1'}} if method=='initialize' else {'tools':[]} if method=='tools/list' else {}
  body=json.dumps({'jsonrpc':'2.0','id':request.get('id'),'result':result}).encode()
  self.send_response(200); self.send_header('Content-Type','application/json'); self.end_headers(); self.wfile.write(body)
 def do_GET(self): self.send_response(200); self.end_headers()
 def log_message(self,*args): pass
HTTPServer(('0.0.0.0',8090),H).serve_forever()`
	if err := dockerContract(ctx, "run", "-d", "--name", network+"-toolhub", "--network", agentNet, "--network-alias", "toolhub", "--entrypoint", "python", image, "-c", toolhubFixture); err != nil {
		return fmt.Errorf("start synthetic ToolHub: %w", err)
	}
	if err := dockerContract(ctx, "run", "-d", "--name", network+"-model", "--network", agentNet, "--network-alias", "model-relay", "--add-host", "host.docker.internal:host-gateway", "-e", "HUB_MODEL_RELAY_MODEL=gpt-4o-mini", "-e", "HUB_MODEL_RELAY_UPSTREAMS=fixture="+providerURL, "--entrypoint", "hub-runtime", image, "model-relay"); err != nil {
		return fmt.Errorf("start synthetic model relay: %w", err)
	}
	if err := dockerContract(ctx, "network", "connect", network, network+"-model"); err != nil {
		return fmt.Errorf("connect synthetic model relay: %w", err)
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
	if err := os.WriteFile(filepath.Join(root, "config", "SOUL.md"), []byte("# synthetic supervisor persona\n"), 0644); err != nil {
		return err
	}
	auth := "context-auth-0123456789abcdef0123456789abcdef"
	if err := stack.InitEnvironment(contextRoot, userID, "prod"); err != nil {
		return err
	}
	for name, body := range map[string]string{
		"stack":  "identity: {user: " + userID + ", infra: true}\nservices:\n  hermes-runtime: {mode: supervised}\n  model-relay: {mode: shared}\n  cliproxy: {mode: shared}\n  toolhub: {mode: shared}\n  credential-broker: {mode: shared}\n  communication-hub: {mode: shared}\n  supervisor: {mode: shared}\n",
		"config": "runtime: {timezone: UTC}\nmemory: {enabled: false}\nchannels:\n  telegram: {bot: {ref: telegram-bot-contract}}\n",
		"models": "default: gpt-4o-mini\nroutes: {gpt-4o-mini: {ladder: [upstream]}}\nservices:\n  upstream: {via: openai-compatible, endpoint: " + providerURL + ", models: [{id: gpt-4o-mini}]}\n",
		"tools":  "binding: {profile_id: " + userID + "-default, generation: 1}\ncore:\n  files: {via: toolhub}\n  web: {via: native}\nextended:\n  'native:terminal': {via: native}\n",
	} {
		if err := os.WriteFile(filepath.Join(contextRoot, "passports", name+".yaml"), []byte(body), 0600); err != nil {
			return err
		}
	}
	catalogDocs, err := catalog.Generate()
	if err != nil {
		return err
	}
	revision, err := passport.CompileAuthored(contextRoot, "prod", time.Now().UTC(), catalogDocs, stack.ReservedSiblingPorts(contextRoot), map[string][]string{})
	if err != nil {
		return err
	}
	if err := publishGenerated(contextRoot, revision); err != nil {
		return err
	}
	if err := stack.RenderEnvironment(context.Background(), generatedReader{contextRoot}, contextRoot, root, "prod"); err != nil {
		return err
	}
	checkTerminal := func(path string) error {
		body, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		var config struct {
			PlatformToolsets map[string][]string `yaml:"platform_toolsets"`
			Agent            struct {
				DisabledToolsets []string `yaml:"disabled_toolsets"`
			} `yaml:"agent"`
		}
		if err := yaml.Unmarshal(body, &config); err != nil {
			return err
		}
		if !slices.Contains(config.PlatformToolsets["api_server"], "terminal") {
			return fmt.Errorf("synthetic supervisor %s lacks selected terminal", filepath.Base(path))
		}
		if slices.Contains(config.Agent.DisabledToolsets, "terminal") {
			return fmt.Errorf("synthetic supervisor %s denies selected terminal", filepath.Base(path))
		}
		return nil
	}
	if err := checkTerminal(filepath.Join(contextRoot, "generated", "hermes.prod.yaml")); err != nil {
		return err
	}
	settings, err := stack.ReadEffective(context.Background(), generatedReader{contextRoot}, contextRoot, "prod")
	if err != nil {
		return err
	}
	if !slices.Contains(settings.NativeCarveouts(), "terminal") {
		return errors.New("synthetic supervisor passport lost native terminal carveout")
	}
	if agent, ok := stack.Config(settings)["agent"].(stack.M); ok {
		if disabled, ok := agent["disabled_toolsets"].([]string); ok && slices.Contains(disabled, "terminal") {
			return errors.New("synthetic supervisor passport still disables native terminal")
		}
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
	if err := os.WriteFile(filepath.Join(contextRoot, "managed-runtime.prod.env"), []byte("OPENAI_API_KEY=probe-openai-key-0123456789\nHUB_TOOLHUB_ENDPOINT=http://toolhub:8090/mcp\n"), 0600); err != nil {
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
	registry, err := integrationIdentity(ctx, root, []commodel.User{{ID: userID, Enabled: true, TelegramIDs: []int64{11}}})
	if err != nil {
		return err
	}
	defer registry.Close()
	cfg.Identity = registry
	var starts atomic.Int32
	cfg.Command = func(callCtx context.Context, args ...string) ([]byte, error) {
		if len(args) != 0 && args[0] == "network" {
			for i, arg := range args {
				if arg == "hermes-hub-control" {
					args[i] = network
				}
			}
		}
		if len(args) != 0 && args[0] == "run" && args[len(args)-1] == "serve" {
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
				_ = exec.CommandContext(cleanupCtx, "docker", "rm", "-f", runtime.Container+"-ctl").Run()
				_ = exec.CommandContext(cleanupCtx, "docker", "rm", "-f", id).Run()
			}
		}
	}()
	first, err := m.Ensure(ctx, binding)
	if err != nil {
		return err
	}
	if err := checkTerminal(filepath.Join(contextRoot, "generated", "hermes-effective.prod.yaml")); err != nil {
		return err
	}
	if id, err := smokeOwnedContainerID(ctx, m, first); err != nil {
		return err
	} else {
		probe := "from gateway.run import _load_gateway_config; from hermes_cli.tools_config import _get_platform_tools; from model_tools import get_tool_definitions; c=_load_gateway_config(); p=c.get('platform_toolsets',{}).get('api_server',[]); d=c.get('agent',{}).get('disabled_toolsets',[]); g=sorted(_get_platform_tools(c, 'api_server')); n=[t['function']['name'] for t in get_tool_definitions(enabled_toolsets=g, disabled_toolsets=d, quiet_mode=True)]; print(int('terminal' in p),int('terminal' in d),int('terminal' in g),int('terminal' in n))"
		out, err := dockerContractOutput(ctx, "exec", id, "python", "-c", probe)
		flags := strings.Fields(string(out))
		if err != nil || len(flags) != 4 || flags[0] != "1" || flags[1] != "0" || flags[2] != "1" || flags[3] != "1" {
			if len(flags) != 4 {
				return fmt.Errorf("synthetic supervisor Hermes loader probe failed: %w", err)
			}
			return fmt.Errorf("synthetic supervisor Hermes loader: configured=%t denied=%t selected=%t registered=%t err=%v", flags[0] == "1", flags[1] == "1", flags[2] == "1", flags[3] == "1", err)
		}
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
	receipts, err := os.ReadDir(filepath.Join(root, "probe-spool", "run-events"))
	if err != nil {
		return err
	}
	for _, receipt := range receipts {
		body, err := os.ReadFile(filepath.Join(root, "probe-spool", "run-events", receipt.Name()))
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
	if err != nil || len(before.Consumers) == 0 {
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
		mapping, _, _ := spool.Mapping(interruptedJob.ID)
		return fmt.Errorf("original run recovery across generation failed: interrupted=%t event_interrupted=%t run_same=%t generation_same=%t mapping_interrupted=%t mapping_event_interrupted=%t mapping_run_same=%t mapping_generation_same=%t uncertain=%t", recovered.Status == "interrupted", recovered.LastEvent == "run.interrupted", recovered.RunID == admitted.RunID, recovered.RuntimeGeneration == replacement.Generation, mapping.Status == "interrupted", mapping.LastKnownEvent == "run.interrupted", mapping.RunID == admitted.RunID, mapping.RuntimeGeneration == replacement.Generation, errors.Is(err, communication.ErrUncertain))
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
		if len(runtime.Consumers) == 0 && (runtime.State == supervisor.Idle || runtime.State == supervisor.Stopped) {
			return runtime, nil
		}
		if time.Now().After(deadline) {
			return runtime, fmt.Errorf("terminal lease did not settle: state=%s leases=%d", runtime.State, len(runtime.Consumers))
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
