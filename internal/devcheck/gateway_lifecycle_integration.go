//go:build integration

package devcheck

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"github.com/letya999/hermes-hub/internal/toolhub/api"
	"io/fs"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/letya999/hermes-hub/internal/catalog"
	"github.com/letya999/hermes-hub/internal/communication"
	"github.com/letya999/hermes-hub/internal/identity"
	"github.com/letya999/hermes-hub/internal/migration"
	"github.com/letya999/hermes-hub/internal/passport"
	"github.com/letya999/hermes-hub/internal/stack"
	"github.com/letya999/hermes-hub/internal/supervisor"
)

// gatewayLifecycleSmoke uses the production Telegram adapter, worker, spool,
// authenticated supervisor HTTP server/reaper and actual pinned Hermes containers.
// Telegram, the model endpoint and ToolHub are fixtures; no external send occurs.
// This gate does not prove the integrated memory/automation boundary of M32.
func GatewayLifecycle(ctx context.Context, image string) error {
	provider, providerURL, err := nativeContractProvider()
	if err != nil {
		return err
	}
	defer func() { provider.CloseClientConnections(); provider.Close() }()
	return gatewayLifecycleSmoke(ctx, image, providerURL)
}

func gatewayLifecycleSmoke(ctx context.Context, image, providerURL string) error {
	// Three cold starts each allow four-minute readiness plus one minute of execution.
	const coldStartBudget = 5 * time.Minute
	ctx, cancel := context.WithTimeout(ctx, 25*time.Minute)
	defer cancel()
	root, err := os.MkdirTemp("", "hermes-gateway-lifecycle-")
	if err != nil {
		return err
	}
	defer os.RemoveAll(root)
	if err := os.MkdirAll(filepath.Join(root, "config"), 0700); err != nil {
		return err
	}
	if err := os.WriteFile(filepath.Join(root, "config", "SOUL.md"), []byte("# gateway lifecycle canary\n"), 0644); err != nil {
		return err
	}
	suffix := strconv.FormatInt(time.Now().UnixNano(), 36)
	network := "hermes-gateway-" + suffix
	if err := exec.CommandContext(ctx, "docker", "network", "create", network).Run(); err != nil {
		return err
	}
	defer exec.Command("docker", "network", "rm", network).Run()
	defer func() {
		for _, user := range []string{"alice-" + suffix, "bob-" + suffix} {
			_ = exec.Command("docker", "rm", "-f", "hermes-gateway-toolhub-"+user).Run()
			_ = exec.Command("docker", "rm", "-f", "hermes-gateway-model-"+user).Run()
			_ = exec.Command("docker", "network", "rm", "hermes-hub-agent-"+user+"-prod").Run()
		}
	}()
	control := "synthetic-gateway-control-0123456789"
	// The deployment default warm window is now 30 min (SPEC-0053 S2), far
	// beyond this check's budget, so the fixture pins its own five-minute
	// window and verifies the extension+reap mechanics at that value.
	const warmWindow = 5 * time.Minute
	cfg := supervisor.Config{SpacesRoot: root, Image: image, Network: network, RuntimeAuth: control, PortBase: 32000, WarmTTL: warmWindow}
	cfg.Command = func(callCtx context.Context, args ...string) ([]byte, error) {
		if len(args) != 0 && args[0] == "network" {
			for i, arg := range args {
				if arg == "hermes-hub-control" {
					args[i] = network
				}
			}
		}
		return exec.CommandContext(callCtx, "docker", args...).CombinedOutput()
	}
	manager, err := supervisor.New(cfg)
	if err != nil {
		return err
	}
	users := make([]communication.User, 0, 2)
	bindings := make([]supervisor.Binding, 0, 2)
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
	for i, name := range []string{"alice", "bob"} {
		name = name + "-" + suffix
		dir := filepath.Join(root, name)
		agentNet := "hermes-hub-agent-" + name + "-prod"
		if err := exec.CommandContext(ctx, "docker", "network", "create", "--internal", agentNet).Run(); err != nil {
			return err
		}
		if err := stack.InitEnvironment(dir, name, "prod"); err != nil {
			return err
		}
		for passportName, body := range map[string]string{
			"stack":  "identity: {user: " + name + ", infra: true}\nservices:\n  hermes-runtime: {mode: supervised}\n  model-relay: {mode: shared}\n  cliproxy: {mode: shared}\n  toolhub: {mode: shared}\n  credential-broker: {mode: shared}\n  communication-hub: {mode: shared}\n  supervisor: {mode: shared}\n",
			"config": "runtime: {timezone: UTC}\nmemory: {enabled: false}\nchannels:\n  telegram: {bot: {ref: telegram-bot-contract}}\n",
			"models": "default: gpt-4o-mini\nroutes: {gpt-4o-mini: {ladder: [upstream]}}\nservices:\n  upstream: {via: openai-compatible, endpoint: " + providerURL + ", models: [{id: gpt-4o-mini}]}\n",
			"tools":  "binding: {profile_id: " + name + "-default, generation: 1}\ncore:\n  files: {via: toolhub}\nextended:\n  'native:terminal': {via: native}\n",
		} {
			if err := os.WriteFile(filepath.Join(dir, "passports", passportName+".yaml"), []byte(body), 0600); err != nil {
				return err
			}
		}
		catalogDocs, err := catalog.Generate()
		if err != nil {
			return err
		}
		revision, err := passport.CompileAuthored(dir, "prod", time.Now().UTC(), catalogDocs, stack.ReservedSiblingPorts(dir), map[string][]string{})
		if err != nil {
			return err
		}
		if err := revision.Publish(dir); err != nil {
			return err
		}
		initialSelection := stack.ExecutionSelection{Schema: 1, User: name, Environment: "prod", Mode: "supervisor", SupervisorURL: "http://supervisor:8876", NativeCron: "disabled", CompatibilityRelease: "0.2.0"}
		selectionBody, err := json.Marshal(initialSelection)
		if err != nil {
			return err
		}
		if err := os.WriteFile(filepath.Join(dir, stack.ExecutionPath("prod")), selectionBody, 0600); err != nil {
			return err
		}
		if err := os.WriteFile(filepath.Join(dir, "secrets.prod.env"), []byte("OPENAI_API_KEY=synthetic-gateway-key\nTELEGRAM_BOT_TOKEN=synthetic-bot\nTELEGRAM_ALLOWED_USERS="+strconv.Itoa(21+i)+"\n"), 0600); err != nil {
			return err
		}
		// The contract provider probes the real terminal tool, so this fixture
		// stands in for a migrated space whose operator re-admitted exec:
		// governance seeds native:terminal deny-by-default and a user-scope
		// allow is exactly the record the host writes to grant it back.
		if err := os.MkdirAll(filepath.Join(dir, "runtime", "toolhub"), 0700); err != nil {
			return err
		}
		if err := api.SaveGovernance(filepath.Join(dir, "runtime", "toolhub", "governance.json"), api.MigrationSeed(name, []string{"native:terminal"}, time.Now())); err != nil {
			return err
		}
		if err := stack.RenderEnvironment(dir, root, "prod"); err != nil {
			return err
		}
		if err := os.WriteFile(filepath.Join(dir, "managed-runtime.prod.env"), []byte("OPENAI_API_KEY=synthetic-gateway-key\nHUB_TOOLHUB_ENDPOINT=http://toolhub:8090/mcp\n"), 0600); err != nil {
			return err
		}
		if err := dockerContract(ctx, "run", "-d", "--name", "hermes-gateway-toolhub-"+name, "--network", agentNet, "--network-alias", "toolhub", "--entrypoint", "python", image, "-c", toolhubFixture); err != nil {
			return fmt.Errorf("gateway synthetic ToolHub: %w", err)
		}
		if err := dockerContract(ctx, "run", "-d", "--name", "hermes-gateway-model-"+name, "--network", agentNet, "--network-alias", "model-relay", "--add-host", "host.docker.internal:host-gateway", "-e", "HUB_MODEL_RELAY_MODEL=gpt-4o-mini", "-e", "HUB_MODEL_RELAY_UPSTREAMS=fixture="+providerURL, "--entrypoint", "hub-runtime", image, "model-relay"); err != nil {
			return fmt.Errorf("gateway synthetic model relay: %w", err)
		}
		if err := dockerContract(ctx, "network", "connect", network, "hermes-gateway-model-"+name); err != nil {
			return err
		}
		// Synthetic fixture directories must be writable by container uid10001 on Linux.
		if err := filepath.WalkDir(dir, func(path string, entry fs.DirEntry, err error) error {
			if err != nil {
				return err
			}
			if entry.IsDir() {
				return os.Chmod(path, 0777)
			}
			return nil
		}); err != nil {
			return err
		}

		settingsValue, err := stack.ReadEffective(dir, "prod")
		if err != nil {
			return err
		}
		policy := stack.PolicyVersion(settingsValue)
		users = append(users, communication.User{ID: name, RuntimeID: name, PolicyVersion: policy, Enabled: true, TelegramIDs: []int64{int64(21 + i)}, Features: []string{"workspace", "telegram"}})
		bindings = append(bindings, supervisor.Binding{PrincipalID: name, ContextID: name, RuntimeID: name, RuntimeMode: "gateway", UserID: name, OrganizationID: "personal", PolicyVersion: policy, ContextRoot: dir})
		if err := os.WriteFile(filepath.Join(dir, "managed", "prod", "hermes", "isolation-canary"), []byte(name), 0644); err != nil {
			return err
		}
	}
	defer func() {
		cleanupCtx, stop := context.WithTimeout(context.Background(), 30*time.Second)
		defer stop()
		for _, binding := range bindings {
			if runtime, ok, err := manager.Status(binding); err == nil && ok && runtime.State != supervisor.Stopped {
				if id, err := smokeOwnedContainerID(cleanupCtx, manager, runtime); err == nil {
					_ = exec.CommandContext(cleanupCtx, "docker", "rm", "-f", id).Run()
					_ = exec.CommandContext(cleanupCtx, "docker", "rm", "-f", runtime.Container+"-ctl").Run()
				}
			}
		}
	}()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return err
	}
	address := listener.Addr().String()
	listener.Close()
	supervisorCtx, stopSupervisor := context.WithCancel(ctx)
	defer stopSupervisor()
	supervisorDone := make(chan error, 1)
	go func() { supervisorDone <- supervisor.Serve(supervisorCtx, manager, address) }()
	endpoint := "http://" + address
	var mu sync.Mutex
	updates := []communication.Update{}
	finals := map[int64]int{}
	api := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/botsynthetic-bot/getUpdates":
			var poll struct {
				Offset int64 `json:"offset"`
			}
			if json.NewDecoder(r.Body).Decode(&poll) != nil {
				w.WriteHeader(400)
				return
			}
			mu.Lock()
			batch := []communication.Update{}
			for _, update := range updates {
				if int64(update.UpdateID) >= poll.Offset {
					batch = append(batch, update)
				}
			}
			mu.Unlock()
			if len(batch) == 0 {
				_ = contractSleep(r.Context(), 200*time.Millisecond)
			}
			_ = json.NewEncoder(w).Encode(map[string]any{"ok": true, "result": batch})
		case "/botsynthetic-bot/sendMessage":
			var message struct {
				ChatID int64  `json:"chat_id"`
				Text   string `json:"text"`
			}
			if json.NewDecoder(r.Body).Decode(&message) != nil {
				w.WriteHeader(400)
				return
			}
			if message.Text == "contract final answer" {
				mu.Lock()
				finals[message.ChatID]++
				mu.Unlock()
			}
			_ = json.NewEncoder(w).Encode(map[string]any{"ok": true, "result": map[string]any{"message_id": 1}})
		default:
			w.WriteHeader(404)
		}
	}))
	defer api.Close()
	spoolDir := filepath.Join(root, "gateway-spool")
	// Persist the initial queue before rollout. It must execute once after migration.
	spool, err := communication.NewSpool(spoolDir)
	if err != nil {
		return err
	}
	queued := communication.Job{Envelope: identity.TelegramEnvelope(users[0].ID, 21, users[0].RuntimeID, users[0].PolicyVersion), ID: "telegram-71001", IdempotencyKey: "telegram:71001", OrganizationID: "personal", UserID: users[0].ID, ActorID: users[0].ID, ScopeID: "user:" + users[0].ID, Channel: "telegram_bot", Trigger: "message", ChatID: 21, MessageID: 71001, Text: "contract probe", CreatedAt: time.Now().UTC()}
	if _, err := spool.Enqueue(queued); err != nil {
		return err
	}
	for _, binding := range bindings {
		audit, err := migration.AuditExecutionSpool(spoolDir, binding.UserID)
		if err != nil {
			return err
		}
		snapshot, err := migration.InspectNativeCron(ctx, binding.ContextRoot, image, func(callCtx context.Context, args ...string) ([]byte, error) {
			return exec.CommandContext(callCtx, "docker", args...).Output()
		})
		if err != nil {
			return err
		}
		audit.NativeCron = &snapshot
		selection := stack.ExecutionSelection{Schema: 1, User: binding.UserID, Environment: "prod", Mode: "supervisor", SupervisorURL: endpoint, NativeCron: "disabled", CompatibilityRelease: "0.2.0"}
		if _, err := migration.SelectExecution(binding.ContextRoot, selection, audit, true); err != nil {
			return err
		}
		// The host-side supervisor endpoint changes only execution routing;
		// the already rendered agent source and managed env are unchanged.

	}
	config := communication.Config{Supervised: true, OrganizationID: "personal", Users: users, TelegramToken: "synthetic-bot", APIBaseURL: api.URL, SpoolDir: spoolDir, RuntimeURL: endpoint, RuntimeAuth: control, PollTimeout: time.Second}
	gateway, err := communication.New(config)
	if err != nil {
		return err
	}
	gatewayCtx, stopGateway := context.WithCancel(ctx)
	gatewayDone := make(chan error, 1)
	go func() { gatewayDone <- gateway.Run(gatewayCtx) }()
	defer func() { stopGateway(); <-gatewayDone; stopSupervisor(); <-supervisorDone }()
	appendTask := func(updateID int, chatID int64) {
		update := communication.Update{UpdateID: updateID, Message: &communication.Message{MessageID: updateID, From: &communication.TGUser{ID: chatID}, Chat: communication.TGChat{ID: chatID, Type: "private"}, Text: "contract probe"}}
		mu.Lock()
		updates = append(updates, update, update)
		mu.Unlock()
	}
	wait := func(description string, limit time.Duration, predicate func() bool) error {
		deadline := time.Now().Add(limit)
		for time.Now().Before(deadline) {
			if predicate() {
				return nil
			}
			if err := contractSleep(ctx, 200*time.Millisecond); err != nil {
				return err
			}
		}
		return fmt.Errorf("gateway lifecycle: %s timed out", description)
	}
	readMapping := func(updateID int) (communication.JobMapping, error) {
		var mapping communication.JobMapping
		// A delivered reply can precede durable terminal settlement in the worker.
		err := wait("durable terminal mapping", 10*time.Second, func() bool {
			body, err := os.ReadFile(filepath.Join(spoolDir, "mappings", "telegram-"+strconv.Itoa(updateID)+".json"))
			return err == nil && json.Unmarshal(body, &mapping) == nil && mapping.Status == "completed"
		})
		return mapping, err
	}
	appendTask(71001, 21)
	if err := wait("Alice cold start and final", coldStartBudget, func() bool { mu.Lock(); defer mu.Unlock(); return finals[21] == 1 }); err != nil {
		runtime, _, _ := manager.Status(bindings[0])
		return fmt.Errorf("%w: runtime=%s", err, runtime.State)
	}
	first, err := readMapping(71001)
	if err != nil || first.Status != "completed" || first.ContextID != users[0].ID || first.RuntimeID != users[0].ID || first.RunID == "" {
		return errors.New("Alice gateway mapping incorrect")
	}
	if runtime, ok, err := manager.Status(bindings[1]); err != nil || (ok && runtime.State != supervisor.Stopped) {
		return errors.New("Alice task woke Bob")
	}
	appendTask(71002, 22)
	if err := wait("Bob isolated cold start and final", coldStartBudget, func() bool { mu.Lock(); defer mu.Unlock(); return finals[22] == 1 }); err != nil {
		return err
	}
	bob, err := readMapping(71002)
	if err != nil || bob.ContextID != users[1].ID || bob.RuntimeID != users[1].ID || bob.SessionID == first.SessionID || bob.RunID == first.RunID {
		return errors.New("Bob gateway mapping crossed Alice")
	}
	idleDeadlines := map[string]time.Time{}
	for i, binding := range bindings {
		if err := wait("terminal lease settlement", 10*time.Second, func() bool {
			runtime, ok, err := manager.Status(binding)
			return err == nil && ok && len(runtime.Consumers) == 0 && runtime.State == supervisor.Idle
		}); err != nil {
			runtime, _, _ := manager.Status(binding)
			return fmt.Errorf("%w (last observed leases=%d state=%s)", err, len(runtime.Consumers), runtime.State)
		}
		runtime, ok, err := manager.Status(binding)
		if err != nil || !ok || len(runtime.Consumers) != 0 || runtime.State != supervisor.Idle {
			return errors.New("completed gateway job did not release leases")
		}
		idleDeadlines[binding.UserID] = runtime.IdleDeadline
		id, err := smokeOwnedContainerID(ctx, manager, runtime)
		if err != nil {
			return err
		}
		body, err := exec.CommandContext(ctx, "docker", "exec", id, "cat", "/state/hermes/isolation-canary").Output()
		if err != nil || strings.TrimSpace(string(body)) != users[i].ID {
			return errors.New("gateway runtime mounted another user's home")
		}
	}
	// Wait real wall time, then a second Alice message must extend the warm deadline.
	if err := contractSleep(ctx, 5*time.Second); err != nil {
		return err
	}
	before, _, _ := manager.Status(bindings[0])
	appendTask(71003, 21)
	if err := wait("Alice warm reuse and second final", 2*time.Minute, func() bool { mu.Lock(); defer mu.Unlock(); return finals[21] == 2 }); err != nil {
		return err
	}
	second, err := readMapping(71003)
	if err != nil || second.SessionID != first.SessionID || second.RunID == first.RunID || second.RuntimeGeneration != first.RuntimeGeneration {
		return errors.New("warm gateway task lost session/generation or reused the run")
	}
	var after supervisor.Runtime
	if err := wait("warm terminal lease settlement and extended deadline", 10*time.Second, func() bool {
		var ok bool
		after, ok, err = manager.Status(bindings[0])
		return err == nil && ok && len(after.Consumers) == 0 && after.State == supervisor.Idle &&
			after.IdleDeadline.After(before.IdleDeadline) && after.IdleDeadline.Sub(after.LastUsed) >= warmWindow-time.Second
	}); err != nil {
		return fmt.Errorf("%w: state=%s leases=%d previous=%s deadline=%s last_used=%s", err, after.State, len(after.Consumers), before.IdleDeadline, after.IdleDeadline, after.LastUsed)
	}
	idleDeadlines[bindings[0].UserID] = after.IdleDeadline
	fmt.Println("Real gateway passed: two authenticated users, exact homes, duplicate updates once, warm session reuse and released leases; waiting actual five-minute idle window")
	earlyShutdown := false
	if err := wait("automatic five-minute idle shutdown", 6*time.Minute, func() bool {
		allStopped := true
		for _, binding := range bindings {
			runtime, ok, err := manager.Status(binding)
			if err != nil || !ok || runtime.State != supervisor.Stopped || len(runtime.Consumers) != 0 {
				allStopped = false
			} else if time.Now().Before(idleDeadlines[binding.UserID]) {
				earlyShutdown = true
				return true
			}
		}
		return allStopped
	}); err != nil {
		detail := ""
		for _, binding := range bindings {
			if runtime, ok, statusErr := manager.Status(binding); statusErr == nil && ok {
				detail += fmt.Sprintf("; %s state=%s leases=%d health=%s ownership=%s desired=%v deadline=%s", binding.UserID, runtime.State, len(runtime.Consumers), runtime.RuntimeHealth, runtime.Ownership, runtime.Desired, runtime.IdleDeadline)
			}
		}
		return fmt.Errorf("%w%s", err, detail)
	}
	if earlyShutdown {
		return errors.New("runtime stopped before its real five-minute idle deadline")
	}
	mu.Lock()
	a, b := finals[21], finals[22]
	mu.Unlock()
	if a != 2 || b != 1 {
		return fmt.Errorf("duplicate final count Alice=%d Bob=%d", a, b)
	}
	// Cold resume retains the session and context home; completed input is not replayed.
	appendTask(71004, 21)
	if err := wait("Alice cold restore after automatic shutdown", coldStartBudget, func() bool { mu.Lock(); defer mu.Unlock(); return finals[21] == 3 }); err != nil {
		return err
	}
	cold, err := readMapping(71004)
	if err != nil || cold.SessionID != first.SessionID || cold.RuntimeGeneration == second.RuntimeGeneration || cold.RunID == second.RunID {
		return errors.New("cold restore lost persistent session or run identity")
	}
	fmt.Println("Real gateway five-minute lifecycle passed: automatic graceful shutdown, no idle compute, original session cold restore; no external Telegram/provider account exercised")
	return nil
}
