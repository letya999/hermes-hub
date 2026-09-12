// Package supervisor owns the host-side scale-to-zero runtime lifecycle.
// It is deliberately small: Docker is the only compute backend and the
// pinned runtime remains the source of truth for Hermes state.
package supervisor

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
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	hubruntime "github.com/letya999/hermes-hub/internal/runtime"
)

type State string

const (
	Stopped  State = "stopped"
	Starting State = "starting"
	Ready    State = "ready"
	Busy     State = "busy"
	Idle     State = "idle"
	Stopping State = "stopping"
	Degraded State = "degraded"
)

// LeaseKind identifies work that must keep a context runtime warm. The names
// are part of the supervisor contract so callers cannot accidentally release
// a stream/approval lease as an ordinary job lease.
type LeaseKind string

const (
	LeaseJob       LeaseKind = "job"
	LeaseStream    LeaseKind = "stream"
	LeaseApproval  LeaseKind = "approval"
	LeaseLifecycle LeaseKind = "lifecycle"
	LeaseUncertain LeaseKind = "uncertain"
)

type Lease struct {
	ID          string    `json:"id"`
	Kind        LeaseKind `json:"kind"`
	PrincipalID string    `json:"principal_id"`
	ContextID   string    `json:"context_id"`
	RuntimeMode string    `json:"runtime_mode"`
	Generation  string    `json:"generation"`
	Owner       string    `json:"owner,omitempty"`
	ExpiresAt   time.Time `json:"expires_at,omitempty"`
}

type Config struct {
	Docker        string
	Image         string
	Network       string
	SpacesRoot    string
	StateDir      string
	RuntimeAuth   string
	WarmTTL       time.Duration
	MaxConcurrent int
	CPU           string
	Memory        string
	PIDs          int
	RuntimePort   int
	PortBase      int
	HTTP          *http.Client
	Probe         func(context.Context, string, string) error
	Command       func(context.Context, ...string) ([]byte, error)
	Now           func() time.Time
}

type Binding struct {
	PrincipalID      string
	ContextID        string
	RuntimeID        string
	RuntimeMode      string
	UserID           string
	OrganizationID   string
	PolicyVersion    string
	ContextRoot      string
	OrganizationRoot string
	EnvFile          string
	runtimeAuth      string
}

type Runtime struct {
	PrincipalID  string    `json:"principal_id"`
	ContextID    string    `json:"context_id"`
	RuntimeID    string    `json:"runtime_id"`
	RuntimeMode  string    `json:"runtime_mode"`
	Generation   string    `json:"generation"`
	Container    string    `json:"container"`
	Address      string    `json:"address"`
	State        State     `json:"state"`
	Leases       int       `json:"leases"`
	LastUsed     time.Time `json:"last_used"`
	IdleDeadline time.Time `json:"idle_deadline,omitempty"`
}

type runtimeEntry struct {
	Runtime
	auth     string
	leases   map[string]Lease
	restored bool
}

type Manager struct {
	cfg       Config
	mu        sync.Mutex
	items     map[string]*runtimeEntry
	locks     map[string]*sync.Mutex
	sem       chan struct{}
	slots     map[string]struct{}
	gen       uint64
	leaseSeq  uint64
	statePath string
	jobs      map[string]jobRecord
}

type persistedManager struct {
	Generation uint64               `json:"generation"`
	LeaseSeq   uint64               `json:"lease_seq"`
	Items      []persistedRuntime   `json:"items"`
	Jobs       map[string]jobRecord `json:"jobs,omitempty"`
}

type persistedRuntime struct {
	Runtime Runtime          `json:"runtime"`
	Leases  map[string]Lease `json:"leases,omitempty"`
}

type jobRecord struct {
	Fingerprint string                     `json:"fingerprint"`
	Response    hubruntime.ExecuteResponse `json:"response"`
	Status      string                     `json:"status"`
	Generation  string                     `json:"generation,omitempty"`
	UpdatedAt   time.Time                  `json:"updated_at"`
}

// Serve runs the always-on control-plane endpoint. It never mounts or exposes
// a Docker socket to the communication or Hermes containers.
func Serve(ctx context.Context, manager *Manager, listen string) error {
	if manager == nil || strings.TrimSpace(listen) == "" {
		return errors.New("manager and listen address are required")
	}
	server := &http.Server{Addr: listen, Handler: manager.Handler(), ReadHeaderTimeout: 5 * time.Second, MaxHeaderBytes: 64 * 1024}
	go func() {
		interval := manager.cfg.WarmTTL / 2
		if interval < time.Second {
			interval = time.Second
		}
		ticker := time.NewTicker(interval)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case now := <-ticker.C:
				_ = manager.Reap(ctx, now)
			}
		}
	}()
	go func() {
		<-ctx.Done()
		shutdown, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = server.Shutdown(shutdown)
	}()
	if err := server.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
		return err
	}
	return nil
}

func New(cfg Config) (*Manager, error) {
	if cfg.SpacesRoot == "" || cfg.RuntimeAuth == "" || cfg.Image == "" {
		return nil, errors.New("spaces root, runtime auth and image are required")
	}
	root, err := filepath.Abs(filepath.Clean(cfg.SpacesRoot))
	if err != nil {
		return nil, fmt.Errorf("spaces root: %w", err)
	}
	cfg.SpacesRoot = root
	if cfg.StateDir == "" {
		cfg.StateDir = filepath.Join(root, ".control")
	}
	if err := os.MkdirAll(cfg.StateDir, 0700); err != nil {
		return nil, fmt.Errorf("supervisor state: %w", err)
	}
	if cfg.Docker == "" {
		cfg.Docker = "docker"
	}
	if cfg.Network == "" {
		cfg.Network = "hermes-hub-runtime"
	}
	if cfg.WarmTTL <= 0 {
		cfg.WarmTTL = 5 * time.Minute
	}
	if cfg.MaxConcurrent <= 0 {
		cfg.MaxConcurrent = 8
	}
	if cfg.CPU == "" {
		cfg.CPU = "2"
	}
	if cfg.Memory == "" {
		cfg.Memory = "1g"
	}
	if cfg.PIDs <= 0 {
		cfg.PIDs = 256
	}
	if cfg.RuntimePort <= 0 {
		cfg.RuntimePort = 8080
	}
	if cfg.PortBase <= 0 {
		cfg.PortBase = 18080
	}
	if cfg.Now == nil {
		cfg.Now = time.Now
	}
	if cfg.HTTP == nil {
		cfg.HTTP = &http.Client{Timeout: 2 * time.Second}
	}
	if cfg.Command == nil {
		cfg.Command = func(ctx context.Context, args ...string) ([]byte, error) {
			return exec.CommandContext(ctx, cfg.Docker, args...).CombinedOutput()
		}
	}
	m := &Manager{cfg: cfg, items: map[string]*runtimeEntry{}, locks: map[string]*sync.Mutex{}, sem: make(chan struct{}, cfg.MaxConcurrent), slots: map[string]struct{}{}, jobs: map[string]jobRecord{}, statePath: filepath.Join(cfg.StateDir, "supervisor.json")}
	if err := m.load(); err != nil {
		return nil, err
	}
	return m, nil
}

func (m *Manager) load() error {
	b, err := os.ReadFile(m.statePath)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	var state persistedManager
	if err := json.Unmarshal(b, &state); err != nil {
		return fmt.Errorf("invalid supervisor state: %w", err)
	}
	m.gen, m.leaseSeq = state.Generation, state.LeaseSeq
	if state.Jobs != nil {
		m.jobs = state.Jobs
	}
	dirty := false
	for key, record := range m.jobs {
		if record.Status == "running" {
			record.Status = "uncertain"
			record.Response.Status = "uncertain"
			record.Response.LastEvent = "run.unknown"
			record.UpdatedAt = m.cfg.Now().UTC()
			m.jobs[key] = record
			dirty = true
		}
	}
	for _, item := range state.Items {
		if !validID(item.Runtime.PrincipalID) || !validID(item.Runtime.ContextID) || !validID(item.Runtime.RuntimeID) || !validID(item.Runtime.RuntimeMode) || item.Runtime.Generation == "" {
			return errors.New("invalid supervisor runtime state")
		}
		m.items[runtimeKey(Binding{PrincipalID: item.Runtime.PrincipalID, ContextID: item.Runtime.ContextID, RuntimeMode: item.Runtime.RuntimeMode})] = &runtimeEntry{Runtime: item.Runtime, leases: item.Leases, restored: true}
	}
	if dirty {
		return m.persistLocked()
	}
	return nil
}

func (m *Manager) persistLocked() error {
	state := persistedManager{Generation: m.gen, LeaseSeq: m.leaseSeq, Items: make([]persistedRuntime, 0, len(m.items)), Jobs: m.jobs}
	for _, item := range m.items {
		state.Items = append(state.Items, persistedRuntime{Runtime: item.Runtime, Leases: item.leases})
	}
	b, err := json.Marshal(state)
	if err != nil {
		return err
	}
	tmp, err := os.CreateTemp(m.cfg.StateDir, ".supervisor-*")
	if err != nil {
		return err
	}
	name := tmp.Name()
	defer os.Remove(name)
	if err = tmp.Chmod(0600); err == nil {
		_, err = tmp.Write(b)
	}
	if err == nil {
		err = tmp.Sync()
	}
	if closeErr := tmp.Close(); err == nil {
		err = closeErr
	}
	if err == nil {
		err = os.Rename(name, m.statePath)
	}
	return err
}

func (m *Manager) lockFor(key string) *sync.Mutex {
	m.mu.Lock()
	defer m.mu.Unlock()
	if lock := m.locks[key]; lock != nil {
		return lock
	}
	lock := &sync.Mutex{}
	m.locks[key] = lock
	return lock
}

func (m *Manager) Ensure(ctx context.Context, binding Binding) (Runtime, error) {
	binding, err := m.normalize(binding)
	if err != nil {
		return Runtime{}, err
	}
	key := runtimeKey(binding)
	lock := m.lockFor(key)
	lock.Lock()
	defer lock.Unlock()
	m.mu.Lock()
	if current := m.items[key]; current != nil && current.RuntimeID != binding.RuntimeID {
		m.mu.Unlock()
		return Runtime{}, errors.New("runtime identity mismatch")
	}
	current := m.items[key]
	if current != nil && current.restored && (current.State == Ready || current.State == Idle || current.State == Busy) {
		container, address, auth := current.Container, current.Address, binding.runtimeAuth
		m.mu.Unlock()
		if out, inspectErr := m.command(ctx, "inspect", "--format", "{{.State.Status}}", container); inspectErr == nil && strings.TrimSpace(string(out)) == "running" && m.ready(ctx, address, auth) == nil {
			select {
			case m.sem <- struct{}{}:
			case <-ctx.Done():
				return Runtime{}, ctx.Err()
			}
			m.mu.Lock()
			current = m.items[key]
			if current == nil {
				m.mu.Unlock()
				<-m.sem
				return Runtime{}, errors.New("runtime disappeared during restore")
			}
			current.restored, current.State, current.Leases, current.LastUsed, current.IdleDeadline = false, Busy, current.Leases+1, m.cfg.Now(), time.Time{}
			m.slots[key] = struct{}{}
			result := current.Runtime
			_ = m.persistLocked()
			m.mu.Unlock()
			return result, nil
		}
		m.mu.Lock()
		current = m.items[key]
		if current != nil {
			current.restored, current.State, current.Leases, current.leases = false, Stopped, 0, map[string]Lease{}
			_ = m.persistLocked()
		}
		m.mu.Unlock()
	} else {
		m.mu.Unlock()
	}
	m.mu.Lock()
	if current := m.items[key]; current != nil && (current.State == Ready || current.State == Idle || current.State == Busy) {
		current.State, current.Leases, current.LastUsed, current.IdleDeadline = Busy, current.Leases+1, m.cfg.Now(), time.Time{}
		result := current.Runtime
		_ = m.persistLocked()
		m.mu.Unlock()
		return result, nil
	}
	m.mu.Unlock()
	m.mu.Lock()
	_, slotHeld := m.slots[key]
	m.mu.Unlock()
	if !slotHeld {
		select {
		case m.sem <- struct{}{}:
		case <-ctx.Done():
			return Runtime{}, ctx.Err()
		}
		m.mu.Lock()
		m.slots[key] = struct{}{}
		m.mu.Unlock()
	}
	container := containerName(key)
	port := m.cfg.PortBase + int(hashNumber(key)%10000)
	address := "http://127.0.0.1:" + strconv.Itoa(port)
	m.mu.Lock()
	m.gen++
	logical := &runtimeEntry{Runtime: Runtime{PrincipalID: binding.PrincipalID, ContextID: binding.ContextID, RuntimeID: binding.RuntimeID, RuntimeMode: binding.RuntimeMode, Generation: fmt.Sprintf("gen-%d", m.gen), Container: container, Address: address, State: Starting, Leases: 1, LastUsed: m.cfg.Now()}, auth: binding.runtimeAuth, leases: map[string]Lease{}}
	m.items[key] = logical
	_ = m.persistLocked()
	m.mu.Unlock()
	if out, inspectErr := m.command(ctx, "inspect", "--format", "{{.State.Status}}", container); inspectErr == nil && strings.TrimSpace(string(out)) == "running" {
		if err = m.ready(ctx, address, binding.runtimeAuth); err == nil {
			m.mu.Lock()
			logical.State = Busy
			result := logical.Runtime
			_ = m.persistLocked()
			m.mu.Unlock()
			return result, nil
		}
		_, _ = m.command(ctx, "rm", "-f", container)
	}
	args := m.runArgsWithGeneration(binding, container, port, logical.Generation)
	if _, err = m.command(ctx, args...); err != nil {
		m.markDegraded(key)
		m.releaseSlot(key)
		return Runtime{}, fmt.Errorf("start runtime: %w", err)
	}
	if err = m.ready(ctx, address, binding.runtimeAuth); err != nil {
		_, _ = m.command(ctx, "rm", "-f", container)
		m.markDegraded(key)
		m.releaseSlot(key)
		return Runtime{}, fmt.Errorf("runtime readiness: %w", err)
	}
	m.mu.Lock()
	logical.State = Busy
	result := logical.Runtime
	_ = m.persistLocked()
	m.mu.Unlock()
	return result, nil
}

func (m *Manager) Release(contextID, runtimeMode string) error {
	var key string
	m.mu.Lock()
	for candidate, runtime := range m.items {
		if runtime.ContextID == contextID && runtime.RuntimeMode == runtimeMode {
			if key != "" && key != candidate {
				m.mu.Unlock()
				return errors.New("runtime release is ambiguous")
			}
			key = candidate
		}
	}
	m.mu.Unlock()
	if key == "" {
		return errors.New("runtime is not registered")
	}
	return m.releaseKey(key)
}

func (m *Manager) releaseKey(key string) error {
	lock := m.lockFor(key)
	lock.Lock()
	defer lock.Unlock()
	m.mu.Lock()
	defer m.mu.Unlock()
	runtime := m.items[key]
	if runtime == nil {
		return errors.New("runtime is not registered")
	}
	if runtime.Leases == 0 {
		return errors.New("runtime lease underflow")
	}
	runtime.Leases--
	runtime.LastUsed = m.cfg.Now()
	if runtime.Leases == 0 {
		runtime.State, runtime.IdleDeadline = Idle, m.cfg.Now().Add(m.cfg.WarmTTL)
	}
	if err := m.persistLocked(); err != nil {
		return err
	}
	return nil
}

// Acquire starts/reuses a runtime and records a named lease. The lease ID is
// opaque to callers and is the only safe way to release work after a request
// has crossed an asynchronous boundary.
func (m *Manager) Acquire(ctx context.Context, binding Binding, kind LeaseKind) (Lease, Runtime, error) {
	if !validLeaseKind(kind) {
		return Lease{}, Runtime{}, errors.New("invalid lease kind")
	}
	if _, err := m.Ensure(ctx, binding); err != nil {
		return Lease{}, Runtime{}, err
	}
	normalized, err := m.normalize(binding)
	if err != nil {
		_ = m.ReleaseBinding(binding)
		return Lease{}, Runtime{}, err
	}
	binding = normalized
	key := runtimeKey(binding)
	lock := m.lockFor(key)
	lock.Lock()
	defer lock.Unlock()
	m.mu.Lock()
	defer m.mu.Unlock()
	m.leaseSeq++
	lease := Lease{ID: fmt.Sprintf("lease-%x", m.leaseSeq), Kind: kind, PrincipalID: binding.PrincipalID, ContextID: binding.ContextID, RuntimeMode: binding.RuntimeMode, Generation: entryGeneration(m.items[key]), Owner: "supervisor", ExpiresAt: m.cfg.Now().Add(5 * time.Minute)}
	entry := m.items[key]
	if entry == nil || entry.State == Stopped || entry.State == Degraded {
		return Lease{}, Runtime{}, errors.New("runtime is not registered")
	}
	if entry.leases == nil {
		entry.leases = map[string]Lease{}
	}
	entry.leases[lease.ID] = lease
	if err := m.persistLocked(); err != nil {
		return Lease{}, Runtime{}, err
	}
	return lease, entry.Runtime, nil
}

func entryGeneration(entry *runtimeEntry) string {
	if entry == nil {
		return ""
	}
	return entry.Generation
}

func (m *Manager) ReleaseLease(leaseID string) error {
	if strings.TrimSpace(leaseID) == "" {
		return errors.New("lease ID is required")
	}
	m.mu.Lock()
	var binding Binding
	for _, entry := range m.items {
		if lease, ok := entry.leases[leaseID]; ok {
			if lease.Generation != entry.Generation {
				m.mu.Unlock()
				return errors.New("stale runtime lease")
			}
			delete(entry.leases, leaseID)
			binding = Binding{PrincipalID: lease.PrincipalID, ContextID: lease.ContextID, RuntimeMode: lease.RuntimeMode}
			break
		}
	}
	m.mu.Unlock()
	if binding.ContextID == "" {
		return errors.New("lease is not registered")
	}
	return m.releaseKey(runtimeKey(binding))
}

func (m *Manager) ReleaseBinding(binding Binding) error {
	binding, err := m.normalize(binding)
	if err != nil {
		return err
	}
	return m.releaseKey(runtimeKey(binding))
}

func (m *Manager) Reap(ctx context.Context, now time.Time) error {
	m.mu.Lock()
	keys := []string{}
	for key, runtime := range m.items {
		if runtime.State == Idle && runtime.Leases == 0 && !runtime.IdleDeadline.After(now) {
			keys = append(keys, key)
		}
	}
	m.mu.Unlock()
	for _, key := range keys {
		lock := m.lockFor(key)
		lock.Lock()
		m.mu.Lock()
		runtime := m.items[key]
		if runtime == nil || runtime.State != Idle || runtime.Leases != 0 || runtime.IdleDeadline.After(now) {
			m.mu.Unlock()
			lock.Unlock()
			continue
		}
		runtime.State = Stopping
		container := runtime.Container
		m.mu.Unlock()
		_, err := m.command(ctx, "rm", "-f", container)
		m.mu.Lock()
		if err != nil {
			runtime.State = Degraded
			if _, held := m.slots[key]; held {
				delete(m.slots, key)
				<-m.sem
			}
		} else {
			runtime.State, runtime.IdleDeadline = Stopped, time.Time{}
			if _, held := m.slots[key]; held {
				delete(m.slots, key)
				<-m.sem
			}
		}
		_ = m.persistLocked()
		m.mu.Unlock()
		lock.Unlock()
		if err != nil {
			return fmt.Errorf("stop runtime %s: %w", container, err)
		}
	}
	return nil
}

func (m *Manager) Status(binding Binding) (Runtime, bool, error) {
	binding, err := m.normalize(binding)
	if err != nil {
		return Runtime{}, false, err
	}
	key := runtimeKey(binding)
	m.mu.Lock()
	defer m.mu.Unlock()
	runtime, ok := m.items[key]
	if !ok {
		return Runtime{PrincipalID: binding.PrincipalID, ContextID: binding.ContextID, RuntimeID: binding.RuntimeID, State: Stopped}, false, nil
	}
	return runtime.Runtime, true, nil
}

func (m *Manager) Handler() http.Handler { return http.HandlerFunc(m.serveHTTP) }

func (m *Manager) serveHTTP(w http.ResponseWriter, r *http.Request) {
	if !m.authorized(r) {
		writeJSON(w, http.StatusUnauthorized, map[string]string{"error": "unauthorized"})
		return
	}
	switch r.URL.Path {
	case "/healthz":
		if r.Method != http.MethodGet {
			writeJSON(w, http.StatusMethodNotAllowed, map[string]string{"error": "method not allowed"})
			return
		}
		w.WriteHeader(http.StatusOK)
	case "/v1/execute":
		m.execute(w, r)
	case "/v1/runtimes":
		m.list(w, r)
	case "/v1/leases":
		m.lease(w, r)
	default:
		if strings.HasPrefix(r.URL.Path, "/v1/leases/") {
			m.releaseLeaseHTTP(w, r, strings.TrimPrefix(r.URL.Path, "/v1/leases/"))
			return
		}
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "not found"})
	}
}

func (m *Manager) lease(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeJSON(w, http.StatusMethodNotAllowed, map[string]string{"error": "method not allowed"})
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, 256*1024)
	defer r.Body.Close()
	body, err := io.ReadAll(r.Body)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "request body too large"})
		return
	}
	var payload struct {
		hubruntime.ExecuteRequest
		Kind LeaseKind `json:"kind"`
	}
	if json.Unmarshal(body, &payload) != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid request"})
		return
	}
	binding, err := m.bindingFor(payload.ExecuteRequest)
	if err != nil {
		writeJSON(w, http.StatusConflict, map[string]string{"error": err.Error()})
		return
	}
	lease, runtime, err := m.Acquire(r.Context(), binding, payload.Kind)
	if err != nil {
		writeJSON(w, http.StatusServiceUnavailable, map[string]string{"error": err.Error()})
		return
	}
	writeJSON(w, http.StatusCreated, struct {
		Lease   Lease   `json:"lease"`
		Runtime Runtime `json:"runtime"`
	}{lease, runtime})
}

func (m *Manager) releaseLeaseHTTP(w http.ResponseWriter, r *http.Request, leaseID string) {
	if r.Method != http.MethodDelete {
		writeJSON(w, http.StatusMethodNotAllowed, map[string]string{"error": "method not allowed"})
		return
	}
	if err := m.ReleaseLease(leaseID); err != nil {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"released": true, "lease_id": leaseID})
}

func (m *Manager) authorized(r *http.Request) bool {
	provided := strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")
	return len(provided) == len(m.cfg.RuntimeAuth) && subtle.ConstantTimeCompare([]byte(provided), []byte(m.cfg.RuntimeAuth)) == 1
}

var errJobInProgress = errors.New("job is already in progress")

func executeJobKey(request hubruntime.ExecuteRequest) string {
	if request.JobID != "" {
		return request.JobID
	}
	return request.IdempotencyKey
}

func executeFingerprint(request hubruntime.ExecuteRequest) string {
	b, _ := json.Marshal(request)
	h := sha256.Sum256(b)
	return hex.EncodeToString(h[:])
}

func (m *Manager) beginJob(request hubruntime.ExecuteRequest) (hubruntime.ExecuteResponse, bool, error) {
	key := executeJobKey(request)
	if key == "" || request.IdempotencyKey == "" {
		return hubruntime.ExecuteResponse{}, false, errors.New("job identity is required")
	}
	fingerprint := executeFingerprint(request)
	m.mu.Lock()
	defer m.mu.Unlock()
	if previous, ok := m.jobs[key]; ok {
		if previous.Fingerprint != fingerprint {
			return hubruntime.ExecuteResponse{}, false, errors.New("job identity already used with different payload")
		}
		if previous.Status == "completed" {
			return previous.Response, true, nil
		}
		if previous.Status == "uncertain" {
			return previous.Response, true, nil
		}
		return hubruntime.ExecuteResponse{}, false, errJobInProgress
	}
	m.jobs[key] = jobRecord{Fingerprint: fingerprint, Status: "running", UpdatedAt: time.Now().UTC()}
	return hubruntime.ExecuteResponse{}, false, m.persistLocked()
}

func (m *Manager) finishJob(request hubruntime.ExecuteRequest, response hubruntime.ExecuteResponse, status string) {
	m.finishJobGeneration(request, response, status, "")
}

func (m *Manager) bindJobGeneration(request hubruntime.ExecuteRequest, generation string) {
	key := executeJobKey(request)
	m.mu.Lock()
	defer m.mu.Unlock()
	if record, ok := m.jobs[key]; ok && record.Status == "running" {
		record.Generation, record.UpdatedAt = generation, time.Now().UTC()
		m.jobs[key] = record
		_ = m.persistLocked()
	}
}

func (m *Manager) finishJobGeneration(request hubruntime.ExecuteRequest, response hubruntime.ExecuteResponse, status, generation string) {
	key := executeJobKey(request)
	if key == "" {
		return
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if record, ok := m.jobs[key]; ok {
		if generation != "" && record.Generation != generation {
			return
		}
		record.Response, record.Status, record.UpdatedAt = response, status, time.Now().UTC()
		m.jobs[key] = record
		_ = m.persistLocked()
	}
}

func (m *Manager) execute(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeJSON(w, http.StatusMethodNotAllowed, map[string]string{"error": "method not allowed"})
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, 2*1024*1024+64*1024)
	defer r.Body.Close()
	body, err := io.ReadAll(r.Body)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "request body too large"})
		return
	}
	var request hubruntime.ExecuteRequest
	if json.Unmarshal(body, &request) != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid request"})
		return
	}
	binding, err := m.bindingFor(request)
	if err != nil {
		writeJSON(w, http.StatusConflict, map[string]string{"error": err.Error()})
		return
	}
	known, replay, err := m.beginJob(request)
	if err != nil {
		status := http.StatusConflict
		if errors.Is(err, errJobInProgress) {
			status = http.StatusTooEarly
		}
		writeJSON(w, status, map[string]string{"error": err.Error()})
		return
	}
	if replay {
		writeJSON(w, http.StatusOK, known)
		return
	}
	lease, runtime, err := m.Acquire(r.Context(), binding, LeaseJob)
	if err != nil {
		m.finishJob(request, hubruntime.ExecuteResponse{}, "uncertain")
		writeJSON(w, http.StatusServiceUnavailable, map[string]string{"error": "runtime unavailable"})
		return
	}
	m.bindJobGeneration(request, runtime.Generation)
	defer func() { _ = m.ReleaseLease(lease.ID) }()
	req, err := http.NewRequestWithContext(r.Context(), http.MethodPost, runtime.Address+"/v1/execute", bytes.NewReader(body))
	if err != nil {
		uncertain := hubruntime.ExecuteResponse{JobID: request.JobID, RuntimeGeneration: runtime.Generation, Status: "uncertain", LastEvent: "run.unknown"}
		m.finishJobGeneration(request, uncertain, "uncertain", runtime.Generation)
		writeJSON(w, http.StatusBadGateway, uncertain)
		return
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+binding.runtimeAuth)
	response, err := m.cfg.HTTP.Do(req)
	if err != nil {
		uncertain := hubruntime.ExecuteResponse{JobID: request.JobID, RuntimeGeneration: runtime.Generation, Status: "uncertain", LastEvent: "run.unknown"}
		m.finishJobGeneration(request, uncertain, "uncertain", runtime.Generation)
		writeJSON(w, http.StatusBadGateway, uncertain)
		return
	}
	defer response.Body.Close()
	body, readErr := io.ReadAll(io.LimitReader(response.Body, 2*1024*1024+1))
	if readErr != nil || len(body) > 2*1024*1024 {
		uncertain := hubruntime.ExecuteResponse{JobID: request.JobID, RuntimeGeneration: runtime.Generation, Status: "uncertain", LastEvent: "run.unknown"}
		m.finishJobGeneration(request, uncertain, "uncertain", runtime.Generation)
		writeJSON(w, http.StatusBadGateway, uncertain)
		return
	}
	var result hubruntime.ExecuteResponse
	if json.Unmarshal(body, &result) == nil {
		status := result.Status
		if status == "" && response.StatusCode/100 == 2 {
			status = "completed"
		}
		m.finishJobGeneration(request, result, status, runtime.Generation)
	} else {
		uncertain := hubruntime.ExecuteResponse{JobID: request.JobID, RuntimeGeneration: runtime.Generation, Status: "uncertain", LastEvent: "run.unknown"}
		m.finishJobGeneration(request, uncertain, "uncertain", runtime.Generation)
		writeJSON(w, http.StatusBadGateway, uncertain)
		return
	}
	w.Header().Set("Content-Type", response.Header.Get("Content-Type"))
	w.WriteHeader(response.StatusCode)
	_, _ = w.Write(body)
}

func (m *Manager) list(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeJSON(w, http.StatusMethodNotAllowed, map[string]string{"error": "method not allowed"})
		return
	}
	m.mu.Lock()
	values := make([]Runtime, 0, len(m.items))
	for _, item := range m.items {
		values = append(values, item.Runtime)
	}
	m.mu.Unlock()
	writeJSON(w, http.StatusOK, values)
}

func (m *Manager) bindingFor(request hubruntime.ExecuteRequest) (Binding, error) {
	contextID := request.ContextID
	if strings.HasPrefix(request.ScopeID, "user:") {
		contextID = strings.TrimPrefix(request.ScopeID, "user:")
		if contextID != request.UserID || request.PrincipalID != request.UserID {
			return Binding{}, errors.New("user scope mismatch")
		}
	}
	if strings.HasPrefix(request.ScopeID, "organization:") {
		contextID = strings.TrimPrefix(request.ScopeID, "organization:")
	}
	if !strings.HasPrefix(request.ScopeID, "user:") && !strings.HasPrefix(request.ScopeID, "organization:") {
		return Binding{}, errors.New("invalid scope")
	}
	if request.ContextID != contextID || request.UserID == "" || request.ActorID == "" || request.PolicyVersion == "" || !validID(contextID) || request.OrganizationID == "" || request.RuntimeID == "" {
		return Binding{}, errors.New("invalid runtime binding")
	}
	organizationRoot := ""
	if strings.HasPrefix(request.ScopeID, "user:") && request.OrganizationID != "" && request.OrganizationID != "personal" {
		organizationRoot = filepath.Join(m.cfg.SpacesRoot, request.OrganizationID)
	}
	return m.normalize(Binding{PrincipalID: request.PrincipalID, ContextID: contextID, RuntimeID: request.RuntimeID, RuntimeMode: "gateway", UserID: request.UserID, OrganizationID: request.OrganizationID, PolicyVersion: request.PolicyVersion, OrganizationRoot: organizationRoot})
}

func runtimeKey(binding Binding) string {
	return binding.PrincipalID + "\x00" + binding.ContextID + "\x00" + binding.RuntimeMode
}

func validLeaseKind(kind LeaseKind) bool {
	switch kind {
	case LeaseJob, LeaseStream, LeaseApproval, LeaseLifecycle, LeaseUncertain:
		return true
	default:
		return false
	}
}

func (m *Manager) normalize(binding Binding) (Binding, error) {
	if !validID(binding.PrincipalID) || !validID(binding.ContextID) || !validID(binding.RuntimeID) {
		return Binding{}, errors.New("invalid runtime binding")
	}
	if binding.RuntimeMode == "" {
		binding.RuntimeMode = "gateway"
	}
	if !validID(binding.RuntimeMode) {
		return Binding{}, errors.New("invalid runtime mode")
	}
	root := filepath.Join(m.cfg.SpacesRoot, binding.ContextID)
	if binding.ContextRoot != "" {
		root = filepath.Clean(binding.ContextRoot)
	}
	abs, err := filepath.Abs(root)
	if err != nil {
		return Binding{}, err
	}
	rel, err := filepath.Rel(m.cfg.SpacesRoot, abs)
	if err != nil || rel == "." || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) || filepath.IsAbs(rel) {
		return Binding{}, errors.New("context path escapes spaces root")
	}
	expected := filepath.Join(m.cfg.SpacesRoot, binding.ContextID)
	if expected, err = filepath.Abs(expected); err != nil || abs != expected {
		return Binding{}, errors.New("context path is not the bound context")
	}
	if info, err := os.Lstat(abs); err != nil || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return Binding{}, errors.New("context path is unavailable")
	}
	binding.ContextRoot = abs
	envFile := filepath.Join(abs, "runtime.auth")
	if binding.EnvFile != "" {
		envFile = filepath.Clean(binding.EnvFile)
	}
	if info, err := os.Lstat(envFile); err != nil || !info.Mode().IsRegular() || info.Mode()&os.ModeSymlink != 0 {
		return Binding{}, errors.New("runtime env file is unavailable")
	}
	rel, err = filepath.Rel(abs, envFile)
	if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) || filepath.IsAbs(rel) {
		return Binding{}, errors.New("runtime env file escapes context")
	}
	binding.EnvFile = envFile
	binding.runtimeAuth, err = envFileAuth(envFile)
	if err != nil {
		return Binding{}, err
	}
	if binding.OrganizationRoot != "" {
		org, err := filepath.Abs(filepath.Clean(binding.OrganizationRoot))
		if err != nil {
			return Binding{}, err
		}
		rel, err := filepath.Rel(m.cfg.SpacesRoot, org)
		if err != nil || rel == "." || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) || filepath.IsAbs(rel) {
			return Binding{}, errors.New("organization path escapes spaces root")
		}
		info, err := os.Lstat(org)
		if err != nil || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
			return Binding{}, errors.New("organization path is unavailable")
		}
		binding.OrganizationRoot = org
	}
	return binding, nil
}

func (m *Manager) runArgs(binding Binding, container string, port int) []string {
	return m.runArgsWithGeneration(binding, container, port, "")
}

func (m *Manager) runArgsWithGeneration(binding Binding, container string, port int, generation string) []string {
	args := []string{"run", "-d", "--name", container, "--network", m.cfg.Network, "--restart=no", "--read-only", "--init", "--user", "10001:10001", "--cap-drop", "ALL", "--security-opt", "no-new-privileges:true", "--pids-limit", strconv.Itoa(m.cfg.PIDs), "--memory", m.cfg.Memory, "--cpus", m.cfg.CPU, "-p", fmt.Sprintf("127.0.0.1:%d:%d", port, m.cfg.RuntimePort), "--mount", "type=bind,src=" + binding.ContextRoot + ",dst=/scope"}
	args = append(args, "--env-file", binding.EnvFile)
	if binding.OrganizationRoot != "" {
		args = append(args, "--mount", "type=bind,src="+binding.OrganizationRoot+",dst=/org,readonly")
	}
	for _, file := range []struct{ source, target string }{{filepath.Join(binding.ContextRoot, "hermes."+envOr("HUB_ENV", "prod")+".yaml"), "/config/config.yaml"}, {filepath.Join(binding.ContextRoot, "SOUL.md"), "/config/SOUL.md"}} {
		if _, err := os.Stat(file.source); err == nil {
			args = append(args, "--mount", "type=bind,src="+file.source+",dst="+file.target+",readonly")
		}
	}
	args = append(args, "-e", "HUB_PERSISTENT_HERMES=true", "-e", "HUB_RUNTIME_LISTEN=0.0.0.0:"+strconv.Itoa(m.cfg.RuntimePort), "-e", "HUB_STATE=/scope/runtime", "-e", "HUB_WORKSPACE=/scope/workspace", "-e", "HERMES_HOME=/scope/hermes", "-e", "HOME=/scope/home", "-e", "HUB_USER_ID="+binding.UserID, "-e", "HUB_ORGANIZATION_ID="+binding.OrganizationID, "-e", "HUB_RUNTIME_ID="+binding.RuntimeID, "-e", "HUB_POLICY_VERSION="+binding.PolicyVersion, "-e", "API_SERVER_ENABLED=true", "-e", "API_SERVER_HOST=127.0.0.1", "-e", "API_SERVER_PORT=8642")
	if generation != "" {
		args = append(args, "-e", "HUB_RUNTIME_GENERATION="+generation)
	}
	args = append(args, m.cfg.Image, "serve")
	return args
}

func (m *Manager) ready(ctx context.Context, address, auth string) error {
	if m.cfg.Probe != nil {
		return m.cfg.Probe(ctx, address, auth)
	}
	deadline := time.Now().Add(30 * time.Second)
	for time.Now().Before(deadline) {
		req, _ := http.NewRequestWithContext(ctx, http.MethodGet, address+"/readyz", nil)
		req.Header.Set("Authorization", "Bearer "+auth)
		response, err := m.cfg.HTTP.Do(req)
		if err == nil {
			_, _ = io.Copy(io.Discard, response.Body)
			_ = response.Body.Close()
			if response.StatusCode == http.StatusOK {
				return nil
			}
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(100 * time.Millisecond):
		}
	}
	return errors.New("readiness timeout")
}

func (m *Manager) command(ctx context.Context, args ...string) ([]byte, error) {
	return m.cfg.Command(ctx, args...)
}
func (m *Manager) releaseSlot(key string) {
	m.mu.Lock()
	if _, ok := m.slots[key]; ok {
		delete(m.slots, key)
		<-m.sem
	}
	m.mu.Unlock()
}
func (m *Manager) markDegraded(key string) {
	m.mu.Lock()
	if runtime := m.items[key]; runtime != nil {
		runtime.State = Degraded
		runtime.Leases = 0
		_ = m.persistLocked()
	}
	m.mu.Unlock()
}
func containerName(key string) string {
	return "hermes-context-" + hex.EncodeToString(hashBytes(key)[:8])
}
func hashNumber(key string) uint32 {
	sum := hashBytes(key)
	return uint32(sum[0])<<24 | uint32(sum[1])<<16 | uint32(sum[2])<<8 | uint32(sum[3])
}
func hashBytes(value string) []byte { sum := sha256.Sum256([]byte(value)); return sum[:] }
func validID(value string) bool {
	if value == "" || len(value) > 40 || value[0] < 'a' || value[0] > 'z' {
		return false
	}
	for _, char := range value[1:] {
		if !(char >= 'a' && char <= 'z') && !(char >= '0' && char <= '9') && char != '_' && char != '-' {
			return false
		}
	}
	return true
}
func envOr(name, fallback string) string {
	if value := os.Getenv(name); value != "" {
		return value
	}
	return fallback
}

func envFileAuth(path string) (string, error) {
	body, err := os.ReadFile(path)
	if err != nil {
		return "", errors.New("runtime env file is unreadable")
	}
	for _, line := range strings.Split(string(body), "\n") {
		if strings.HasPrefix(line, "HUB_RUNTIME_AUTH=") {
			value := strings.TrimSpace(strings.TrimPrefix(line, "HUB_RUNTIME_AUTH="))
			if value != "" && !strings.ContainsAny(value, "\r\n") {
				return value, nil
			}
		}
	}
	return "", errors.New("HUB_RUNTIME_AUTH missing from runtime env file")
}
func writeJSON(w http.ResponseWriter, status int, value any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(value)
}
