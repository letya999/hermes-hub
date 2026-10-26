package supervisor

import (
	"context"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"github.com/letya999/hermes-hub/internal/diagnostics"
	hubruntime "github.com/letya999/hermes-hub/internal/runtime"
	"github.com/letya999/hermes-hub/internal/stack"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"
)

// Package supervisor owns the host-side scale-to-zero runtime lifecycle.
// It is deliberately small: Docker is the only compute backend and the
// pinned runtime remains the source of truth for Hermes state.

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

// A lease is the persisted record of one lifecycle consumer's demand on a
// runtime generation: who holds it, under which trusted scope and why, how
// long the hold is valid, and the last health observed while it was held.
// The fields stay backend-neutral — nothing here encodes Docker semantics —
// so the same consumer registry maps onto k3s or microVM supervisors later.
type Lease struct {
	ID             string    `json:"id"`
	Kind           LeaseKind `json:"kind"`
	PrincipalID    string    `json:"principal_id"`
	ContextID      string    `json:"context_id"`
	RuntimeMode    string    `json:"runtime_mode"`
	Generation     string    `json:"generation"`
	Owner          string    `json:"owner,omitempty"`
	Scope          string    `json:"scope,omitempty"`
	Reason         string    `json:"reason,omitempty"`
	ObservedHealth string    `json:"observed_health,omitempty"`
	RenewedAt      time.Time `json:"renewed_at,omitempty"`
	ExpiresAt      time.Time `json:"expires_at,omitempty"`
}

// LeaseConsumer is the allowlisted status projection of a lease: identity and
// lifecycle only, never request payloads, auth material or environment data.
type LeaseConsumer struct {
	ID        string    `json:"id"`
	Kind      LeaseKind `json:"kind"`
	Owner     string    `json:"owner,omitempty"`
	Scope     string    `json:"scope,omitempty"`
	Reason    string    `json:"reason,omitempty"`
	ExpiresAt time.Time `json:"expires_at,omitempty"`
}

// DefaultWarmTTL is the project idle-retention default; a shorter window
// drops runtimes between messages and forces cold starts mid-conversation.
const DefaultWarmTTL = 30 * time.Minute

type Config struct {
	Docker         string
	Image          string
	Network        string
	SpacesRoot     string
	HostSpacesRoot string
	StateDir       string
	RuntimeAuth    string
	AdminAuth      string
	Environment    string
	WarmTTL        time.Duration
	MaxConcurrent  int
	// MaxPerPrincipal bounds live runtimes owned by one principal; zero
	// leaves the global MaxConcurrent semaphore as the only capacity cap.
	MaxPerPrincipal int
	CPU             string
	Memory          string
	PIDs            int
	RuntimePort     int
	PortBase        int
	PortRange       int
	HTTP            *http.Client
	// StreamHTTP serves run-stream calls: no total timeout, bounded header
	// wait; the request context carries the run budget and the body an idle
	// deadline.
	StreamHTTP *http.Client
	Probe      func(context.Context, string, string) error
	Command    func(context.Context, ...string) ([]byte, error)
	// Stats samples resource counters for owned containers in one bounded
	// call per watchdog sweep. A nil sampler records nothing; failures are
	// observations, never authority to stop anything.
	Stats func(context.Context, []string) (map[string]ContainerStat, error)
	Now   func() time.Time
	// Decide is the personal-supervisor model seam (#328): one bounded,
	// allowlisted request per coalesced wake, server-validated decisions.
	// Nil disables the reviewer entirely — watchdog and containment never
	// depend on it.
	Decide func(context.Context, PersonalRequest) (PersonalDecision, error)
	// AllowWildcardListen permits Serve to bind a non-loopback control-plane
	// address; without it wildcard or empty hosts are refused.
	AllowWildcardListen bool
}

type Binding struct {
	PrincipalID      string
	ContextID        string
	RuntimeID        string
	RuntimeMode      string
	ScopeID          string
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
	// R1.4-R1.6 observations persisted from the runtime's own surface probe.
	// Reporting only: they never feed remediation decisions or grants.
	ToolSurface  string            `json:"tool_surface,omitempty"`
	MissingTools []string          `json:"missing_tools,omitempty"`
	ToolReasons  map[string]string `json:"tool_reasons,omitempty"`
	ModelStatus  string            `json:"model_status,omitempty"`
	Restarts     int               `json:"restarts,omitempty"`
	Desired      bool              `json:"desired"`
	PrincipalID  string            `json:"principal_id"`
	ContextID    string            `json:"context_id"`
	RuntimeID    string            `json:"runtime_id"`
	RuntimeMode  string            `json:"runtime_mode"`
	Generation   string            `json:"generation"`
	Container    string            `json:"container"`
	Address      string            `json:"address"`
	State        State             `json:"state"`
	ImageSource  string            `json:"image_source,omitempty"`
	Leases       int               `json:"leases"`
	LastUsed     time.Time         `json:"last_used"`
	IdleDeadline time.Time         `json:"idle_deadline,omitempty"`
	Managed      bool              `json:"managed,omitempty"`
	// Consumers and WaitReason are read projections, never persisted state:
	// they describe live demand at read time and stay empty on disk.
	Consumers  []LeaseConsumer `json:"consumers,omitempty"`
	WaitReason string          `json:"wait_reason,omitempty"`
	// Resource counters sampled once per watchdog sweep; reporting only, they
	// never feed remediation decisions.
	CPUPercent float64 `json:"cpu_percent,omitempty"`
	MemBytes   int64   `json:"mem_bytes,omitempty"`
}

type runtimeEntry struct {
	Runtime
	binding              Binding
	auth                 string
	leases               map[string]Lease
	incidents            map[string]Incident
	restored             bool
	consecutiveUncertain int
	readinessBadSince    time.Time
	lastRemediatedAt     time.Time
	personalRestart      bool
}

type Manager struct {
	holds           map[string]AdministrativeHold
	pins            map[string]hubruntime.ExecuteRequest
	orphans         []OrphanRuntime
	cfg             Config
	mu              sync.Mutex
	items           map[string]*runtimeEntry
	locks           map[string]*sync.Mutex
	sem             chan struct{}
	slots           map[string]struct{}
	gen             uint64
	leaseSeq        uint64
	statePath       string
	jobs            map[string]jobRecord
	watchdog        WatchdogMetrics
	personalPending map[string]*personalWake
	personalRunning map[string]bool
	wakeNotify      chan struct{}
}

type persistedManager struct {
	Holds      map[string]AdministrativeHold        `json:"holds,omitempty"`
	Pins       map[string]hubruntime.ExecuteRequest `json:"pins,omitempty"`
	Orphans    []OrphanRuntime                      `json:"orphans,omitempty"`
	Generation uint64                               `json:"generation"`
	LeaseSeq   uint64                               `json:"lease_seq"`
	Items      []persistedRuntime                   `json:"items"`
	Jobs       map[string]jobRecord                 `json:"jobs,omitempty"`
}

type persistedRuntime struct {
	Binding   Binding             `json:"binding,omitempty"`
	Runtime   Runtime             `json:"runtime"`
	Leases    map[string]Lease    `json:"leases,omitempty"`
	Incidents map[string]Incident `json:"incidents,omitempty"`
}

type jobRecord struct {
	OperationalEpoch uint64                     `json:"operational_epoch,omitempty"`
	Dispatching      bool                       `json:"dispatching,omitempty"`
	CancelRequested  bool                       `json:"cancel_requested,omitempty"`
	Controls         map[string]controlRecord   `json:"controls,omitempty"`
	ApprovalDeadline time.Time                  `json:"approval_deadline,omitempty"`
	Request          hubruntime.ExecuteRequest  `json:"request"`
	Fingerprint      string                     `json:"fingerprint"`
	Response         hubruntime.ExecuteResponse `json:"response"`
	Status           string                     `json:"status"`
	Phase            string                     `json:"phase,omitempty"`
	Unconfirmed      bool                       `json:"unconfirmed,omitempty"`
	UnconfirmedSince time.Time                  `json:"unconfirmed_since,omitempty"`
	Generation       string                     `json:"generation,omitempty"`
	UpdatedAt        time.Time                  `json:"updated_at"`
}

// Serve runs the always-on control-plane endpoint. It never mounts or exposes
// a Docker socket to the communication or Hermes containers.
func Serve(ctx context.Context, manager *Manager, listen string) error {
	if manager == nil || strings.TrimSpace(listen) == "" {
		return errors.New("manager and listen address are required")
	}
	if !manager.cfg.AllowWildcardListen {
		host, _, err := net.SplitHostPort(listen)
		if err != nil || host == "" || host == "0.0.0.0" || host == "::" {
			return fmt.Errorf("wildcard listen %q requires an explicit opt-in; the control plane binds a private address by default", listen)
		}
	}
	server := &http.Server{Addr: listen, Handler: diagnostics.HTTP("supervisor", manager.Handler()), ReadHeaderTimeout: 5 * time.Second, MaxHeaderBytes: 64 * 1024}
	if manager.cfg.Decide != nil {
		go manager.personalLoop(ctx)
	}
	sweepInterval := min(manager.cfg.WarmTTL/2, 5*time.Second)
	if sweepInterval < time.Second {
		sweepInterval = time.Second
	}
	go func() {
		ticker := time.NewTicker(sweepInterval)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case now := <-ticker.C:
				manager.sweep(ctx, now)
			}
		}
	}()
	go func() {
		// Spawns and restarts dominate the 150s budget; on their own loop a
		// slow Ensure can never delay health detection for the fleet.
		ticker := time.NewTicker(sweepInterval)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				manager.reconcileWork(ctx)
			}
		}
	}()
	go func() { // #nosec G118 -- detached reconcile worker
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
	if cfg.HostSpacesRoot == "" {
		cfg.HostSpacesRoot = os.Getenv("HUB_SUPERVISOR_HOST_SPACES")
	}
	if strings.ContainsAny(cfg.HostSpacesRoot, ",\r\n") {
		return nil, errors.New("invalid host spaces root")
	}
	if _, err := (&Manager{cfg: cfg}).hostBindSource(root); err != nil {
		return nil, err
	}
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
		cfg.WarmTTL = DefaultWarmTTL
	}
	if cfg.MaxConcurrent <= 0 {
		cfg.MaxConcurrent = 8
	}
	if value, parseErr := strconv.Atoi(strings.TrimSpace(os.Getenv("HUB_MAX_RUNTIMES_PER_PRINCIPAL"))); parseErr == nil && value > 0 {
		cfg.MaxPerPrincipal = value
	}
	if cfg.CPU == "" {
		cfg.CPU = "2"
	}
	if cfg.Memory == "" {
		cfg.Memory = "2g"
	}
	if cfg.PIDs <= 0 {
		cfg.PIDs = 1024
	}
	if cfg.RuntimePort <= 0 {
		cfg.RuntimePort = 8080
	}
	if cfg.PortBase <= 0 {
		cfg.PortBase = 18080
	}
	if cfg.PortRange <= 0 {
		cfg.PortRange = 10000
	}
	if cfg.Now == nil {
		cfg.Now = time.Now
	}
	if cfg.HTTP == nil {
		// Bounded client for readiness, restart, usage and artifact calls.
		cfg.HTTP = &http.Client{Timeout: durationEnv("HUB_SUPERVISOR_JOB_TIMEOUT", 130*time.Second)}
	}
	if cfg.StreamHTTP == nil {
		cfg.StreamHTTP = &http.Client{Transport: &http.Transport{
			Proxy:                 http.ProxyFromEnvironment,
			DialContext:           (&net.Dialer{Timeout: 10 * time.Second}).DialContext,
			ResponseHeaderTimeout: 30 * time.Second,
		}}
	}
	if cfg.Command == nil {
		cfg.Command = func(ctx context.Context, args ...string) ([]byte, error) {
			return exec.CommandContext(ctx, cfg.Docker, args...).CombinedOutput()
		}
	}
	if cfg.Stats == nil {
		cfg.Stats = dockerStatsCommand(cfg.Command)
	}
	if cfg.AdminAuth == "" {
		cfg.AdminAuth = os.Getenv("HUB_SUPERVISOR_ADMIN_AUTH")
	}
	// The personal supervisor is opt-in: a configured model name activates
	// the relay decider; without it the machinery never calls a model.
	if cfg.Decide == nil && os.Getenv("HUB_PERSONAL_MODEL") != "" {
		cfg.Decide = relayDecide
	}
	m := &Manager{holds: map[string]AdministrativeHold{}, pins: map[string]hubruntime.ExecuteRequest{}, cfg: cfg, items: map[string]*runtimeEntry{}, locks: map[string]*sync.Mutex{}, sem: make(chan struct{}, cfg.MaxConcurrent), slots: map[string]struct{}{}, jobs: map[string]jobRecord{}, personalPending: map[string]*personalWake{}, personalRunning: map[string]bool{}, wakeNotify: make(chan struct{}, 1), statePath: filepath.Join(cfg.StateDir, "supervisor.json")}
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
	if state.Holds != nil {
		m.holds = state.Holds
	}
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
		legacy := record.Phase == ""
		// Older expiry code changed the record but left its replay uncertain.
		if record.Status == "interrupted" && (record.Response.Status == "" || record.Response.Status == "uncertain") {
			record.Response.Status, record.Response.LastEvent = "interrupted", "run.interrupted"
			dirty = true
		}
		if record.Status == "running" {
			record.Status = "uncertain"
			record.Response.Status = "uncertain"
			record.Response.LastEvent = "run.unknown"
			record.UpdatedAt = m.cfg.Now().UTC()
			dirty = true
		}
		mapLegacyPhase(&record)
		m.jobs[key] = record
		dirty = dirty || legacy
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
		m.items[key] = &runtimeEntry{Runtime: item.Runtime, binding: item.Binding, leases: item.Leases, incidents: item.Incidents, restored: true}
	}
	if dirty {
		return m.persistLocked()
	}
	return nil
}

func (m *Manager) persistLocked() error {
	state := persistedManager{Holds: m.holds, Pins: m.pins, Generation: m.gen, LeaseSeq: m.leaseSeq, Items: make([]persistedRuntime, 0, len(m.items)), Jobs: m.jobs, Orphans: m.orphans}
	for _, item := range m.items {
		state.Items = append(state.Items, persistedRuntime{Runtime: item.Runtime, Binding: item.binding, Leases: item.leases, Incidents: item.incidents})
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

func (m *Manager) sweep(ctx context.Context, now time.Time) {
	sweepCtx, cancel := context.WithTimeout(ctx, 150*time.Second)
	defer cancel()
	_ = m.DetectOrphans(sweepCtx)
	m.ReapOrphans(sweepCtx)
	m.ReconcileHealth(sweepCtx, now)
	if sweepCtx.Err() == nil {
		m.ExpireApprovals(sweepCtx, now)
		m.reconcileUnconfirmed(sweepCtx, now)
		m.ExpireUncertain(sweepCtx, now)
		m.CompactJobs(now)
	}
	if sweepCtx.Err() == nil {
		_ = m.Reap(sweepCtx, now)
	}
}

func (m *Manager) reconcileWork(ctx context.Context) {
	workCtx, cancel := context.WithTimeout(ctx, 150*time.Second)
	defer cancel()
	m.RecoverMissingWork(workCtx)
	if workCtx.Err() == nil {
		m.ReconcilePins(workCtx)
	}
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
	status := runtime.Runtime
	status.Consumers = runtime.consumers()
	status.WaitReason = m.expectedWaitLocked(runtime)
	return status, true, nil
}

func (m *Manager) Handler() http.Handler { return http.HandlerFunc(m.serveHTTP) }

func (m *Manager) authorized(r *http.Request) bool {
	provided := strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")
	return len(provided) == len(m.cfg.RuntimeAuth) && subtle.ConstantTimeCompare([]byte(provided), []byte(m.cfg.RuntimeAuth)) == 1
}

func (m *Manager) list(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeJSON(w, http.StatusMethodNotAllowed, map[string]string{"error": "method not allowed"})
		return
	}
	m.mu.Lock()
	values := make([]Runtime, 0, len(m.items))
	for _, item := range m.items {
		runtime := item.Runtime
		runtime.Consumers = item.consumers()
		runtime.WaitReason = m.expectedWaitLocked(item)
		values = append(values, runtime)
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
	if strings.HasPrefix(request.ScopeID, "group:") {
		contextID = strings.TrimPrefix(request.ScopeID, "group:")
		if request.PrincipalID != contextID || request.UserID != contextID || request.ActorID != contextID || request.RuntimeID != contextID {
			return Binding{}, errors.New("group principal mismatch")
		}
	}
	if !strings.HasPrefix(request.ScopeID, "user:") && !strings.HasPrefix(request.ScopeID, "organization:") && !strings.HasPrefix(request.ScopeID, "group:") {
		return Binding{}, errors.New("invalid scope")
	}
	if request.ContextID != contextID || request.UserID == "" || request.ActorID == "" || request.PolicyVersion == "" || !validID(contextID) || request.OrganizationID == "" || request.RuntimeID == "" {
		return Binding{}, errors.New("invalid runtime binding")
	}
	if strings.HasPrefix(request.ScopeID, "group:") {
		scope, err := stack.ReadScope(filepath.Join(m.cfg.SpacesRoot, contextID))
		if err != nil || scope.Kind != stack.GroupScope || scope.ID != contextID {
			return Binding{}, errors.New("group requires a dedicated group home")
		}
	}
	organizationRoot := ""
	if strings.HasPrefix(request.ScopeID, "user:") && request.OrganizationID != "" && request.OrganizationID != "personal" {
		organizationRoot = filepath.Join(m.cfg.SpacesRoot, request.OrganizationID)
	}
	return m.normalize(Binding{PrincipalID: request.PrincipalID, ContextID: contextID, RuntimeID: request.RuntimeID, RuntimeMode: "gateway", ScopeID: request.ScopeID, UserID: request.UserID, OrganizationID: request.OrganizationID, PolicyVersion: request.PolicyVersion, OrganizationRoot: organizationRoot})
}

func runtimeKey(binding Binding) string {
	return binding.PrincipalID + "\x00" + binding.ContextID + "\x00" + binding.RuntimeMode
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

// allocatePortLocked reserves a loopback host port for this context. The hash
// offset keeps allocations stable across respawns, and linear probing over the
// tracked inventory guarantees no two supervised runtimes ever share a port.
// Callers must hold m.mu.
func (m *Manager) allocatePortLocked(key string) (int, error) {
	used := map[int]struct{}{}
	for _, item := range m.items {
		if port := addressPort(item.Address); port != 0 {
			used[port] = struct{}{}
		}
	}
	start := int(hashNumber(key) % uint32(m.cfg.PortRange)) // #nosec G115 -- result is bounded by the PortRange modulo
	for i := 0; i < m.cfg.PortRange; i++ {
		candidate := m.cfg.PortBase + (start+i)%m.cfg.PortRange
		if _, busy := used[candidate]; !busy {
			return candidate, nil
		}
	}
	return 0, errors.New("supervisor host port range exhausted")
}

func addressPort(address string) int {
	i := strings.LastIndexByte(address, ':')
	if i < 0 {
		return 0
	}
	port, _ := strconv.Atoi(address[i+1:])
	return port
}

// publishedPort reports the loopback host port a running runtime — or a
// managed runtime's control relay — actually binds, so a recovered container
// keeps its real address even when allocation moved on.
func (m *Manager) publishedPort(ctx context.Context, runtime Runtime) (int, error) {
	name, port := runtime.Container, m.cfg.RuntimePort
	if runtime.Managed {
		name += "-ctl"
		port = controlRelayContainerPort
	}
	out, err := m.command(ctx, "port", name, strconv.Itoa(port))
	if err != nil {
		return 0, errors.New("published port unavailable")
	}
	line := strings.TrimSpace(firstLine(string(out)))
	i := strings.LastIndexByte(line, ':')
	if i < 0 {
		return 0, errors.New("published port unavailable")
	}
	parsed, err := strconv.Atoi(line[i+1:])
	if err != nil || parsed <= 0 || parsed > 65535 {
		return 0, errors.New("published port unavailable")
	}
	return parsed, nil
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
func writeJSON(w http.ResponseWriter, status int, value any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(value)
}

func firstLine(s string) string {
	s = strings.TrimSpace(s)
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		s = s[:i]
	}
	if len(s) > 300 {
		s = s[:300]
	}
	return s
}
