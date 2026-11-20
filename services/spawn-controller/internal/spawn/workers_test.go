package spawn

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/letya999/hermes-hub/services/spawn-controller/internal/backend"
	"github.com/letya999/hermes-hub/services/spawn-controller/internal/spawn/migrations"
	"github.com/letya999/hermes-hub/shared/httpapi"
	"github.com/letya999/hermes-hub/shared/store"
	"github.com/letya999/hermes-hub/shared/store/storetest"
)

var workerTenant = store.Tenant{Org: "local", Owner: "alice"}

// workerFakeBackend records specs and answers the claim-time canary/proc
// probes and the reuse VerifyWorker check; its live set drives the
// sweeper's confirmed-stop transition.
type workerFakeBackend struct {
	mu     sync.Mutex
	live   map[string]backend.Runtime
	specs  map[string]backend.Spec
	execs  []backend.ExecSpec
	starts int
	exec   func(backend.ExecSpec) (backend.ExecResult, error)
}

func newWorkerFakeBackend() *workerFakeBackend {
	return &workerFakeBackend{live: map[string]backend.Runtime{}, specs: map[string]backend.Spec{}}
}

func (f *workerFakeBackend) Start(_ context.Context, spec backend.Spec) (backend.Runtime, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.starts++
	rt := backend.Runtime{ID: spec.Key, Space: spec.Space, Agent: spec.Agent, State: "running", Name: "hermes-hub-rt-" + spec.Key}
	f.live[spec.Key] = rt
	f.specs[spec.Key] = spec
	return rt, nil
}

func (f *workerFakeBackend) Stop(_ context.Context, id string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	delete(f.live, id)
	return nil
}

func (f *workerFakeBackend) Inspect(_ context.Context, id string) (backend.Runtime, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	rt, ok := f.live[id]
	if !ok {
		return backend.Runtime{}, fmt.Errorf("%w: %s", backend.ErrNotFound, id)
	}
	return rt, nil
}

func (f *workerFakeBackend) List(context.Context) ([]backend.Runtime, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := make([]backend.Runtime, 0, len(f.live))
	for _, rt := range f.live {
		out = append(out, rt)
	}
	return out, nil
}

func (f *workerFakeBackend) Exec(_ context.Context, id string, spec backend.ExecSpec) (backend.ExecResult, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if _, ok := f.live[id]; !ok {
		return backend.ExecResult{}, fmt.Errorf("%w: %s", backend.ErrNotFound, id)
	}
	f.execs = append(f.execs, spec)
	if f.exec != nil {
		return f.exec(spec)
	}
	return backend.ExecResult{}, nil
}

// VerifyWorker refuses a workload that drifted from its start spec — the
// reuse path depends on this to quarantine dirty workers.
func (f *workerFakeBackend) VerifyWorker(_ context.Context, id string, spec backend.Spec) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if _, ok := f.live[id]; !ok {
		return fmt.Errorf("%w: %s", backend.ErrNotFound, id)
	}
	got := f.specs[id]
	if got.Key != spec.Key || got.Image != spec.Image || len(got.Mounts) != len(spec.Mounts) {
		return fmt.Errorf("fake backend: worker profile drifted")
	}
	return nil
}

func (f *workerFakeBackend) startCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.starts
}

func (f *workerFakeBackend) specOf(key string) backend.Spec {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.specs[key]
}

// workersFixture wires the durable worker service over a real store and the
// recording fake backend; cells are configured with a real state root and a
// shipped cellinit file so the spec builder runs end to end.
func workersFixture(t *testing.T, mutate func(*Cells)) (*store.Store, *workerFakeBackend, *Workers) {
	t.Helper()
	s := storetest.Open(t, storetest.SQLiteURL(t), migrations.FS)
	be := newWorkerFakeBackend()
	root := t.TempDir()
	cellinit := filepath.Join(root, "cellinit")
	if err := os.WriteFile(cellinit, []byte("cellinit"), 0o700); err != nil {
		t.Fatal(err)
	}
	cells := &Cells{
		ToolsImage:     "sha256:" + strings.Repeat("a", 64),
		CellInitSource: cellinit,
		StateRoot:      filepath.Join(root, "state"),
		SpacesRoot:     filepath.Join(root, "spaces"),
	}
	if err := os.MkdirAll(cells.StateRoot, 0o700); err != nil {
		t.Fatal(err)
	}
	if mutate != nil {
		mutate(cells)
	}
	return s, be, &Workers{Store: s, Backend: be, Cells: cells}
}

func workerClaimReq() WorkerClaim {
	return WorkerClaim{
		CellKey: "binding|alice|bind-1|sha256:aaa", Image: "sha256:" + strings.Repeat("a", 64),
		Principal: "alice", BindingID: "bind-1",
		Limits: CellLimits{CPUMillis: 2000, MemoryMiB: 2048, MaxPIDs: 128, TimeoutSeconds: 60, OutputBytes: 262144},
	}
}

// TestWorkerTransitionsWired drives the FSM through the service: claim →
// busy (leased), release → draining → absent once the backend confirms.
func TestWorkerTransitionsWired(t *testing.T) {
	s, be, w := workersFixture(t, nil)
	ctx := context.Background()
	lease, err := w.Claim(ctx, workerTenant, "alice", workerClaimReq())
	if err != nil {
		t.Fatalf("claim: %v", err)
	}
	if lease.Worker.State != "busy" || lease.Lease == "" || !strings.HasPrefix(lease.Worker.ID, "wk-") {
		t.Fatalf("claim=%+v", lease)
	}
	if be.startCount() != 1 {
		t.Fatalf("starts=%d", be.startCount())
	}
	// Renew requires the lease.
	got, err := w.Renew(ctx, workerTenant, lease.Worker.ID, lease.Lease)
	if err != nil || got.State != "busy" {
		t.Fatalf("renew: %v %+v", err, got)
	}
	// Exec requires a held lease on a busy worker.
	res, err := w.Exec(ctx, workerTenant, lease.Worker.ID, lease.Lease, backend.ExecSpec{Argv: []string{"true"}})
	if err != nil || res.ExitCode != 0 {
		t.Fatalf("exec: %v %+v", err, res)
	}
	if _, err := w.Exec(ctx, workerTenant, lease.Worker.ID, "wl-wrong", backend.ExecSpec{Argv: []string{"true"}}); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("exec with foreign lease: %v, want not-found", err)
	}
	if err := w.Release(ctx, workerTenant, lease.Worker.ID, lease.Lease); err != nil {
		t.Fatalf("release: %v", err)
	}
	row, err := loadWorkerInStore(ctx, s, workerTenant, lease.Worker.ID)
	if err != nil || row.State != "absent" {
		t.Fatalf("released worker state=%q err=%v, want absent", row.State, err)
	}
	// Second claim on the same key is a fresh generation — the released
	// worker drained instead of being reused.
	lease2, err := w.Claim(ctx, workerTenant, "alice", workerClaimReq())
	if err != nil {
		t.Fatalf("reclaim: %v", err)
	}
	if lease2.Worker.ID == lease.Worker.ID || lease2.Worker.Generation != 2 {
		t.Fatalf("reclaim got %+v, want a new generation", lease2.Worker)
	}
}

func loadWorkerInStore(ctx context.Context, s *store.Store, t store.Tenant, id string) (Worker, error) {
	var out Worker
	err := s.ReadTx(ctx, func(tx *store.Tx) error {
		var err error
		out, err = loadWorker(tx.Scoped(t), id)
		return err
	})
	return out, err
}

// TestWorkerMountsOnlyOwnSpace: a claim may only bind the claiming space's
// own workspace — another space's tree, a path outside every root and a
// workspace belonging to a different principal all refuse before Start.
func TestWorkerMountsOnlyOwnSpace(t *testing.T) {
	_, be, w := workersFixture(t, nil)
	ctx := context.Background()
	// Plant alice's principal workspace under the spaces root.
	own := filepath.Join(w.Cells.SpacesRoot, "alice", "workspace")
	if err := os.MkdirAll(own, 0o700); err != nil {
		t.Fatal(err)
	}
	other := filepath.Join(w.Cells.SpacesRoot, "bob", "workspace")
	if err := os.MkdirAll(other, 0o700); err != nil {
		t.Fatal(err)
	}
	req := workerClaimReq()
	req.Workspace = CellWorkspace{Scope: WorkspacePrincipal}
	lease, err := w.Claim(ctx, workerTenant, "alice", req)
	if err != nil {
		t.Fatalf("own-space claim: %v", err)
	}
	mounts := be.specOf(lease.Worker.ID).Mounts
	found := false
	for _, m := range mounts {
		if m.Target == cellWorkGuest {
			found = true
			if filepath.Clean(m.Source) != filepath.Clean(own) {
				t.Fatalf("work mount=%q, want %q", m.Source, own)
			}
		}
	}
	if !found {
		t.Fatalf("principal workspace not mounted: %v", mounts)
	}
	// A claim whose principal is not the space refuses at spec construction —
	// cross-space mounts are impossible by construction. A fresh cell key
	// forces the build (an identical key would reuse the live worker, the
	// same way v0's warm key did).
	foreign := workerClaimReq()
	foreign.CellKey = "binding|bob|bind-9|sha256:aaa"
	foreign.Principal = "bob"
	foreign.Workspace = CellWorkspace{Scope: WorkspacePrincipal}
	if _, err := w.Claim(ctx, workerTenant, "alice", foreign); !errors.Is(err, httpapi.ErrForbidden) {
		t.Fatalf("foreign-principal claim: %v, want forbidden", err)
	}
	// A binding path outside the state root refuses too.
	outside := workerClaimReq()
	outside.CellKey = "binding|alice|bind-9|sha256:aaa"
	outside.Workspace = CellWorkspace{Scope: WorkspaceBinding, Path: t.TempDir()}
	if _, err := w.Claim(ctx, workerTenant, "alice", outside); !errors.Is(err, httpapi.ErrForbidden) {
		t.Fatalf("outside binding claim: %v, want forbidden", err)
	}
}

// TestWorkerNeverCrossesTenants: the same cell key and image under a
// different owner (or space) must mint a new worker — the tenant scope is
// part of the reuse key.
func TestWorkerNeverCrossesTenants(t *testing.T) {
	_, be, w := workersFixture(t, nil)
	ctx := context.Background()
	leaseA, err := w.Claim(ctx, workerTenant, "alice", workerClaimReq())
	if err != nil {
		t.Fatal(err)
	}
	other := store.Tenant{Org: "local", Owner: "bob"}
	req := workerClaimReq()
	req.Principal = "bob"
	leaseB, err := w.Claim(ctx, other, "bob", req)
	if err != nil {
		t.Fatal(err)
	}
	if leaseB.Worker.ID == leaseA.Worker.ID {
		t.Fatal("worker crossed tenants on an identical cell key")
	}
	if be.startCount() != 2 {
		t.Fatalf("cross-tenant reuse happened: starts=%d", be.startCount())
	}
	// Same tenant, different space: no reuse either.
	reqC := workerClaimReq()
	leaseC, err := w.Claim(ctx, workerTenant, "alice-2", reqC)
	if err != nil {
		t.Fatal(err)
	}
	if leaseC.Worker.ID == leaseA.Worker.ID {
		t.Fatal("worker crossed spaces on an identical cell key")
	}
}

// TestExecNoSecretInArgv: brokered credential values reach the cred
// sidecar through the exec file channel only — never the worker's spec
// argv, env, mounts or the persisted claim.
func TestExecNoSecretInArgv(t *testing.T) {
	s, be, w := workersFixture(t, nil)
	ctx := context.Background()
	secret := "sk-live-" + strings.Repeat("s", 16)
	req := workerClaimReq()
	req.Brokered = []CellCred{{EnvName: "GH_TOKEN", Host: "api.github.com", Prefix: "Bearer ", Value: secret}}
	lease, err := w.Claim(ctx, workerTenant, "alice", req)
	if err != nil {
		t.Fatalf("brokered claim: %v", err)
	}
	spec := be.specOf(lease.Worker.ID)
	var blob strings.Builder
	_, _ = blob.WriteString(strings.Join(spec.Command, "\x00"))
	_, _ = blob.WriteString(strings.Join(spec.Env, "\x00"))
	for _, sc := range spec.Sidecars {
		_, _ = blob.WriteString(strings.Join(sc.Command, "\x00"))
		_, _ = blob.WriteString(strings.Join(sc.Env, "\x00"))
		for _, f := range sc.Files {
			if !f.ViaExec {
				_, _ = blob.Write(f.Content) // docker-cp staged payload must be secret-free
			}
		}
	}
	if strings.Contains(blob.String(), secret) {
		t.Fatal("credential value reached spec argv/env/staged files")
	}
	// The persisted claim column strips values.
	row, err := loadWorkerInStore(ctx, s, workerTenant, lease.Worker.ID)
	if err != nil {
		t.Fatal(err)
	}
	for _, c := range row.claim.Brokered {
		if c.Value != "" {
			t.Fatal("credential value persisted on the worker row")
		}
	}
	if lease.CredHost == "" {
		t.Fatal("brokered claim returned no cred endpoint")
	}
}

// TestWorkerCellRealDocker is the end-to-end leg: a real store, a real
// docker backend and the shipped cellinit image run claim → exec → release
// with no fakes between the service and the daemon.
func TestWorkerCellRealDocker(t *testing.T) {
	if _, err := exec.LookPath("docker"); err != nil {
		t.Skip("docker not installed")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()
	if out, err := exec.CommandContext(ctx, "docker", "info", "--format", "{{.ServerVersion}}").CombinedOutput(); err != nil {
		t.Skipf("docker daemon unavailable: %s", strings.TrimSpace(string(out)))
	}
	tools := os.Getenv("HUB_CLI_TOOLS_IMAGE")
	if tools == "" {
		tools = "hermes-hub-cli-tools:test"
	}
	if out, err := exec.CommandContext(ctx, "docker", "image", "inspect", tools).CombinedOutput(); err != nil {
		t.Skipf("image %s unavailable: %s", tools, strings.TrimSpace(string(out)))
	}
	root := t.TempDir()
	// The worker mounts cellinit from the state root; it must be a Linux
	// binary no matter the host OS — cross-build it like the v0 test did.
	cellinit := filepath.Join(root, "cellinit")
	build := exec.CommandContext(ctx, "go", "build", "-o", cellinit, "github.com/letya999/hermes-hub/cmd/cellinit")
	build.Env = append(os.Environ(), "CGO_ENABLED=0", "GOOS=linux", "GOARCH=amd64")
	if out, err := build.CombinedOutput(); err != nil {
		t.Fatalf("cross-build cellinit: %v (%s)", err, out)
	}
	seccomp := filepath.Join(root, "seccomp.json")
	if err := os.WriteFile(seccomp, []byte(`{"defaultAction":"SCMP_ACT_ALLOW"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	stateRoot := filepath.Join(root, "state")
	if err := os.MkdirAll(stateRoot, 0o700); err != nil {
		t.Fatal(err)
	}
	s := storetest.Open(t, storetest.SQLiteURL(t), migrations.FS)
	be := backend.NewDocker(backend.DockerConfig{
		ContainerPrefix: "hermes-hub-wkt-",
		NetworkPrefix:   "hermes-hub-wnt-",
		Labels:          map[string]string{"hermes.test": "chg-0336"},
		Trusted:         true,
		StateRoot:       stateRoot,
	})
	// Labelled leftovers from a failed run are safe to remove; keep the
	// cleanup scoped to this test's prefixes.
	t.Cleanup(func() {
		_ = exec.Command("docker", "rm", "-f", "hermes-hub-wkt-test-worker").Run()
	})
	w := &Workers{Store: s, Backend: be, Cells: &Cells{
		ToolsImage: tools, CellInitSource: cellinit, StateRoot: stateRoot,
		SeccompProfile: seccomp, Runtime: "runc",
	}}
	imageID, err := exec.CommandContext(ctx, "docker", "image", "inspect", tools, "--format", "{{.Id}}").Output()
	if err != nil {
		t.Fatal(err)
	}
	req := workerClaimReq()
	req.Image = strings.TrimSpace(string(imageID))
	req.Limits = CellLimits{CPUMillis: 1000, MemoryMiB: 256, MaxPIDs: 64, TimeoutSeconds: 30, OutputBytes: 65536}
	lease, err := w.Claim(ctx, workerTenant, "alice", req)
	if err != nil {
		t.Fatalf("claim: %v", err)
	}
	res, err := w.Exec(ctx, workerTenant, lease.Worker.ID, lease.Lease, backend.ExecSpec{
		Argv: []string{"echo", "cell-ok"}, Timeout: 15 * time.Second, MaxOutput: 4096,
	})
	if err != nil {
		t.Fatalf("exec: %v", err)
	}
	if res.ExitCode != 0 || !strings.Contains(string(res.Stdout), "cell-ok") {
		t.Fatalf("exec result=%+v", res)
	}
	if err := w.Release(ctx, workerTenant, lease.Worker.ID, lease.Lease); err != nil {
		t.Fatalf("release: %v", err)
	}
	// Everything the worker owned — container and its network — is gone.
	ps, err := exec.CommandContext(ctx, "docker", "ps", "-aq", "--filter", "label=hermes-hub.key="+lease.Worker.ID).Output()
	if err != nil {
		t.Fatal(err)
	}
	if strings.TrimSpace(string(ps)) != "" {
		t.Fatalf("worker containers survived release: %s", ps)
	}
}
