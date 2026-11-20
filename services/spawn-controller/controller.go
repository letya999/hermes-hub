package main

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"sync"
	"time"
)

// Controller ensures one child runtime per (space, agent) pair and forwards
// space traffic to it. Runtimes are looked up, never created twice.
type Controller struct {
	backend Backend
	mu      sync.Mutex
	ensured map[string]Runtime
	client  *http.Client
}

func runtimeKey(space, agent string) string {
	sum := sha256.Sum256([]byte(space + "\x00" + agent))
	return "rt-" + hex.EncodeToString(sum[:8])
}

// ensure returns the runtime for (space, agent), starting it on first use.
func (c *Controller) ensure(ctx context.Context, space, agent string) (Runtime, error) {
	key := runtimeKey(space, agent)
	c.mu.Lock()
	defer c.mu.Unlock()
	if rt, ok := c.ensured[key]; ok {
		if observed, err := c.backend.Inspect(ctx, key); err == nil && observed.State != "exited" {
			return rt, nil
		}
		delete(c.ensured, key)
	}
	rt, err := c.backend.Start(ctx, SpawnSpec{Key: key, Space: space, Agent: agent})
	if err != nil {
		return Runtime{}, err
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

func (c *Controller) routes() *http.ServeMux {
	mux := http.NewServeMux()
	mux.HandleFunc("/healthz", func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(http.StatusOK) })
	mux.HandleFunc("/v1/runtimes", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, http.StatusOK, map[string]any{"runtimes": c.backend.List(r.Context())})
	})
	mux.HandleFunc("/v1/messages", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			writeJSON(w, http.StatusMethodNotAllowed, map[string]any{"error": "method not allowed"})
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
	})
	return mux
}

func writeJSON(w http.ResponseWriter, status int, body any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(body)
}

// controllerURL is the controller's own loopback address as seen by children.
func controllerURL() string {
	listen := os.Getenv("HUB_LISTEN")
	if listen == "" {
		return ""
	}
	if _, _, err := net.SplitHostPort(listen); err != nil {
		return ""
	}
	return "http://" + listen
}

// serve runs the controller until ctx ends. Untrusted installations are
// refused before any socket or child exists.
func serve(ctx context.Context) error {
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
	executable, err := os.Executable()
	if err != nil {
		return err
	}
	backend := &processBackend{
		binDir:   filepath.Dir(executable),
		dataDir:  dataDir,
		env:      []string{"TZ", "HUB_MANAGED_MODEL_URL", "HUB_MANAGED_MODEL_ID", "HUB_FEATURES"},
		ctx:      ctx,
		children: map[string]*childRuntime{},
	}
	controller := &Controller{backend: backend, ensured: map[string]Runtime{}, client: &http.Client{Timeout: 60 * time.Second}}
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
	for _, child := range backend.children {
		_ = backend.Stop(context.Background(), child.rt.ID)
	}
	if err != nil && !errors.Is(err, http.ErrServerClosed) {
		return err
	}
	return nil
}
