// Package api is the spawn-controller HTTP surface: runtime ensure and the
// message path, plus the service bootstrap that wires them.
package api

import (
	"bytes"
	"context"
	"crypto/sha256"
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
	"github.com/letya999/hermes-hub/services/spawn-controller/internal/spawn/migrations"
	"github.com/letya999/hermes-hub/shared/proctree"
	"github.com/letya999/hermes-hub/shared/store"
	_ "github.com/letya999/hermes-hub/shared/store/postgres" // pgx for HUB_DATABASE_URL postgres://
)

// Controller ensures one child runtime per (space, agent) pair and forwards
// space traffic to it. Runtimes are looked up, never created twice; max
// bounds how many may run at once.
type Controller struct {
	backend backend.Backend
	token   string
	max     int
	spaces  http.Handler
	mu      sync.Mutex
	ensured map[string]backend.Runtime
	client  *http.Client
}

func runtimeKey(space, agent string) string {
	sum := sha256.Sum256([]byte(space + "\x00" + agent))
	return "rt-" + hex.EncodeToString(sum[:8])
}

// ensure returns the runtime for (space, agent), starting it on first use.
func (c *Controller) ensure(ctx context.Context, space, agent string) (backend.Runtime, error) {
	key := runtimeKey(space, agent)
	c.mu.Lock()
	defer c.mu.Unlock()
	if rt, ok := c.ensured[key]; ok {
		if observed, err := c.backend.Inspect(ctx, key); err == nil && observed.State != "exited" {
			return rt, nil
		}
		delete(c.ensured, key)
	}
	if c.max > 0 && len(c.backend.List(ctx)) >= c.max {
		return backend.Runtime{}, errors.New("runtime limit reached")
	}
	// Until S4 the spec is thin: the store-backed tenant, policy digest and
	// mounts land with the runtime rows. The process backend applies only
	// key, space, agent and image.
	rt, err := c.backend.Start(ctx, backend.Spec{
		Key: key, Org: "local", Owner: space, Space: space, Agent: agent,
		Image: "harness-runtime", OCIRuntime: "runc",
	})
	if err != nil {
		return backend.Runtime{}, err
	}
	c.ensured[key] = rt
	return rt, nil
}

// message forwards a user message to the ensured runtime and relays its reply.
func (c *Controller) message(ctx context.Context, space, agent, text string) (map[string]any, error) {
	rt, err := c.ensure(ctx, space, agent)
	if err != nil {
		return nil, err
	}
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
	}
	mux.HandleFunc("/v1/runtimes", c.guard(func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, http.StatusOK, map[string]any{"runtimes": c.backend.List(r.Context())})
	}))
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
		out, err := c.message(r.Context(), input.Space, input.Agent, input.Text)
		if err != nil {
			writeJSON(w, http.StatusBadGateway, map[string]any{"error": "runtime unavailable: " + err.Error()})
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

const defaultMaxRuntimes = 32

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
	if os.Getenv("HUB_TRUSTED") != "true" {
		return errors.New("untrusted installation")
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
	executable, err := os.Executable()
	if err != nil {
		return err
	}
	group, err := proctree.NewGroup()
	if err != nil {
		return err
	}
	defer group.Close()
	be := backend.NewProcess(
		filepath.Dir(executable), dataDir,
		[]string{"TZ", "HUB_MANAGED_MODEL_URL", "HUB_MANAGED_MODEL_ID", "HUB_FEATURES"},
		token, os.Getenv("HUB_TRUSTED") == "true", ctx, group,
	)
	serviceStore, err := store.Open(ctx, databaseURL, migrations.FS)
	if err != nil {
		return err
	}
	defer serviceStore.Close()
	controller := &Controller{
		backend: be, token: token, max: max,
		spaces: spawnv1.Handler(Contract{Revisions: &spawn.Revisions{
			Store:       serviceStore,
			LoadCatalog: catalogLoader(catalog.LoadPinned),
		}}, token),
		ensured: map[string]backend.Runtime{}, client: &http.Client{Timeout: 60 * time.Second},
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
	for _, child := range be.List(context.Background()) {
		_ = be.Stop(context.Background(), child.ID)
	}
	if err != nil && !errors.Is(err, http.ErrServerClosed) {
		return err
	}
	return nil
}
