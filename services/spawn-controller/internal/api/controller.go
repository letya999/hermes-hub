// Package api is the spawn-controller HTTP surface: the durable runtime
// lifecycle's claim-lease-forward-release message path, the tenant-scoped
// runtime inventory and the lease heartbeat, plus the service bootstrap
// that wires them.
package api

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/subtle"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	spawnv1 "github.com/letya999/hermes-hub/contracts/spawn/v1"
	"github.com/letya999/hermes-hub/internal/catalog"
	"github.com/letya999/hermes-hub/services/spawn-controller/internal/backend"
	"github.com/letya999/hermes-hub/services/spawn-controller/internal/spawn"
	"github.com/letya999/hermes-hub/services/spawn-controller/internal/spawn/importer"
	"github.com/letya999/hermes-hub/services/spawn-controller/internal/spawn/migrations"
	"github.com/letya999/hermes-hub/shared/httpapi"
	"github.com/letya999/hermes-hub/shared/proctree"
	"github.com/letya999/hermes-hub/shared/store"
	_ "github.com/letya999/hermes-hub/shared/store/postgres" // pgx for HUB_DATABASE_URL postgres://
)

// Controller routes space traffic through the durable runtime lifecycle:
// every message claims the (tenant, space, agent)'s runtime row, holds a
// lease on it while the call is forwarded and releases the hold after —
// the in-memory registry this replaced is gone.
type Controller struct {
	runtimes *spawn.Runtimes
	token    string
	spaces   http.Handler
	client   *http.Client
}

// requestTenant reads the caller's explicit (org, owner) — the same
// transport the spawn/v1 contract uses for its trusted control-plane
// clients; tenant identity is never derived from a header.
func requestTenant(r *http.Request) (store.Tenant, error) {
	t := store.Tenant{Org: r.URL.Query().Get("org"), Owner: r.URL.Query().Get("owner")}
	if t.Org == "" || t.Owner == "" || len(t.Org) > 256 || len(t.Owner) > 256 {
		return t, httpapi.Errorf(httpapi.ErrInvalid, "tenant query parameters required")
	}
	return t, nil
}

// leaseID mints the hold row's provenance label — an id, never a key.
func leaseID() string {
	raw := make([]byte, 8)
	_, _ = rand.Read(raw)
	return "ls-" + hex.EncodeToString(raw)
}

// statusOf maps the service's typed errors onto the surface's statuses.
func statusOf(err error) int {
	switch {
	case errors.Is(err, httpapi.ErrNotFound):
		return http.StatusNotFound
	case errors.Is(err, httpapi.ErrConflict):
		return http.StatusConflict
	case errors.Is(err, httpapi.ErrForbidden):
		return http.StatusForbidden
	case errors.Is(err, httpapi.ErrInvalid):
		return http.StatusBadRequest
	default:
		return http.StatusServiceUnavailable
	}
}

// message executes the full message path: claim the runtime bound to the
// space's stored effective digest, hold a lease on it while the call is
// forwarded, then release. The lease keeps the runtime out of the idle
// drain for the call's duration; the release moves it back to warm.
func (c *Controller) message(ctx context.Context, t store.Tenant, space, agent, text string) (map[string]any, error) {
	rt, err := c.runtimes.Claim(ctx, t, space, agent)
	if err != nil {
		return nil, err
	}
	lease := leaseID()
	if err := c.runtimes.Acquire(ctx, t, rt.ID, lease); err != nil {
		return nil, err
	}
	defer func() { _ = c.runtimes.Release(context.Background(), t, rt.ID, lease) }()
	body, _ := json.Marshal(map[string]string{"space": space, "agent": agent, "text": text})
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, rt.Address+"/v1/messages", bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+c.token)
	resp, err := c.client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("runtime %s: %w", rt.ID, err)
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return nil, err
	}
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("runtime %s: %s", rt.ID, http.StatusText(resp.StatusCode))
	}
	var out map[string]any
	if err := json.Unmarshal(raw, &out); err != nil {
		return nil, err
	}
	return out, nil
}

// guard wraps a /v1/* handler with the service-surface checks: a browser
// Origin is refused outright (no cross-origin page may speak to loopback),
// then the shared service token must match in constant time.
func (c *Controller) guard(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Origin") != "" {
			writeJSON(w, http.StatusForbidden, map[string]any{"error": "browser origin refused"})
			return
		}
		got, bearer := strings.CutPrefix(r.Header.Get("Authorization"), "Bearer ")
		if !bearer || subtle.ConstantTimeCompare([]byte(got), []byte(c.token)) != 1 {
			writeJSON(w, http.StatusUnauthorized, map[string]any{"error": "unauthorized"})
			return
		}
		next(w, r)
	}
}

func (c *Controller) routes() *http.ServeMux {
	mux := http.NewServeMux()
	mux.HandleFunc("/healthz", func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(http.StatusOK) })
	if c.spaces != nil {
		mux.Handle("/v1/spaces", c.guard(c.spaces.ServeHTTP))
		mux.Handle("/v1/spaces/", c.guard(c.spaces.ServeHTTP))
		// The contract's runtime surface — claim, inventory and the lease
		// lifecycle — is served by the same contract handler that owns the
		// spaces routes; only the process-mode first message stays here.
		mux.Handle("/v1/runtimes", c.guard(c.spaces.ServeHTTP))
		mux.Handle("/v1/leases/", c.guard(c.spaces.ServeHTTP))
		mux.Handle("/v1/workers/", c.guard(c.spaces.ServeHTTP))
	}
	mux.HandleFunc("/v1/messages", c.guard(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			writeJSON(w, http.StatusMethodNotAllowed, map[string]any{"error": "method not allowed"})
			return
		}
		if ct := r.Header.Get("Content-Type"); !strings.HasPrefix(ct, "application/json") {
			writeJSON(w, http.StatusUnsupportedMediaType, map[string]any{"error": "application/json required"})
			return
		}
		var input struct {
			Space string `json:"space"`
			Agent string `json:"agent"`
			Text  string `json:"text"`
		}
		if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<20)).Decode(&input); err != nil || input.Space == "" || input.Agent == "" || input.Text == "" {
			writeJSON(w, http.StatusBadRequest, map[string]any{"error": "invalid message"})
			return
		}
		t, err := requestTenant(r)
		if err != nil {
			writeJSON(w, statusOf(err), map[string]any{"error": err.Error()})
			return
		}
		out, err := c.message(r.Context(), t, input.Space, input.Agent, input.Text)
		if err != nil {
			writeJSON(w, statusOf(err), map[string]any{"error": "runtime unavailable: " + err.Error()})
			return
		}
		writeJSON(w, http.StatusOK, out)
	}))
	return mux
}

func writeJSON(w http.ResponseWriter, status int, body any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(body)
}

const (
	defaultMaxRuntimes  = 32
	defaultSweepEvery   = 5 * time.Second
	runtimeDefaultImage = "harness-runtime"
)

// catalogLoader caches the pinned catalog after the first successful
// load. The baked tree lives in the image while process-mode children
// inherit HUB_CATALOG_ROOT from the launcher, so the lookup stays lazy —
// and a failure is never remembered: the next compile retries the load
// instead of serving a transient error until restart.
func catalogLoader(load func() (catalog.Docs, error)) func() (catalog.Docs, error) {
	var mu sync.Mutex
	var docs catalog.Docs
	var loaded bool
	return func() (catalog.Docs, error) {
		mu.Lock()
		defer mu.Unlock()
		if loaded {
			return docs, nil
		}
		got, err := load()
		if err != nil {
			return catalog.Docs{}, err
		}
		docs, loaded = got, true
		return docs, nil
	}
}

// Serve runs the controller until ctx ends. Untrusted installations or a
// missing service token are refused before any socket or child exists.
func Serve(ctx context.Context) error {
	// The launch backend selects where runtimes execute: process mode is
	// the trusted developer default — it offers no isolation, so an
	// untrusted installation is refused outright; docker is the compose
	// stack's container authority, where an untrusted installation must
	// run every workload on runsc (X6).
	launchMode := os.Getenv("HUB_BACKEND")
	if launchMode == "" {
		launchMode = "process"
	}
	trusted := os.Getenv("HUB_TRUSTED") == "true"
	ociRuntime := os.Getenv("HUB_CONTAINER_RUNTIME")
	if ociRuntime == "" {
		ociRuntime = "runc"
	}
	switch launchMode {
	case "process":
		if !trusted {
			return errors.New("untrusted installation")
		}
	case "docker":
		if !trusted && ociRuntime != "runsc" {
			return errors.New("untrusted installation requires the runsc OCI runtime")
		}
	default:
		return fmt.Errorf("unknown HUB_BACKEND %q", launchMode)
	}
	listen := os.Getenv("HUB_LISTEN")
	if listen == "" {
		return errors.New("HUB_LISTEN is required")
	}
	dataDir := os.Getenv("HUB_DATA_DIR")
	if dataDir == "" {
		return errors.New("HUB_DATA_DIR is required")
	}
	token := os.Getenv("HUB_SERVICE_TOKEN")
	if token == "" {
		return errors.New("HUB_SERVICE_TOKEN is required")
	}
	databaseURL := os.Getenv("HUB_DATABASE_URL")
	if databaseURL == "" {
		return errors.New("HUB_DATABASE_URL is required")
	}
	max := defaultMaxRuntimes
	if raw := os.Getenv("HUB_MAX_RUNTIMES"); raw != "" {
		var parsed int
		if _, err := fmt.Sscanf(raw, "%d", &parsed); err != nil || parsed < 1 {
			return errors.New("HUB_MAX_RUNTIMES must be a positive integer")
		}
		max = parsed
	}
	sweepEvery := defaultSweepEvery
	if raw := os.Getenv("HUB_SWEEP_INTERVAL"); raw != "" {
		parsed, err := time.ParseDuration(raw)
		if err != nil || parsed <= 0 {
			return errors.New("HUB_SWEEP_INTERVAL must be a positive duration")
		}
		sweepEvery = parsed
	}
	// The state root hosts the seeded cellinit, the docker backend's staged
	// sidecar payloads and — for docker-mode runtimes — its scratch files.
	stateRoot := os.Getenv("HUB_CELL_STATE_ROOT")
	if stateRoot == "" {
		stateRoot = filepath.Join(dataDir, "cells")
	}
	var be backend.Backend
	if launchMode == "docker" {
		be = backend.NewDocker(backend.DockerConfig{Trusted: trusted, StateRoot: stateRoot})
	} else {
		executable, err := os.Executable()
		if err != nil {
			return err
		}
		group, err := proctree.NewGroup()
		if err != nil {
			return err
		}
		defer group.Close()
		be = backend.NewProcess(
			filepath.Dir(executable), dataDir,
			[]string{"TZ", "HUB_MANAGED_MODEL_URL", "HUB_MANAGED_MODEL_ID", "HUB_FEATURES"},
			token, trusted, ctx, group,
		)
	}
	serviceStore, err := store.Open(ctx, databaseURL, migrations.FS)
	if err != nil {
		return err
	}
	defer serviceStore.Close()
	image := os.Getenv("HUB_RUNTIME_IMAGE")
	if image == "" {
		image = runtimeDefaultImage
	}
	runtimes := &spawn.Runtimes{
		Store: serviceStore, Backend: be,
		Image: image, OCIRuntime: ociRuntime, MaxLive: max,
	}
	// Cells are the operator-opt-in worker surface (V5): enabled only when
	// the tools image and the shipped cellinit binary are configured —
	// every other input has a safe default or refuses at spec time.
	cells := &spawn.Cells{
		ToolsImage:      os.Getenv("HUB_CELL_TOOLS_IMAGE"),
		ProxyImage:      os.Getenv("HUB_CELL_PROXY_IMAGE"),
		CellInitSource:  os.Getenv("HUB_CELLINIT"),
		StateRoot:       stateRoot,
		SpacesRoot:      os.Getenv("HUB_SPACES_ROOT"),
		Environment:     os.Getenv("HUB_CELL_ENV"),
		Runtime:         os.Getenv("HUB_CELL_RUNTIME"),
		SeccompProfile:  os.Getenv("HUB_CELL_SECCOMP"),
		PerPrincipalUID: os.Getenv("HUB_CELL_PER_PRINCIPAL_UID") == "true",
	}
	if cells.Enabled() {
		// The state root hosts the seeded cellinit and the docker backend's
		// staged sidecar payloads; create it only when cells can run.
		if err := os.MkdirAll(stateRoot, 0o700); err != nil {
			return err
		}
	}
	maxWorkers := defaultMaxRuntimes * 2
	if raw := os.Getenv("HUB_MAX_WORKERS"); raw != "" {
		var parsed int
		if _, err := fmt.Sscanf(raw, "%d", &parsed); err != nil || parsed < 1 {
			return errors.New("HUB_MAX_WORKERS must be a positive integer")
		}
		maxWorkers = parsed
	}
	workerBackend := backend.NewDocker(backend.DockerConfig{
		ContainerPrefix: "hermes-hub-wk-",
		NetworkPrefix:   "hermes-hub-wn-",
		Trusted:         trusted, // untrusted installs reach here only on runsc
		StateRoot:       stateRoot,
	})
	workers := &spawn.Workers{
		Store: serviceStore, Backend: workerBackend,
		Cells: cells, MaxLive: maxWorkers,
	}
	revisions := &spawn.Revisions{
		Store:       serviceStore,
		LoadCatalog: catalogLoader(catalog.LoadPinned),
	}
	// One-time v0 import (D5): enabled by HUB_IMPORT_V0_DIR it runs once
	// before the socket opens, while the spawn store still holds none of
	// the v0 space rows. The report is stdout-safe: counts and ids only.
	if v0Dir := os.Getenv("HUB_IMPORT_V0_DIR"); v0Dir != "" {
		report, err := (&importer.Importer{Revisions: revisions, Store: serviceStore}).Run(ctx, importer.Config{
			V0Dir:     v0Dir,
			SpacesDir: os.Getenv("HUB_IMPORT_SPACES_DIR"),
			DryRun:    os.Getenv("HUB_IMPORT_DRY_RUN") == "true",
		})
		if err != nil {
			return err
		}
		if err := json.NewEncoder(os.Stdout).Encode(report); err != nil {
			return err
		}
	}
	sweeper := &spawn.Sweeper{Store: serviceStore, Backend: be, WorkerBackend: workerBackend}
	// Boot recovery is one reconcile pass before the socket opens: runtime
	// rows this process left behind are re-anchored against the backend.
	if err := sweeper.Tick(ctx); err != nil {
		return err
	}
	sweepCtx, stopSweep := context.WithCancel(ctx)
	defer stopSweep()
	go sweeper.Run(sweepCtx, sweepEvery)
	controller := &Controller{
		runtimes: runtimes, token: token,
		spaces: spawnv1.Handler(Contract{Revisions: revisions, Workers: workers, Runtimes: runtimes}, token),
		client: &http.Client{Timeout: 60 * time.Second},
	}
	listener, err := net.Listen("tcp", listen)
	if err != nil {
		return err
	}
	server := &http.Server{Handler: controller.routes(), ReadHeaderTimeout: 10 * time.Second}
	go func() {
		<-ctx.Done()
		_ = server.Shutdown(context.Background())
	}()
	err = server.Serve(listener)
	// Process-mode children die with their controller; docker runtimes are
	// durable containers — the next boot's reconcile re-anchors them, so a
	// service restart never stops live runtime workloads.
	if launchMode == "process" {
		if children, lerr := be.List(context.Background()); lerr == nil {
			for _, child := range children {
				_ = be.Stop(context.Background(), child.ID)
			}
		}
	}
	if err != nil && !errors.Is(err, http.ErrServerClosed) {
		return err
	}
	return nil
}
