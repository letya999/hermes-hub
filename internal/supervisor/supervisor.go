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
	"log"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	hubruntime "github.com/letya999/hermes-hub/internal/runtime"
	"github.com/letya999/hermes-hub/internal/stack"
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
	ConnectorHealth   string    `json:"connector_health,omitempty"`
	FailureGeneration string    `json:"failure_generation,omitempty"`
	Ownership         string    `json:"ownership,omitempty"`
	CrashCount        int       `json:"crash_count,omitempty"`
	CrashWindowStart  time.Time `json:"crash_window_start,omitempty"`
	NextRetryAt       time.Time `json:"next_retry_at,omitempty"`
	RuntimeHealth     string    `json:"runtime_health,omitempty"`
	HermesReadiness   string    `json:"hermes_readiness,omitempty"`
	HealthCheckedAt   time.Time `json:"health_checked_at,omitempty"`
	Desired           bool      `json:"desired"`
	PrincipalID       string    `json:"principal_id"`
	ContextID         string    `json:"context_id"`
	RuntimeID         string    `json:"runtime_id"`
	RuntimeMode       string    `json:"runtime_mode"`
	Generation        string    `json:"generation"`
	Container         string    `json:"container"`
	Address           string    `json:"address"`
	State             State     `json:"state"`
	Leases            int       `json:"leases"`
	LastUsed          time.Time `json:"last_used"`
	IdleDeadline      time.Time `json:"idle_deadline,omitempty"`
}

type runtimeEntry struct {
	Runtime
	binding  Binding
	auth     string
	leases   map[string]Lease
	restored bool
}

type Manager struct {
	pins      map[string]hubruntime.ExecuteRequest
	orphans   []OrphanRuntime
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
	Pins       map[string]hubruntime.ExecuteRequest `json:"pins,omitempty"`
	Orphans    []OrphanRuntime                      `json:"orphans,omitempty"`
	Generation uint64                               `json:"generation"`
	LeaseSeq   uint64                               `json:"lease_seq"`
	Items      []persistedRuntime                   `json:"items"`
	Jobs       map[string]jobRecord                 `json:"jobs,omitempty"`
}

type persistedRuntime struct {
	Binding Binding          `json:"binding,omitempty"`
	Runtime Runtime          `json:"runtime"`
	Leases  map[string]Lease `json:"leases,omitempty"`
}

type jobRecord struct {
	Dispatching      bool                       `json:"dispatching,omitempty"`
	CancelRequested  bool                       `json:"cancel_requested,omitempty"`
	Controls         map[string]controlRecord   `json:"controls,omitempty"`
	ApprovalDeadline time.Time                  `json:"approval_deadline,omitempty"`
	Request          hubruntime.ExecuteRequest  `json:"request"`
	Fingerprint      string                     `json:"fingerprint"`
	Response         hubruntime.ExecuteResponse `json:"response"`
	Status           string                     `json:"status"`
	Generation       string                     `json:"generation,omitempty"`
	UpdatedAt        time.Time                  `json:"updated_at"`
}

// Serve runs the always-on control-plane endpoint. It never mounts or exposes
// a Docker socket to the communication or Hermes containers.
func Serve(ctx context.Context, manager *Manager, listen string) error {
	if manager == nil || strings.TrimSpace(listen) == "" {
		return errors.New("manager and listen address are required")
	}
	server := &http.Server{Addr: listen, Handler: manager.Handler(), ReadHeaderTimeout: 5 * time.Second, MaxHeaderBytes: 64 * 1024}
	go func() {
		interval := min(manager.cfg.WarmTTL/2, 5*time.Second)
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
				sweepCtx, cancel := context.WithTimeout(ctx, 150*time.Second)
				_ = manager.DetectOrphans(sweepCtx)
				manager.ReapOrphans(sweepCtx)
				manager.ReconcileHealth(sweepCtx, now)
				if sweepCtx.Err() == nil {
					manager.RecoverMissingWork(sweepCtx)
				}
				if sweepCtx.Err() == nil {
					manager.ReconcilePins(sweepCtx)
				}
				if sweepCtx.Err() == nil {
					manager.ExpireApprovals(sweepCtx, now)
					manager.ExpireUncertain(sweepCtx, now)
				}
				if sweepCtx.Err() == nil {
					_ = manager.Reap(sweepCtx, now)
				}
				cancel()
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
		cfg.HTTP = &http.Client{Timeout: 130 * time.Second}
	}
	if cfg.Command == nil {
		cfg.Command = func(ctx context.Context, args ...string) ([]byte, error) {
			return exec.CommandContext(ctx, cfg.Docker, args...).CombinedOutput()
		}
	}
	m := &Manager{pins: map[string]hubruntime.ExecuteRequest{}, cfg: cfg, items: map[string]*runtimeEntry{}, locks: map[string]*sync.Mutex{}, sem: make(chan struct{}, cfg.MaxConcurrent), slots: map[string]struct{}{}, jobs: map[string]jobRecord{}, statePath: filepath.Join(cfg.StateDir, "supervisor.json")}
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
	m.gen, m.leaseSeq, m.orphans = state.Generation, state.LeaseSeq, state.Orphans
	for key, request := range state.Pins {
		if request.Text != "" || request.Envelope.Validate(request.PrincipalID, request.ContextID, request.RuntimeID, request.PolicyVersion) != nil || key != pinKey(request) {
			return errors.New("invalid persisted operator pin")
		}
		m.pins[key] = request
	}
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
		key := runtimeKey(Binding{PrincipalID: item.Runtime.PrincipalID, ContextID: item.Runtime.ContextID, RuntimeMode: item.Runtime.RuntimeMode})
		if _, exists := m.items[key]; exists {
			return errors.New("duplicate persisted runtime context")
		}
		if item.Runtime.State != Stopped {
			select {
			case m.sem <- struct{}{}:
			default:
				return errors.New("persisted active runtimes exceed configured concurrency limit")
			}
			m.slots[key] = struct{}{}
		}
		if item.Binding.PrincipalID != "" && runtimeKey(item.Binding) != key {
			return errors.New("persisted binding mismatch")
		}
		m.items[key] = &runtimeEntry{Runtime: item.Runtime, binding: item.Binding, leases: item.Leases, restored: true}
	}
	if dirty {
		return m.persistLocked()
	}
	return nil
}

func (m *Manager) persistLocked() error {
	state := persistedManager{Pins: m.pins, Generation: m.gen, LeaseSeq: m.leaseSeq, Items: make([]persistedRuntime, 0, len(m.items)), Jobs: m.jobs, Orphans: m.orphans}
	for _, item := range m.items {
		state.Items = append(state.Items, persistedRuntime{Runtime: item.Runtime, Binding: item.binding, Leases: item.leases})
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
	selection, selected, selectionErr := stack.ReadExecution(binding.ContextRoot, envOr("HUB_ENV", "prod"), binding.UserID)
	if selectionErr != nil {
		return Runtime{}, selectionErr
	}
	if selected && selection.Mode != "supervisor" {
		return Runtime{}, errors.New("context selected the legacy executor")
	}
	m.mu.Lock()
	if current := m.items[key]; current != nil && current.RuntimeID != binding.RuntimeID {
		m.mu.Unlock()
		return Runtime{}, errors.New("runtime identity mismatch")
	}
	current := m.items[key]
	lifecycle := map[string]Lease{}
	if current != nil {
		for id, lease := range current.leases {
			if lease.Kind == LeaseLifecycle {
				lifecycle[id] = lease
			}
		}
	}
	if current != nil && (current.State == Degraded || current.State == Stopped || recoverableRuntime(current.Runtime)) && !current.CrashWindowStart.IsZero() && m.cfg.Now().Sub(current.CrashWindowStart) < 10*time.Minute {
		if current.CrashCount >= 3 || m.cfg.Now().Before(current.NextRetryAt) {
			result := current.Runtime
			m.mu.Unlock()
			return result, errors.New("runtime recovery backoff or crash budget exhausted")
		}
	}
	if current != nil && current.restored && current.State != Stopped {
		container, address, auth := current.Container, current.Address, binding.runtimeAuth
		snapshot := current.Runtime
		_, slotHeld := m.slots[key]
		m.mu.Unlock()
		ownershipErr := m.VerifyOwnership(ctx, snapshot)
		if ownershipErr != nil && !errors.Is(ownershipErr, ErrRuntimeMissing) {
			return snapshot, ownershipErr
		}
		out, inspectErr := m.command(ctx, "inspect", "--format", "{{.State.Status}}", container)
		if inspectErr != nil && !errors.Is(ownershipErr, ErrRuntimeMissing) {
			return snapshot, errors.New("restored runtime health unavailable")
		}
		if inspectErr == nil && strings.TrimSpace(string(out)) == "running" {
			if readinessErr := m.ready(ctx, address, auth); readinessErr != nil {
				return snapshot, readinessErr
			}
			if !slotHeld {
				select {
				case m.sem <- struct{}{}:
				case <-ctx.Done():
					return Runtime{}, ctx.Err()
				}
			}
			m.mu.Lock()
			current = m.items[key]
			if current == nil {
				m.mu.Unlock()
				if !slotHeld {
					<-m.sem
				}
				return Runtime{}, errors.New("runtime disappeared during restore")
			}
			previous := current.Runtime
			current.State, current.Leases, current.LastUsed, current.IdleDeadline = Busy, current.Leases+1, m.cfg.Now(), time.Time{}
			m.slots[key] = struct{}{}
			result := current.Runtime
			if err := m.persistLocked(); err != nil {
				current.Runtime = previous
				m.mu.Unlock()
				return snapshot, err
			}
			current.restored, current.auth = false, auth
			m.mu.Unlock()
			return result, nil
		}
		if !errors.Is(ownershipErr, ErrRuntimeMissing) {
			status := strings.TrimSpace(string(out))
			if status != "exited" && status != "dead" {
				return snapshot, errors.New("runtime state is not safely replaceable")
			}
			if err := m.removeOwnedRuntime(ctx, snapshot); err != nil {
				return snapshot, err
			}
		}
		m.mu.Lock()
		current = m.items[key]
		if current != nil {
			previous, previousLeases := current.Runtime, current.leases
			current.State, current.Leases, current.leases = Stopped, 0, map[string]Lease{}
			if err := m.persistLocked(); err != nil {
				current.Runtime, current.leases = previous, previousLeases
				m.mu.Unlock()
				return snapshot, err
			}
			current.restored = false
		}
		m.mu.Unlock()
	} else {
		m.mu.Unlock()
	}
	m.mu.Lock()
	if current := m.items[key]; current != nil && (current.State == Ready || current.State == Idle || current.State == Busy) {
		previous := current.Runtime
		current.State, current.Leases, current.LastUsed, current.IdleDeadline = Busy, current.Leases+1, m.cfg.Now(), time.Time{}
		result := current.Runtime
		if err := m.persistLocked(); err != nil {
			current.Runtime = previous
			m.mu.Unlock()
			return previous, err
		}
		m.mu.Unlock()
		return result, nil
	}
	m.mu.Unlock()
	m.mu.Lock()
	_, slotHeld := m.slots[key]
	if !slotHeld && len(m.orphans) != 0 {
		m.mu.Unlock()
		return Runtime{}, errors.New("owned orphan runtimes require inspection before new capacity")
	}
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
	logical.binding = binding
	for id, lease := range lifecycle {
		lease.Generation = logical.Generation
		logical.leases[id] = lease
		logical.Leases++
	}
	previousEntry := m.items[key]
	if previousEntry != nil && !previousEntry.CrashWindowStart.IsZero() && m.cfg.Now().Sub(previousEntry.CrashWindowStart) < 10*time.Minute {
		logical.CrashCount, logical.CrashWindowStart, logical.NextRetryAt = previousEntry.CrashCount, previousEntry.CrashWindowStart, previousEntry.NextRetryAt
	}
	m.items[key] = logical
	if err = m.persistLocked(); err != nil {
		if previousEntry == nil {
			delete(m.items, key)
		} else {
			m.items[key] = previousEntry
		}
		m.mu.Unlock()
		if !slotHeld {
			m.releaseSlot(key)
		}
		return Runtime{}, errors.New("runtime start state unavailable")
	}
	m.mu.Unlock()
	if out, inspectErr := m.command(ctx, "inspect", "--format", "{{.State.Status}}", container); inspectErr == nil && strings.TrimSpace(string(out)) == "running" {
		if _, ownershipErr := m.ownedContainerID(ctx, logical.Runtime); ownershipErr != nil {
			m.mu.Lock()
			if previousEntry == nil {
				delete(m.items, key)
			} else {
				m.items[key] = previousEntry
			}
			_ = m.persistLocked()
			m.mu.Unlock()
			m.releaseSlot(key)
			return Runtime{}, ownershipErr
		}
		if err = m.ready(ctx, address, binding.runtimeAuth); err == nil {
			m.mu.Lock()
			logical.State = Busy
			result := logical.Runtime
			_ = m.persistLocked()
			m.mu.Unlock()
			return result, nil
		}
		if cleanupErr := m.removeOwnedRuntime(ctx, logical.Runtime); cleanupErr != nil {
			m.markDegraded(key)
			return Runtime{}, cleanupErr
		}
	}
	args, err := m.runArgsWithGeneration(binding, container, port, logical.Generation)
	if err != nil {
		m.markDegraded(key)
		return Runtime{}, fmt.Errorf("prepare runtime launch: %w", err)
	}
	if _, err = m.command(ctx, args...); err != nil {
		m.markDegraded(key)
		// Docker may have created the container before its acknowledgement was lost.
		cleanupErr := m.removeOwnedRuntime(ctx, logical.Runtime)
		if cleanupErr == nil || errors.Is(cleanupErr, ErrRuntimeMissing) {
			m.releaseSlot(key)
		}
		return Runtime{}, fmt.Errorf("start runtime: %w", err)
	}
	if err = m.ready(ctx, address, binding.runtimeAuth); err != nil {
		cleanupErr := m.removeOwnedRuntime(ctx, logical.Runtime)
		m.markDegraded(key)
		if cleanupErr == nil {
			m.releaseSlot(key)
		} else {
			return Runtime{}, cleanupErr
		}
		return Runtime{}, fmt.Errorf("runtime readiness: %w", err)
	}
	m.mu.Lock()
	logical.State = Busy
	result := logical.Runtime
	if err := m.persistLocked(); err != nil {
		logical.restored = true
		m.mu.Unlock()
		return Runtime{}, err
	}
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
	var key string
	for candidate, entry := range m.items {
		if _, ok := entry.leases[leaseID]; ok {
			key = candidate
			break
		}
	}
	m.mu.Unlock()
	if key == "" {
		return errors.New("lease is not registered")
	}
	lock := m.lockFor(key)
	lock.Lock()
	defer lock.Unlock()
	m.mu.Lock()
	defer m.mu.Unlock()
	entry := m.items[key]
	lease, ok := entry.leases[leaseID]
	if !ok {
		return errors.New("lease is not registered")
	}
	if lease.Generation != entry.Generation {
		return errors.New("stale runtime lease")
	}
	if entry.Leases == 0 {
		return errors.New("runtime lease underflow")
	}
	before := entry.Runtime
	delete(entry.leases, leaseID)
	entry.Leases--
	entry.LastUsed = m.cfg.Now()
	if entry.Leases == 0 {
		entry.State, entry.IdleDeadline = Idle, m.cfg.Now().Add(m.cfg.WarmTTL)
	}
	if err := m.persistLocked(); err != nil {
		entry.Runtime = before
		entry.leases[leaseID] = lease
		return err
	}
	return nil
}

func (m *Manager) ReleaseBinding(binding Binding) error {
	binding, err := m.normalize(binding)
	if err != nil {
		return err
	}
	return m.releaseKey(runtimeKey(binding))
}

// sweepStaleLeasesLocked drops holds that can never be released through the
// normal path: leases pinned to a superseded runtime generation (ReleaseLease
// only answers "stale runtime lease" for them) and job-owned leases whose job
// is terminal or gone. Caller-held stream/approval/lifecycle leases have no
// time bound — ExpiresAt is not a validity check for them. Uncertain jobs are
// resolved by ExpireUncertain through the durable control path, not here.
// Without this an orphaned lease keeps runtimeDesiredLocked true forever and
// the runtime is never reaped. m.mu must be held.
func (m *Manager) sweepStaleLeasesLocked(now time.Time) {
	for _, entry := range m.items {
		changed := false
		for id, lease := range entry.leases {
			stale := lease.Generation != entry.Generation
			if !stale {
				if jobID, ok := strings.CutPrefix(lease.Owner, "job:"); ok {
					record, found := m.jobs[jobID]
					stale = !found || terminalRunStatus(record.Status)
				}
			}
			if !stale {
				continue
			}
			delete(entry.leases, id)
			if entry.Leases > 0 {
				entry.Leases--
			}
			changed = true
		}
		if !changed {
			continue
		}
		if entry.Leases == 0 && entry.State == Busy {
			entry.State, entry.IdleDeadline = Idle, m.cfg.Now().Add(m.cfg.WarmTTL)
		}
		_ = m.persistLocked()
	}
}

func (m *Manager) Reap(ctx context.Context, now time.Time) error {
	m.mu.Lock()
	m.sweepStaleLeasesLocked(now)
	keys := []string{}
	for key, runtime := range m.items {
		if runtime.State == Idle && !m.runtimeDesiredLocked(runtime) && runtime.RuntimeHealth != "unknown" && runtime.Ownership != "unverified" && !runtime.IdleDeadline.After(now) {
			keys = append(keys, key)
		}
	}
	m.mu.Unlock()
	for _, key := range keys {
		lock := m.lockFor(key)
		lock.Lock()
		m.mu.Lock()
		runtime := m.items[key]
		if runtime == nil || runtime.State != Idle || m.runtimeDesiredLocked(runtime) || runtime.RuntimeHealth == "unknown" || runtime.Ownership == "unverified" || runtime.IdleDeadline.After(now) {
			m.mu.Unlock()
			lock.Unlock()
			continue
		}
		snapshot := runtime.Runtime
		m.mu.Unlock()
		containerID, ownershipErr := m.ownedContainerID(ctx, snapshot)
		if errors.Is(ownershipErr, ErrRuntimeMissing) {
			m.mu.Lock()
			if !m.runtimeDesiredLocked(runtime) && runtime.Generation == snapshot.Generation {
				runtime.State, runtime.IdleDeadline, runtime.Ownership = Stopped, time.Time{}, "absent"
				if err := m.persistLocked(); err != nil {
					runtime.Runtime = snapshot
					m.mu.Unlock()
					lock.Unlock()
					return err
				}
				m.releaseSlotLocked(key)
			}
			m.mu.Unlock()
			lock.Unlock()
			continue
		}
		if ownershipErr != nil {
			m.mu.Lock()
			runtime.Ownership = "unverified"
			_ = m.persistLocked()
			m.mu.Unlock()
			lock.Unlock()
			return ownershipErr
		}
		m.mu.Lock()
		if m.runtimeDesiredLocked(runtime) || runtime.Generation != snapshot.Generation {
			m.mu.Unlock()
			lock.Unlock()
			continue
		}
		runtime.State = Stopping
		container := containerID
		m.mu.Unlock()
		_, err := m.command(ctx, "stop", "--time", "30", container)
		if err == nil {
			_, err = m.command(ctx, "rm", "-f", container)
		}
		m.mu.Lock()
		previous := snapshot
		if err != nil {
			runtime.State = Degraded
		} else {
			runtime.State, runtime.IdleDeadline = Stopped, time.Time{}
		}
		persistErr := m.persistLocked()
		if persistErr != nil {
			runtime.Runtime = previous
		} else if err == nil {
			m.releaseSlotLocked(key)
		}
		m.mu.Unlock()
		if persistErr != nil {
			lock.Unlock()
			return persistErr
		}
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
	case "/v1/execute", "/v1/jobs":
		m.execute(w, r)
	case "/v1/resume":
		m.resume(w, r)
	case "/v1/control":
		m.control(w, r)
	case "/v1/pins":
		m.pinHTTP(w, r)
	case "/v1/runtimes":
		m.list(w, r)
	case "/v1/leases":
		m.lease(w, r)
	case "/v1/self-env":
		m.selfEnv(w, r)
	case "/v1/restart":
		m.restartHTTP(w, r)
	default:
		if strings.HasPrefix(r.URL.Path, "/v1/jobs/") {
			m.jobStatus(w, r, strings.TrimPrefix(r.URL.Path, "/v1/jobs/"))
			return
		}
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
		log.Printf("acquire failed for %s/%s: %v", binding.PrincipalID, binding.ContextID, err)
		writeJSON(w, http.StatusServiceUnavailable, map[string]string{"error": err.Error()})
		return
	}
	writeJSON(w, http.StatusCreated, struct {
		Lease   Lease   `json:"lease"`
		Runtime Runtime `json:"runtime"`
	}{lease, runtime})
}

// restartHTTP forwards a runtime restart request to the runtime owning the
// request's binding. A stopped or absent runtime needs no restart: the next
// Acquire spawns a fresh one, so the handler answers success without spawning
// a container just to bounce it.
func (m *Manager) restartHTTP(w http.ResponseWriter, r *http.Request) {
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
	m.mu.Lock()
	entry := m.items[runtimeKey(binding)]
	running := entry != nil && (entry.State == Ready || entry.State == Busy || entry.State == Idle) && entry.Container != ""
	m.mu.Unlock()
	if !running {
		w.WriteHeader(http.StatusOK)
		return
	}
	lease, runtime, err := m.Acquire(r.Context(), binding, LeaseLifecycle)
	if err != nil {
		writeJSON(w, http.StatusServiceUnavailable, map[string]string{"error": "runtime unavailable"})
		return
	}
	defer m.ReleaseLease(lease.ID)
	upstream, err := http.NewRequestWithContext(r.Context(), http.MethodPost, runtime.Address+"/v1/restart", nil)
	if err != nil {
		writeJSON(w, http.StatusBadGateway, map[string]string{"error": "runtime unavailable"})
		return
	}
	upstream.Header.Set("Authorization", "Bearer "+binding.runtimeAuth)
	response, err := m.cfg.HTTP.Do(upstream)
	if err != nil {
		writeJSON(w, http.StatusBadGateway, map[string]string{"error": "runtime unavailable"})
		return
	}
	defer response.Body.Close()
	_, _ = io.Copy(io.Discard, io.LimitReader(response.Body, 64*1024))
	w.WriteHeader(response.StatusCode)
}

func (m *Manager) selfEnv(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeJSON(w, http.StatusMethodNotAllowed, map[string]string{"error": "method not allowed"})
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, 128*1024)
	defer r.Body.Close()
	body, err := io.ReadAll(r.Body)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid request"})
		return
	}
	defer clear(body)
	var request hubruntime.SelfEnvRequest
	if json.Unmarshal(body, &request) != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid request"})
		return
	}
	job := hubruntime.ExecuteRequest{Envelope: request.Envelope, OrganizationID: request.OrganizationID, UserID: request.UserID, ActorID: request.ActorID, ScopeID: request.ScopeID, Channel: "communication", Trigger: "credential-form", JobID: "credential-form", IdempotencyKey: "credential-form", Text: "protected credential form"}
	binding, err := m.bindingFor(job)
	if err != nil {
		writeJSON(w, http.StatusConflict, map[string]string{"error": "invalid runtime scope"})
		return
	}
	lease, runtime, err := m.Acquire(r.Context(), binding, LeaseLifecycle)
	if err != nil {
		writeJSON(w, http.StatusServiceUnavailable, map[string]string{"error": "runtime unavailable"})
		return
	}
	defer m.ReleaseLease(lease.ID)
	upstream, err := http.NewRequestWithContext(r.Context(), http.MethodPost, runtime.Address+"/v1/self-env", bytes.NewReader(body))
	if err != nil {
		writeJSON(w, http.StatusBadGateway, map[string]string{"error": "runtime unavailable"})
		return
	}
	upstream.Header.Set("Authorization", "Bearer "+binding.runtimeAuth)
	upstream.Header.Set("Content-Type", "application/json")
	response, err := m.cfg.HTTP.Do(upstream)
	if err != nil {
		writeJSON(w, http.StatusBadGateway, map[string]string{"error": "runtime unavailable"})
		return
	}
	defer response.Body.Close()
	result, err := io.ReadAll(io.LimitReader(response.Body, 128*1024+1))
	if err != nil || len(result) > 128*1024 {
		writeJSON(w, http.StatusBadGateway, map[string]string{"error": "runtime response unavailable"})
		return
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(response.StatusCode)
	_, _ = w.Write(result)
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
		if terminalRunStatus(previous.Status) {
			return previous.Response, true, nil
		}
		if previous.Status == "uncertain" {
			return previous.Response, true, nil
		}
		return hubruntime.ExecuteResponse{}, false, errJobInProgress
	}
	metadata := request
	metadata.Text = ""
	m.jobs[key] = jobRecord{Request: metadata, Fingerprint: fingerprint, Status: "running", UpdatedAt: time.Now().UTC()}
	if err := m.persistLocked(); err != nil {
		delete(m.jobs, key)
		return hubruntime.ExecuteResponse{}, false, err
	}
	return hubruntime.ExecuteResponse{}, false, nil
}

func (m *Manager) finishJob(request hubruntime.ExecuteRequest, response hubruntime.ExecuteResponse, status string) {
	m.finishJobGeneration(request, response, status, "")
}

// abandonJob removes a job that failed before dispatch. Acquire errors mean no
// runtime ever saw the job, so a retry must re-admit it rather than replay a
// synthetic terminal result. Records bound to a generation or already past
// "running" are left untouched.
func (m *Manager) abandonJob(request hubruntime.ExecuteRequest) {
	key := executeJobKey(request)
	if key == "" {
		return
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	record, ok := m.jobs[key]
	if !ok || record.Status != "running" || record.Generation != "" || record.Response.RunID != "" {
		return
	}
	delete(m.jobs, key)
	_ = m.persistLocked()
}

func (m *Manager) bindJobGeneration(request hubruntime.ExecuteRequest, generation string) error {
	key := executeJobKey(request)
	m.mu.Lock()
	defer m.mu.Unlock()
	record, ok := m.jobs[key]
	if !ok {
		return errors.New("job is not registered")
	}
	if record.Status != "running" && !(record.Status == "cancelled" && !record.Dispatching && record.Response.RunID == "") {
		return errors.New("job cannot bind generation")
	}
	previous := record
	record.Generation, record.UpdatedAt = generation, time.Now().UTC()
	if record.Status == "cancelled" {
		record.Response.RuntimeGeneration = generation
	}
	m.jobs[key] = record
	if err := m.persistLocked(); err != nil {
		m.jobs[key] = previous
		return err
	}
	return nil
}

func (m *Manager) finishJobGeneration(request hubruntime.ExecuteRequest, response hubruntime.ExecuteResponse, status, generation string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.finishJobGenerationLocked(request, response, status, generation)
}

func (m *Manager) finishJobGenerationLocked(request hubruntime.ExecuteRequest, response hubruntime.ExecuteResponse, status, generation string) error {
	key := executeJobKey(request)
	if key == "" {
		return errors.New("job identity is required")
	}
	if record, ok := m.jobs[key]; ok {
		if terminalRunStatus(record.Status) && status != record.Status {
			return errors.New("job already terminal")
		}
		if generation != "" && record.Generation != generation {
			return errors.New("stale job generation")
		}
		current := m.items[runtimeKey(Binding{PrincipalID: request.PrincipalID, ContextID: request.ContextID, RuntimeMode: "gateway"})]
		if current != nil && current.Generation != record.Generation && (record.Response.RunID != "" || response.RunID != "") {
			return errors.New("obsolete runtime producer")
		}
		if (response.JobID != "" && response.JobID != request.JobID) || (response.RuntimeGeneration != "" && response.RuntimeGeneration != record.Generation) || (record.Response.RunID != "" && response.RunID != "" && record.Response.RunID != response.RunID) || (record.Response.SessionID != "" && response.SessionID != "" && record.Response.SessionID != response.SessionID) {
			return errors.New("job response binding mismatch")
		}
		if terminalRunStatus(record.Status) {
			if response.Text != "" && response.Text != record.Response.Text {
				return errors.New("terminal result cannot change")
			}
			return nil
		}
		if response.RunID == "" {
			response.RunID = record.Response.RunID
		}
		if response.SessionID == "" {
			response.SessionID = record.Response.SessionID
		}
		previous := record
		if terminalRunStatus(status) {
			controls := make(map[string]controlRecord, len(record.Controls))
			for id, control := range record.Controls {
				if control.State == "uncertain" {
					control.State = "closed"
					control.Response = response
				}
				controls[id] = control
			}
			record.Controls = controls
		}
		var approvalEntry *runtimeEntry
		var previousRuntime Runtime
		var previousLeases map[string]Lease
		for _, entry := range m.items {
			if entry.Generation != record.Generation {
				continue
			}
			approvalEntry = entry
			previousRuntime = entry.Runtime
			previousLeases = entry.leases
			entry.leases = make(map[string]Lease, len(previousLeases)+1)
			for id, lease := range previousLeases {
				entry.leases[id] = lease
			}
			if response.ApprovalID != "" {
				id := "approval:" + key + ":" + response.ApprovalID
				if _, exists := entry.leases[id]; !exists {
					deadline := record.ApprovalDeadline
					if response.ApprovalID != record.Response.ApprovalID {
						deadline = time.Now().UTC().Add(2 * time.Minute)
					}
					entry.leases[id] = Lease{ID: id, Kind: LeaseApproval, PrincipalID: entry.PrincipalID, ContextID: entry.ContextID, RuntimeMode: entry.RuntimeMode, Generation: entry.Generation, Owner: "job:" + key, ExpiresAt: deadline}
					entry.Leases++
					entry.State, entry.IdleDeadline = Busy, time.Time{}
				}
			}
			{
				for id, lease := range entry.leases {
					obsolete := response.ApprovalID != "" && id != "approval:"+key+":"+response.ApprovalID
					release := terminalRunStatus(status) || (status == "running" && record.ApprovalDeadline.IsZero()) || obsolete
					if release && lease.Kind == LeaseApproval && lease.Owner == "job:"+key {
						delete(entry.leases, id)
						entry.Leases--
					}
				}
				if entry.Leases == 0 {
					entry.State, entry.IdleDeadline = Idle, m.cfg.Now().Add(m.cfg.WarmTTL)
				}
			}
			break
		}
		if response.ApprovalID != "" && response.ApprovalID != record.Response.ApprovalID {
			record.ApprovalDeadline = time.Now().UTC().Add(2 * time.Minute)
		}
		record.Response, record.Status, record.UpdatedAt = response, status, time.Now().UTC()
		m.jobs[key] = record
		if err := m.persistLocked(); err != nil {
			m.jobs[key] = previous
			if approvalEntry != nil {
				approvalEntry.Runtime = previousRuntime
				approvalEntry.leases = previousLeases
			}
			return err
		}
		return nil
	}
	return errors.New("job is not registered")
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
	if request.Trigger == "cron" {
		if err := m.authorizeCurrentPolicy(request); err != nil {
			writeJSON(w, 409, map[string]string{"error": "scheduled job current policy rejected"})
			return
		}
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
	kind := LeaseJob
	if r.Header.Get("Accept") == hubruntime.RunStreamContentType {
		kind = LeaseStream
	}
	lease, runtime, err := m.Acquire(r.Context(), binding, kind)
	if err != nil {
		// The job never reached a runtime: drop the record so a retry re-admits
		// it instead of replaying a synthetic "uncertain" result. The cause is
		// logged host-side; the caller only gets that the runtime is unavailable.
		log.Printf("acquire failed for %s/%s: %v", binding.PrincipalID, binding.ContextID, err)
		m.abandonJob(request)
		writeJSON(w, http.StatusServiceUnavailable, map[string]string{"error": "runtime unavailable"})
		return
	}
	if err := m.bindJobGeneration(request, runtime.Generation); err != nil {
		_ = m.ReleaseLease(lease.ID)
		writeJSON(w, 500, map[string]string{"error": "job generation state unavailable"})
		return
	}
	if err := m.attachJobLease(lease.ID, executeJobKey(request)); err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "lease state unavailable"})
		return
	}
	defer m.settleJobLeases(request, lease)
	m.mu.Lock()
	latest := m.jobs[executeJobKey(request)]
	if terminalRunStatus(latest.Status) {
		m.mu.Unlock()
		writeJSON(w, 200, latest.Response)
		return
	}
	previous := latest
	latest.Dispatching = true
	m.jobs[executeJobKey(request)] = latest
	err = m.persistLocked()
	if err != nil {
		m.jobs[executeJobKey(request)] = previous
	}
	m.mu.Unlock()
	if err != nil {
		writeJSON(w, 500, map[string]string{"error": "dispatch state unavailable"})
		return
	}
	req, err := http.NewRequestWithContext(r.Context(), http.MethodPost, runtime.Address+"/v1/execute", bytes.NewReader(body))
	if err != nil {
		uncertain := hubruntime.ExecuteResponse{JobID: request.JobID, RuntimeGeneration: runtime.Generation, Status: "uncertain", LastEvent: "run.unknown"}
		m.finishJobGeneration(request, uncertain, "uncertain", runtime.Generation)
		writeJSON(w, http.StatusBadGateway, uncertain)
		return
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+binding.runtimeAuth)
	req.Header.Set("Accept", r.Header.Get("Accept"))
	response, err := m.cfg.HTTP.Do(req)
	if err != nil {
		uncertain := hubruntime.ExecuteResponse{JobID: request.JobID, RuntimeGeneration: runtime.Generation, Status: "uncertain", LastEvent: "run.unknown"}
		m.finishJobGeneration(request, uncertain, "uncertain", runtime.Generation)
		writeJSON(w, http.StatusBadGateway, uncertain)
		return
	}
	defer response.Body.Close()
	if response.StatusCode == http.StatusOK && response.Header.Get("Content-Type") == hubruntime.RunStreamContentType {
		m.relayRunStream(w, request, runtime, response.Body)
		return
	}
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

func terminalRunStatus(status string) bool {
	return status == "completed" || status == "failed" || status == "cancelled" || status == "interrupted"
}

func (m *Manager) relayRunStream(w http.ResponseWriter, request hubruntime.ExecuteRequest, runtime Runtime, reader io.Reader) {
	w.Header().Set("Content-Type", hubruntime.RunStreamContentType)
	known := hubruntime.ExecuteResponse{JobID: request.JobID, RuntimeGeneration: runtime.Generation, Status: "uncertain", LastEvent: "run.unknown"}
	err := hubruntime.ReadRunStream(reader, func(event hubruntime.ExecuteResponse) error {
		if event.JobID != request.JobID || event.RuntimeGeneration != runtime.Generation || (known.RunID != "" && event.RunID != known.RunID) || (known.SessionID != "" && event.SessionID != known.SessionID) {
			return errors.New("stream identity mismatch")
		}
		m.mu.Lock()
		current := m.items[runtimeKey(Binding{PrincipalID: runtime.PrincipalID, ContextID: runtime.ContextID, RuntimeMode: runtime.RuntimeMode})]
		valid := current != nil && current.Generation == runtime.Generation
		m.mu.Unlock()
		if !valid {
			return errors.New("stale stream generation")
		}
		if err := m.finishJobGeneration(request, event, event.Status, runtime.Generation); err != nil {
			return err
		}
		known = event
		if err := json.NewEncoder(w).Encode(event); err != nil {
			return err
		}
		return http.NewResponseController(w).Flush()
	})
	if !terminalRunStatus(known.Status) {
		known.Status, known.LastEvent, known.Text = "uncertain", "run.unknown", ""
		m.finishJobGeneration(request, known, "uncertain", runtime.Generation)
	}
	_ = err // A durably recorded terminal event remains authoritative after a later transport error.
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
	if err := request.Envelope.Validate(request.PrincipalID, request.ContextID, request.RuntimeID, request.PolicyVersion); err != nil {
		return Binding{}, err
	}

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
	if !validID(binding.PrincipalID) || !validID(binding.ContextID) || !validID(binding.RuntimeID) || !validID(binding.UserID) {
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
	for _, file := range []string{"runtime." + envOr("HUB_ENV", "prod") + ".env", "hermes." + envOr("HUB_ENV", "prod") + ".yaml", "SOUL.md"} {
		info, err := os.Lstat(filepath.Join(abs, file))
		if err != nil && !errors.Is(err, os.ErrNotExist) {
			return Binding{}, errors.New("runtime input file unavailable")
		}
		if err == nil && !info.Mode().IsRegular() {
			return Binding{}, errors.New("runtime input file is not an owned regular file")
		}
	}
	binding.runtimeAuth, err = envFileAuth(envFile)
	if err != nil {
		return Binding{}, err
	}
	// Match static Compose data paths without allowing a symlink to another home.
	for _, name := range []string{"runtime", "hermes", "connections", "connections/google", "connections/telegram", "connections/browser", "home", "cache", "workspace", "archive"} {
		path := filepath.Join(abs, filepath.FromSlash(name))
		if err := os.MkdirAll(path, 0770); err != nil {
			return Binding{}, errors.New("context data directory unavailable")
		}
		if info, err := os.Lstat(path); err != nil || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
			return Binding{}, errors.New("context data directory is not an owned directory")
		}
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

func (m *Manager) runArgs(binding Binding, container string, port int) ([]string, error) {
	return m.runArgsWithGeneration(binding, container, port, "")
}

func (m *Manager) runArgsWithGeneration(binding Binding, container string, port int, generation string) ([]string, error) {
	env := envOr("HUB_ENV", "prod")
	project := "hermes-hub-" + binding.UserID + "-" + env
	// Spawned runtimes join the single shared runtime network where the shared
	// control plane (toolhub, credential-broker, cliproxy, communication-hub)
	// resolves; per-user isolation lives in mounts and principal-scoped auth.
	args := []string{"run", "-d", "--name", container, "--network", m.cfg.Network, "--restart=no", "--read-only", "--init", "--user", "10001:10001", "--cap-drop", "ALL", "--security-opt", "no-new-privileges:true", "--pids-limit", strconv.Itoa(m.cfg.PIDs), "--memory", m.cfg.Memory, "--cpus", m.cfg.CPU, "-p", fmt.Sprintf("127.0.0.1:%d:%d", port, m.cfg.RuntimePort)}
	if info, err := os.Lstat(filepath.Join(binding.ContextRoot, "runtime."+env+".env")); err == nil && info.Mode().IsRegular() {
		args = append(args, "--env-file", filepath.Join(binding.ContextRoot, "runtime."+env+".env"))
	}
	args = append(args, "--env-file", binding.EnvFile)
	args = append(args, "--mount", "type=volume,src="+project+"_broker-secrets-runtime,dst=/run/broker-secrets,readonly")
	args = append(args, "--mount", "type=bind,src="+binding.ContextRoot+",dst=/scope,readonly")
	if settings, err := stack.ReadEnvironment(binding.ContextRoot, env); err == nil {
		service := stack.RuntimeService(settings, "", binding.ContextRoot)
		keys := make([]string, 0)
		for key := range service["environment"].(stack.M) {
			keys = append(keys, key)
		}
		sort.Strings(keys)
		for _, key := range keys {
			args = append(args, "-e", key+"="+fmt.Sprint(service["environment"].(stack.M)[key]))
		}
	}
	for _, mount := range []struct{ source, target string }{{"runtime", "/state"}, {"hermes", "/state/hermes"}, {"connections/google", "/state/google"}, {"connections/telegram", "/state/telegram"}, {"connections/browser", "/state/browser"}, {"home", "/state/home"}, {"cache", "/state/cache"}, {"workspace", "/workspace"}, {"archive", "/archive"}} {
		value := "type=bind,src=" + filepath.Join(binding.ContextRoot, filepath.FromSlash(mount.source)) + ",dst=" + mount.target
		if mount.target == "/archive" {
			value += ",readonly"
		}
		args = append(args, "--mount", value)
	}
	// Mirror the rendered runtime contract: shipped hub skills (connector
	// onboarding rules) must be readable at /opt/hub/skills, matching
	// external_dirs in the generated Hermes config. Without them the agent
	// improvises installs through terminal instead of ToolHub control ops.
	skillsDir := ""
	if settings, err := stack.Read(filepath.Join(binding.ContextRoot, "settings.yaml")); err == nil {
		skillsDir = strings.TrimSpace(settings.GlobalSkillsDir)
	}
	if skillsDir == "" {
		skillsDir = filepath.Join(filepath.Dir(filepath.Dir(binding.ContextRoot)), "config", "skills")
	}
	if info, err := os.Stat(skillsDir); err == nil && info.IsDir() {
		args = append(args, "--mount", "type=bind,src="+skillsDir+",dst=/opt/hub/skills,readonly")
	}
	// Materialize the effective Hermes config host-side and mount it read-only
	// over the agent-writable state dir: mcp_servers must come from ToolHub
	// onboarding, never from terminal edits inside the runtime.
	effectiveConfig, err := m.materializeHermesConfig(binding, env)
	if err != nil {
		return nil, err
	}
	args = append(args, "--mount", "type=bind,src="+effectiveConfig+",dst=/state/hermes/config.yaml,readonly")
	args = append(args, "--label", "hermes-hub.owner="+m.ownerID(), "--label", "hermes-hub.context="+hex.EncodeToString(hashBytes(runtimeKey(binding))), "--label", "hermes-hub.generation="+generation)
	if binding.OrganizationRoot != "" {
		args = append(args, "--mount", "type=bind,src="+binding.OrganizationRoot+",dst=/org,readonly")
	}
	for _, file := range []struct{ source, target string }{{filepath.Join(binding.ContextRoot, "hermes."+envOr("HUB_ENV", "prod")+".yaml"), "/config/config.yaml"}, {filepath.Join(binding.ContextRoot, "SOUL.md"), "/config/SOUL.md"}} {
		if _, err := os.Stat(file.source); err == nil {
			args = append(args, "--mount", "type=bind,src="+file.source+",dst="+file.target+",readonly")
		}
	}
	args = append(args, "--add-host", "host.docker.internal:host-gateway", "--tmpfs", "/tmp:uid=10001,gid=10001,mode=1777", "--shm-size", "1gb")
	args = append(args, "-e", "HUB_RUNTIME_LISTEN=0.0.0.0:"+strconv.Itoa(m.cfg.RuntimePort), "-e", "HUB_STATE=/state", "-e", "HUB_WORKSPACE=/workspace", "-e", "HERMES_HOME=/state/hermes", "-e", "HOME=/state/home", "-e", "HUB_USER_ID="+binding.UserID, "-e", "HUB_ORGANIZATION_ID="+binding.OrganizationID, "-e", "HUB_RUNTIME_ID="+binding.RuntimeID, "-e", "HUB_POLICY_VERSION="+binding.PolicyVersion, "-e", "API_SERVER_ENABLED=true", "-e", "API_SERVER_HOST=127.0.0.1", "-e", "API_SERVER_PORT=8642")
	if generation != "" {
		args = append(args, "-e", "HUB_RUNTIME_GENERATION="+generation)
	}
	args = append(args, m.cfg.Image, "serve")
	return args, nil
}

// materializeHermesConfig renders the effective Hermes config on the host so
// the spawned runtime mounts it read-only. Inputs mirror the rendered
// runtime contract: secrets.<env>.env endpoint override and self-services.
func (m *Manager) materializeHermesConfig(binding Binding, env string) (string, error) {
	source := filepath.Join(binding.ContextRoot, "hermes."+env+".yaml")
	dest := filepath.Join(binding.ContextRoot, "generated", "hermes-effective."+env+".yaml")
	secrets, _ := stack.ReadSecrets(filepath.Join(binding.ContextRoot, "runtime."+env+".env"))
	tokenEnv := strings.TrimSpace(secrets["HUB_TOOLHUB_TOKEN_ENV"])
	if tokenEnv == "" {
		tokenEnv = "HUB_RUNTIME_AUTH"
	}
	authPresent := strings.TrimSpace(secrets[tokenEnv]) != ""
	envFile := binding.EnvFile
	if envFile == "" {
		envFile = filepath.Join(binding.ContextRoot, "runtime.auth")
	}
	if !authPresent {
		if envSecrets, err := stack.ReadSecrets(envFile); err == nil && strings.TrimSpace(envSecrets[tokenEnv]) != "" {
			authPresent = true
		}
	}
	opts := stack.MaterializeOptions{
		ToolHubEndpoint:    strings.TrimSpace(secrets["HUB_TOOLHUB_ENDPOINT"]),
		ToolHubTokenEnv:    tokenEnv,
		RuntimeAuthPresent: authPresent,
		ToolHubReconnect:   !strings.EqualFold(strings.TrimSpace(secrets["HUB_TOOLHUB_RECONNECT"]), "false"),
		SelfServicesPath:   filepath.Join(binding.ContextRoot, "runtime", "self-services.json"),
	}
	if _, present := secrets["HUB_TOOLHUB_ENDPOINT"]; !present {
		opts.ToolHubEndpoint = "http://toolhub:8090/mcp"
	}
	if err := stack.MaterializeHermesConfig(source, dest, opts); err != nil {
		return "", fmt.Errorf("materialize Hermes config: %w", err)
	}
	return dest, nil
}

func (m *Manager) ready(ctx context.Context, address, auth string) error {
	if m.cfg.Probe != nil {
		return m.cfg.Probe(ctx, address, auth)
	}
	// Cold starts include container create, browser/X session boot and Hermes
	// gateway warmup; under host load they can exceed 90s. A longer bound keeps
	// wake-up jobs alive instead of failing them while the runtime is booting.
	deadline := time.Now().Add(4 * time.Minute)
	for time.Now().Before(deadline) {
		probeCtx, cancel := context.WithTimeout(ctx, 2*time.Second)
		req, _ := http.NewRequestWithContext(probeCtx, http.MethodGet, address+"/readyz", nil)
		req.Header.Set("Authorization", "Bearer "+auth)
		response, err := m.cfg.HTTP.Do(req)
		if err == nil {
			n, copyErr := io.Copy(io.Discard, io.LimitReader(response.Body, 64*1024+1))
			_ = response.Body.Close()
			cancel()
			if response.StatusCode == http.StatusOK && copyErr == nil && n <= 64*1024 {
				return nil
			}
		} else {
			cancel()
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
	callCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	return m.cfg.Command(callCtx, args...)
}
func (m *Manager) releaseSlot(key string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.releaseSlotLocked(key)
}
func (m *Manager) releaseSlotLocked(key string) {
	if _, ok := m.slots[key]; ok {
		delete(m.slots, key)
		<-m.sem
	}
}

func (m *Manager) markDegraded(key string) {
	m.mu.Lock()
	if runtime := m.items[key]; runtime != nil {
		runtime.restored = true
		runtime.State = Degraded
		runtime.Leases = len(runtime.leases)
		m.recordRuntimeFailureLocked(&runtime.Runtime)
		_ = m.persistLocked()
	}
	m.mu.Unlock()
}

func (m *Manager) recordRuntimeFailureLocked(runtime *Runtime) {
	runtime.FailureGeneration = runtime.Generation
	if runtime.CrashWindowStart.IsZero() || m.cfg.Now().Sub(runtime.CrashWindowStart) >= 10*time.Minute {
		runtime.CrashWindowStart = m.cfg.Now()
		runtime.CrashCount = 0
	}
	runtime.CrashCount++
	shift := runtime.CrashCount
	if shift > 3 {
		shift = 3
	}
	runtime.NextRetryAt = m.cfg.Now().Add(time.Duration(1<<shift) * time.Second)
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
