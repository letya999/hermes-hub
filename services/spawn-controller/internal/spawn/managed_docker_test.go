package spawn_test

import (
	"bufio"
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	contract "github.com/letya999/hermes-hub/contracts/identity/v1"
	spawnv1 "github.com/letya999/hermes-hub/contracts/spawn/v1"
	"github.com/letya999/hermes-hub/internal/catalog"
	"github.com/letya999/hermes-hub/internal/identity"
	hubruntime "github.com/letya999/hermes-hub/internal/runtime"
	"github.com/letya999/hermes-hub/internal/stack"
	"github.com/letya999/hermes-hub/services/spawn-controller/internal/api"
	"github.com/letya999/hermes-hub/services/spawn-controller/internal/backend"
	"github.com/letya999/hermes-hub/services/spawn-controller/internal/spawn"
	"github.com/letya999/hermes-hub/services/spawn-controller/internal/spawn/migrations"
	"github.com/letya999/hermes-hub/shared/store/storetest"
)

// managedDockerRun runs one bounded docker CLI call; outputs stay small.
func managedDockerRun(ctx context.Context, args ...string) error {
	out, err := exec.CommandContext(ctx, "docker", args...).CombinedOutput()
	if err != nil {
		line := strings.TrimSpace(string(out))
		if cut := strings.IndexByte(line, '\n'); cut >= 0 {
			line = line[:cut]
		}
		if len(line) > 300 {
			line = line[:300]
		}
		return fmt.Errorf("docker %s: %w: %s", args[0], err, line)
	}
	return nil
}

// relayProbe starts one long-lived container on hermes-hub-control so
// every relay hop is a cheap docker exec instead of a fresh docker run —
// under a loaded daemon a create+rm pair can outlast the runtime's 10s
// run-authorization window, which made the streamed first message flaky.
func relayProbe(t *testing.T, image, name string) string {
	t.Helper()
	if err := managedDockerRun(t.Context(), "run", "-d", "--name", name, "--network", "hermes-hub-control",
		"--label", "hermes.test=chg-0336", "--entrypoint", "sh", image, "-c", "sleep 900"); err != nil {
		t.Fatalf("relay probe container: %v", err)
	}
	t.Cleanup(func() {
		_ = exec.Command("docker", "rm", "-f", name).Run()
	})
	return name
}

// relayCurl issues one bounded request to a claimed managed runtime the
// way the containerized controller reaches it: the control relay is
// dual-homed on the internal agent network and hermes-hub-control, and
// the claim answers its name on the control net (HUB_DOCKER_HOST_ROOT
// set). The loopback -p publish the argv also carries is a no-op for a
// container whose primary network is internal, so the host never owns a
// route — probes ride a shared container on the control network.
func relayCurl(ctx context.Context, box, url, auth, method string, body []byte, headers ...string) (int, []byte, error) {
	args := []string{"exec", "-i", box, "curl",
		"-sS", "--max-time", "110", "-X", method, "-H", "Authorization: Bearer " + auth,
		"-w", "\n%{http_code}"}
	for _, h := range headers {
		args = append(args, "-H", h)
	}
	if body != nil {
		args = append(args, "-H", "Content-Type: application/json", "--data-binary", "@-")
	}
	args = append(args, url)
	cmd := exec.CommandContext(ctx, "docker", args...)
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	if body != nil {
		cmd.Stdin = bytes.NewReader(body)
	}
	if err := cmd.Run(); err != nil {
		line := strings.TrimSpace(stderr.String())
		if cut := strings.IndexByte(line, '\n'); cut >= 0 {
			line = line[:cut]
		}
		return 0, nil, fmt.Errorf("relay probe %s %s: %w: %s", method, url, err, line)
	}
	out := stdout.Bytes()
	cut := bytes.LastIndexByte(out, '\n')
	if cut < 0 {
		return 0, nil, fmt.Errorf("relay probe %s %s: malformed output", method, url)
	}
	code, err := strconv.Atoi(strings.TrimSpace(string(out[cut+1:])))
	if err != nil {
		return 0, nil, fmt.Errorf("relay probe %s %s: bad status %q", method, url, out[cut+1:])
	}
	return code, out[:cut], nil
}

// relayCurlStream posts body and consumes the runtime's ndjson run stream
// line by line — the caller must see run.admitted while the run is still
// in flight so it can install the authorization grant the model proxy
// waits on (the same handoff supervisor's deliverRunAuthorization makes).
func relayCurlStream(ctx context.Context, box, url, auth string, headers []string, body []byte, consume func([]byte) error) error {
	args := []string{"exec", "-i", box, "curl",
		"-sS", "-N", "--max-time", "280", "-X", http.MethodPost,
		"-H", "Authorization: Bearer " + auth,
		"-H", "Content-Type: application/json",
		"-H", "Accept: " + hubruntime.RunStreamContentType,
		"--data-binary", "@-"}
	for _, h := range headers {
		args = append(args, "-H", h)
	}
	args = append(args, url)
	cmd := exec.CommandContext(ctx, "docker", args...)
	cmd.Stdin = bytes.NewReader(body)
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return err
	}
	if err := cmd.Start(); err != nil {
		return err
	}
	scanner := bufio.NewScanner(stdout)
	scanner.Buffer(make([]byte, 8192), 1<<20)
	for scanner.Scan() {
		if err := consume(scanner.Bytes()); err != nil {
			_ = cmd.Process.Kill()
			_ = cmd.Wait()
			return err
		}
	}
	serr := scanner.Err()
	werr := cmd.Wait()
	if serr != nil {
		return serr
	}
	if werr != nil {
		line := strings.TrimSpace(stderr.String())
		if cut := strings.IndexByte(line, '\n'); cut >= 0 {
			line = line[:cut]
		}
		return fmt.Errorf("relay stream: %w: %s", werr, line)
	}
	return nil
}

// managedProviderFixture is the minimal offline-capable OpenAI-compatible
// endpoint the managed runtime's model-relay forwards to — the same shape
// internal/devcheck's nativeContractProvider serves (any message answers
// with the fixed contract text, streaming and not).
func managedProviderFixture(t *testing.T) (url string) {
	t.Helper()
	listener, err := net.Listen("tcp", "0.0.0.0:0")
	if err != nil {
		t.Fatalf("provider listener: %v", err)
	}
	server := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, err := io.ReadAll(io.LimitReader(r.Body, 2<<20))
		if err != nil {
			http.Error(w, "unreadable provider request", http.StatusBadRequest)
			return
		}
		var request struct {
			Stream bool `json:"stream"`
		}
		if json.Unmarshal(body, &request) != nil {
			http.Error(w, "invalid bounded provider request", http.StatusBadRequest)
			return
		}
		if request.Stream {
			w.Header().Set("Content-Type", "text/event-stream")
			_, _ = io.WriteString(w, "data: {\"id\":\"probe\",\"object\":\"chat.completion.chunk\",\"choices\":[{\"index\":0,\"delta\":{\"role\":\"assistant\",\"content\":\"contract final answer\"},\"finish_reason\":null}]}\n\ndata: {\"id\":\"probe\",\"object\":\"chat.completion.chunk\",\"choices\":[{\"index\":0,\"delta\":{},\"finish_reason\":\"stop\"}]}\n\ndata: [DONE]\n\n")
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"id":"probe","object":"chat.completion","model":"everyday","choices":[{"index":0,"message":{"role":"assistant","content":"contract final answer"},"finish_reason":"stop"}],"usage":{"prompt_tokens":1,"completion_tokens":1,"total_tokens":2}}`)
	}))
	server.Listener = listener
	server.Start()
	t.Cleanup(func() { server.CloseClientConnections(); server.Close() })
	return "http://host.docker.internal:" + strconv.Itoa(listener.Addr().(*net.TCPAddr).Port) + "/v1"
}

// toolhubFixture answers MCP initialize and tools/list with an empty
// registry; the managed gateway only needs the endpoint to be live.
const managedToolhubFixture = `import json
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

// TestManagedRuntimeDockerAnswersMessage is the A3.1 docker leg: space
// creation, layer submission and activation, runtime claim and lease
// release all go through contracts/spawn/v1; the real docker backend then
// starts the pinned runtime image on an internal agent network and a
// message posted to the claimed endpoint returns the fixture model's
// reply — the same chain supervisor drives, minus any mocked runtime.
// Every docker resource it creates carries hermes.test=chg-0336.
func TestManagedRuntimeDockerAnswersMessage(t *testing.T) {
	if _, err := exec.LookPath("docker"); err != nil {
		t.Skip("docker CLI unavailable")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()
	if out, err := exec.CommandContext(ctx, "docker", "info", "--format", "{{.ServerVersion}}").CombinedOutput(); err != nil {
		t.Skipf("docker daemon unavailable: %v: %s", err, out)
	}
	image := os.Getenv("HUB_RUNTIME_IMAGE")
	if image == "" {
		image = "hermes-hub:0.5.0-dev"
	}
	if out, err := exec.CommandContext(ctx, "docker", "image", "inspect", image, "--format", "{{.Id}}").CombinedOutput(); err != nil {
		t.Skipf("runtime image %s unavailable: %v: %s", image, err, out)
	}

	suffix := make([]byte, 4)
	if _, err := rand.Read(suffix); err != nil {
		t.Fatalf("suffix: %v", err)
	}
	space := "m40a" + hex.EncodeToString(suffix)
	const env = "dev"
	tenant := spawnv1.Tenant{Org: "local", Owner: space}
	agentNet := "hermes-hub-agent-" + space + "-" + env
	bridge := "hermes-hub-m40a-" + hex.EncodeToString(suffix)
	prefix := "m40a-" + hex.EncodeToString(suffix)
	auth := "m40a-auth-" + hex.EncodeToString(suffix) + hex.EncodeToString(suffix)

	b := backend.NewDocker(backend.DockerConfig{
		ContainerPrefix: "hermes-hub-m40a-",
		Labels:          map[string]string{"hermes.test": "chg-0336"},
		Trusted:         true,
	})
	cleanup := func() {
		cctx, ccancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer ccancel()
		_ = managedDockerRun(cctx, "rm", "-f", prefix+"-toolhub", prefix+"-model")
		_ = managedDockerRun(cctx, "network", "rm", agentNet)
		_ = managedDockerRun(cctx, "network", "rm", bridge)
	}
	defer cleanup()

	if err := managedDockerRun(ctx, "network", "create", "--label", "hermes.test=chg-0336", bridge); err != nil {
		t.Fatalf("bridge network: %v", err)
	}
	if err := managedDockerRun(ctx, "network", "create", "--internal", "--label", "hermes.test=chg-0336", agentNet); err != nil {
		t.Fatalf("agent network: %v", err)
	}
	providerURL := managedProviderFixture(t)
	if err := managedDockerRun(ctx, "run", "-d", "--name", prefix+"-toolhub", "--network", agentNet,
		"--network-alias", "toolhub", "--label", "hermes.test=chg-0336",
		"--entrypoint", "python", image, "-c", managedToolhubFixture); err != nil {
		t.Fatalf("toolhub stub: %v", err)
	}
	if err := managedDockerRun(ctx, "run", "-d", "--name", prefix+"-model", "--network", agentNet,
		"--network-alias", "model-relay", "--label", "hermes.test=chg-0336",
		"--add-host", "host.docker.internal:host-gateway",
		"-e", "HUB_MODEL_RELAY_MODEL=everyday",
		"-e", "HUB_MODEL_RELAY_UPSTREAMS=fixture="+providerURL,
		"--entrypoint", "hub-runtime", image, "model-relay"); err != nil {
		t.Fatalf("model relay stub: %v", err)
	}
	if err := managedDockerRun(ctx, "network", "connect", bridge, prefix+"-model"); err != nil {
		t.Fatalf("model relay uplink: %v", err)
	}

	// Repo-shaped platform root + space dir: config/ beside spaces/, the
	// runtime auth file, the managed env file and the rendered hermes
	// source the spec builder materializes into the mounted config.
	root := t.TempDir()
	spacesRoot := filepath.Join(root, "spaces")
	dir := filepath.Join(spacesRoot, space)
	for _, sub := range []string{
		filepath.Join(root, "config", "skills"),
		filepath.Join(dir, "skills"),
		filepath.Join(dir, "workspace"),
		filepath.Join(dir, "runtime"),
		filepath.Join(dir, "hermes"),
		filepath.Join(dir, "home"),
		filepath.Join(dir, "connections"),
		filepath.Join(dir, "cache"),
		filepath.Join(dir, "archive"),
		filepath.Join(dir, "managed", env, "hermes"),
		filepath.Join(dir, "managed", env, "home"),
		filepath.Join(dir, "managed", env, "tmp"),
	} {
		if err := os.MkdirAll(sub, 0o777); err != nil {
			t.Fatalf("fixture dir %s: %v", sub, err)
		}
	}
	writeFixtureFile(t, filepath.Join(root, "config", "SOUL.md"), "# managed docker fixture\n")
	writeFixtureFile(t, filepath.Join(dir, "runtime.auth"),
		"HUB_RUNTIME_AUTH="+auth+"\nOPENAI_API_KEY=probe-openai-key-0123456789\nOPENAI_BASE_URL="+providerURL+"\n")
	writeFixtureFile(t, filepath.Join(dir, "secrets."+env+".env"), "OPENAI_API_KEY=probe-openai-key-0123456789\n")
	writeFixtureFile(t, filepath.Join(dir, "managed-runtime."+env+".env"),
		"HUB_RUNTIME_AUTH="+auth+"\nOPENAI_API_KEY=probe-openai-key-0123456789\nHUB_TOOLHUB_ENDPOINT=http://toolhub:8090/mcp\n")

	st := storetest.Open(t, storetest.SQLiteURL(t), migrations.FS)
	catalogDocs, err := catalog.Generate()
	if err != nil {
		t.Fatalf("catalog generate: %v", err)
	}
	revs := &spawn.Revisions{Store: st, Catalog: catalogDocs, Now: time.Now}
	revs.SpacesRoot = spacesRoot
	client := spawnv1.NewInProc(api.Contract{Revisions: revs})

	if _, err := client.CreateSpace(ctx, spawnv1.CreateSpaceRequest{
		Org: tenant.Org, Owner: tenant.Owner, ID: space, Environment: env, IdempotencyKey: "m40a-space-" + space,
	}); err != nil {
		t.Fatalf("create space: %v", err)
	}
	layers := layerDocs()
	layers["platform-ceiling"]["stack"]["identity"] = map[string]any{"user": space, "organization": ""}
	// Full dependency-complete service set; infra:false keeps the shared
	// services out of this space's rendered topology, and the synthetic
	// agent network's toolhub/model-relay stubs stand in for the relays.
	layers["platform-ceiling"]["stack"]["identity"] = map[string]any{"user": space, "organization": "", "infra": false}
	layers["platform-ceiling"]["stack"]["services"] = map[string]any{
		"hermes-runtime":    map[string]any{"mode": "supervised"},
		"toolhub":           map[string]any{"mode": "shared"},
		"model-relay":       map[string]any{"mode": "shared"},
		"cliproxy":          map[string]any{"mode": "shared"},
		"communication-hub": map[string]any{"mode": "shared"},
		"credential-broker": map[string]any{"mode": "shared"},
		"supervisor":        map[string]any{"mode": "shared"},
	}
	// The ceiling's openai-compatible slot carries the stub endpoint and the
	// provider id; the everyday ladder routes to it. Managed mode still
	// rewrites the runtime's model URL to the model-relay inside the sandbox.
	ceilingModels := layers["platform-ceiling"]["models"]
	ceilingModels["services"].(map[string]any)["direct-key"] = map[string]any{
		"via":      "openai-compatible",
		"endpoint": providerURL,
		"models":   []any{map[string]any{"id": "everyday"}},
	}
	ceilingModels["routes"].(map[string]any)["everyday"] = map[string]any{"ladder": []any{"direct-key"}}
	delete(layers["platform-template"]["config"], "ports")
	layers["user"] = userDocs("UTC")
	layers["user"]["tools"] = map[string]any{"binding": map[string]any{"profile_id": space + "-profile", "generation": 1}}
	ids := map[string]string{}
	for _, layer := range spawn.LayerOrder {
		docs, ok := layers[layer]
		if !ok {
			continue
		}
		rev, err := client.SubmitRevision(ctx, space, layer, spawnv1.SubmitRevisionRequest{
			Org: tenant.Org, Owner: tenant.Owner, Documents: docs, IdempotencyKey: "m40a-sub-" + space + "-" + layer,
		})
		if err != nil {
			t.Fatalf("submit %s: %v", layer, err)
		}
		ids[layer] = rev.ID
	}
	var all []string
	for _, layer := range spawn.LayerOrder {
		id, ok := ids[layer]
		if !ok {
			continue
		}
		rev, err := client.ValidateRevision(ctx, space, id, spawnv1.RevisionRequest{
			Org: tenant.Org, Owner: tenant.Owner, IdempotencyKey: "m40a-val-" + space + "-" + layer,
		})
		if err != nil {
			t.Fatalf("validate %s %s: %v", layer, id, err)
		}
		if rev.State != "validated" {
			t.Fatalf("validate %s %s: state %s detail %s", layer, id, rev.State, rev.Detail)
		}
		all = append(all, id)
	}
	if _, err := client.ActivateRevisions(ctx, space, spawnv1.ActivationRequest{
		Org: tenant.Org, Owner: tenant.Owner, Revisions: all, IdempotencyKey: "m40a-act-" + space,
	}); err != nil {
		t.Fatalf("activate set %v: %v", all, err)
	}

	settings, err := stack.ReadEffective(ctx, revReader{revs: revs}, dir, env)
	if err != nil {
		t.Fatalf("effective settings: %v", err)
	}
	// The same render `space apply` runs: generated/hermes.<env>.yaml, the
	// config-view the MountPlan binds at /config, and the effective skills
	// tree — the fixtures the claim's bind sources come from.
	if err := stack.RenderEnvironment(ctx, revReader{revs: revs}, dir, root, env); err != nil {
		t.Fatalf("render environment: %v", err)
	}
	if settings.PassportWorkspace != nil {
		if err := stack.MaterializeWorkspaceLayout(filepath.Join(dir, "managed", env), *settings.PassportWorkspace); err != nil {
			t.Fatalf("workspace layout: %v", err)
		}
	}
	// Synthetic bind directories must be writable by container uid 10001.
	_ = filepath.Walk(dir, func(path string, info os.FileInfo, err error) error {
		if err == nil && info.IsDir() {
			_ = os.Chmod(path, 0o777)
		}
		return nil
	})

	rts := &spawn.Runtimes{
		Store: st, Backend: b, Revisions: revs, Image: image,
		SpacesRoot: spacesRoot, RuntimePort: 8080, PlatformRoot: root,
		Limits: backend.Limits{Memory: "2g", CPUs: "2", PIDs: 512},
	}
	client = spawnv1.NewInProc(api.Contract{Revisions: revs, Runtimes: rts})

	// Answer the claim the way the containerized controller's callers see
	// it: the control relay's agent-network -p publish is inert (internal
	// networks carry no host bindings), so the reachable address is the
	// relay name on hermes-hub-control. Host-root is only non-emptiness
	// checked (mount stat skipped — the daemon validates binds itself).
	t.Setenv("HUB_DOCKER_HOST_ROOT", "/")

	runtimeID := "rt-" + space
	policy := "pol-s9a"
	claimed, err := client.ClaimRuntime(ctx, space, spawnv1.ClaimRuntimeRequest{
		Org: tenant.Org, Owner: tenant.Owner, Agent: "a1",
		RuntimeID: runtimeID, UserID: space, OrganizationID: tenant.Org,
		PolicyVersion: policy, IdentityRevision: "irev-s9a",
	})
	if err != nil {
		t.Fatalf("claim: %v", err)
	}
	defer func() {
		cctx, ccancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer ccancel()
		if err := b.Stop(cctx, claimed.Runtime.ID); err != nil {
			t.Logf("stop runtime %s: %v", claimed.Runtime.ID, err)
		}
	}()
	if claimed.Runtime.Address == "" {
		t.Fatal("claim returned no runtime address")
	}

	readyURL := claimed.Runtime.Address + "/readyz"
	box := relayProbe(t, image, "hermes-m40a-"+claimed.Runtime.ID+"-probe")
	deadline := time.Now().Add(480 * time.Second)
	for {
		code, _, err := relayCurl(ctx, box, readyURL, auth, http.MethodGet, nil)
		if err == nil && code == http.StatusOK {
			break
		}
		if time.Now().After(deadline) {
			if logs, lerr := exec.CommandContext(context.Background(), "docker", "logs", "--tail", "25", "hermes-hub-m40a-"+claimed.Runtime.ID).CombinedOutput(); lerr == nil {
				t.Logf("runtime logs tail:\n%s", logs)
			}
			t.Fatalf("managed runtime never became ready at %s (last code %d, err %v)", readyURL, code, err)
		}
		select {
		case <-ctx.Done():
			t.Fatalf("context done waiting for readyz: %v", ctx.Err())
		case <-time.After(3 * time.Second):
		}
	}

	job := hubruntime.ExecuteRequest{
		Envelope:       identity.TelegramEnvelope(space, 11, runtimeID, policy),
		JobID:          "m40a-first-message",
		OrganizationID: tenant.Org,
		UserID:         space,
		ActorID:        space,
		ScopeID:        "user:" + space,
		Channel:        "telegram_bot",
		Trigger:        "message",
		IdempotencyKey: "m40a-first-message",
		Text:           "contract probe",
	}
	payload, err := json.Marshal(job)
	if err != nil {
		t.Fatalf("marshal execute: %v", err)
	}
	// The run-token handoff (S6/S7 contract): the streamed execute carries a
	// caller-chosen delivery proof; when the runtime emits run.admitted the
	// caller installs the audience grants the in-runtime model proxy then
	// injects on the run's upstream calls — the same exchange
	// internal/supervisor's deliverRunAuthorization drives in production.
	proofBytes := make([]byte, 32)
	if _, err := rand.Read(proofBytes); err != nil {
		t.Fatalf("delivery proof: %v", err)
	}
	proof := hex.EncodeToString(proofBytes)
	var final hubruntime.ExecuteResponse
	streamErr := relayCurlStream(ctx, box, claimed.Runtime.Address+"/v1/execute", auth,
		[]string{hubruntime.RunDeliveryHeader + ": " + proof}, payload,
		func(line []byte) error {
			var event hubruntime.ExecuteResponse
			if err := json.Unmarshal(line, &event); err != nil {
				return fmt.Errorf("invalid stream frame: %w", err)
			}
			if event.LastEvent == "run.admitted" && event.RunID != "" {
				grant := hubruntime.RunAuthorization{
					JobID: job.JobID, RunID: event.RunID, Generation: event.RuntimeGeneration,
					Tokens: map[string]string{contract.ModelAudience: "m40a-run-token"},
				}
				grantBody, err := json.Marshal(grant)
				if err != nil {
					return err
				}
				code, out, err := relayCurl(ctx, box,
					claimed.Runtime.Address+hubruntime.RunAuthorizationPath, auth,
					http.MethodPut, grantBody, hubruntime.RunDeliveryHeader+": "+proof)
				if err != nil || code != http.StatusNoContent {
					return fmt.Errorf("install run authorization: code %d err %v: %s", code, err, out)
				}
			}
			if strings.HasPrefix(event.EventID, "terminal:") {
				final = event
			}
			return nil
		})
	if streamErr != nil {
		if logs, lerr := exec.CommandContext(context.Background(), "docker", "logs", "--tail", "40", "hermes-hub-m40a-"+claimed.Runtime.ID).CombinedOutput(); lerr == nil {
			t.Logf("runtime logs tail:\n%s", logs)
		}
		t.Fatalf("first message stream: %v", streamErr)
	}
	if final.Status != "completed" {
		if logs, lerr := exec.CommandContext(context.Background(), "docker", "logs", "--tail", "40", "hermes-hub-m40a-"+claimed.Runtime.ID).CombinedOutput(); lerr == nil {
			t.Logf("runtime logs tail:\n%s", logs)
		}
		t.Fatalf("first message run ended %s (%s): %s", final.Status, final.LastEvent, final.Text)
	}
	if !strings.Contains(final.Text, "contract final answer") {
		t.Fatalf("first message reply missing contract answer: %s", final.Text)
	}

	if err := client.ReleaseLease(ctx, claimed.Lease, spawnv1.LeaseRequest(tenant)); err != nil {
		t.Fatalf("release lease: %v", err)
	}
	rts2, err := client.ListRuntimes(ctx, tenant)
	if err != nil {
		t.Fatalf("list runtimes: %v", err)
	}
	var found bool
	for _, rt := range rts2 {
		if rt.ID == claimed.Runtime.ID {
			found = true
			if rt.State != "warm" {
				t.Fatalf("released runtime state %s, want warm", rt.State)
			}
		}
	}
	if !found {
		t.Fatal("released runtime missing from tenant inventory")
	}
	t.Logf("docker leg answered: %s", strings.TrimSpace(final.Text))
}
