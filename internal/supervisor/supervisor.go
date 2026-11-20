package supervisor

import (
	"context"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	identitycontract "github.com/letya999/hermes-hub/contracts/identity/v1"
	taskcontract "github.com/letya999/hermes-hub/contracts/task/v1"
	"github.com/letya999/hermes-hub/internal/diagnostics"
	"github.com/letya999/hermes-hub/internal/identityhub"
	"github.com/letya999/hermes-hub/internal/passportview"
	hubruntime "github.com/letya999/hermes-hub/internal/runtime"
	"github.com/letya999/hermes-hub/internal/stack"
	"github.com/letya999/hermes-hub/internal/toolhub/api"
	"github.com/letya999/hermes-hub/shared/locale"
	"log"
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
// Docker is the default compute backend; Kubernetes is opt-in. The
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
	Identity                 identitycontract.Client
	IdentityLinkAuth         string
	CommunicationServiceAuth string
	DreamingServiceAuth      string

	ExecutionBackend string
	ContainerRuntime string
	Kubernetes       KubernetesConfig
	UnoURL           string
	UnoToken         string
	UnoImage         string
	UnoBoxRAMMB      int
	UnoBoxVCPU       int
	UnoProxyHost     string
	Backend          ComputeBackend
	Docker           string
	Image            string
	ScriptImage      string
	Network          string
	SpacesRoot       string
	HostSpacesRoot   string
	// SettingsEdit publishes one config passport change after the HTTP control
	// request is authenticated. Nil denies settings edits.
	SettingsEdit func(context.Context, string, string, string, *string, string) (map[string]any, error)
	StateDir     string
	RuntimeAuth  string
	AdminAuth    string
	Environment  string
	WarmTTL      time.Duration
	// BrowserIdleTTL is the grace a per-space browser sidecar stays up after
	// its last runtime is gone; the sweep stops it once the deadline passes.
	BrowserIdleTTL time.Duration
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
	// BackupEvery is the scheduled contextlife capture cadence per space;
	// zero disables the backup sweep. BackupKeep bounds retained archives.
	// BackupDrillEvery is the restore-drill cadence; zero keeps backups
	// running with drills off.
	BackupEvery      time.Duration
	BackupKeep       int
	BackupDrillEvery time.Duration
}

type Binding struct {
	SubjectID        string
	AgentID          string
	IdentityRevision string
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
	IdentityRevision  string      `json:"identity_revision,omitempty"`
	ComputeID         string      `json:"compute_id,omitempty"`
	ComputeConditions []Condition `json:"compute_conditions,omitempty"`
	ConnectorHealth   string      `json:"connector_health,omitempty"`
	FailureGeneration string      `json:"failure_generation,omitempty"`
	Ownership         string      `json:"ownership,omitempty"`
	CrashCount        int         `json:"crash_count,omitempty"`
	CrashWindowStart  time.Time   `json:"crash_window_start,omitempty"`
	NextRetryAt       time.Time   `json:"next_retry_at,omitempty"`
	RuntimeHealth     string      `json:"runtime_health,omitempty"`
	HermesReadiness   string      `json:"hermes_readiness,omitempty"`
	HealthCheckedAt   time.Time   `json:"health_checked_at,omitempty"`
	// R1.4-R1.6 observations persisted from the runtime's own surface probe.
	// Reporting only: they never feed remediation decisions or grants.
	ToolSurface  string            `json:"tool_surface,omitempty"`
	MissingTools []string          `json:"missing_tools,omitempty"`
	ToolReasons  map[string]string `json:"tool_reasons,omitempty"`
	ModelStatus  string            `json:"model_status,omitempty"`
	Restarts     int               `json:"restarts,omitempty"`
	// PendingRestart is the durable cause of a deferred restart: an armed
	// marker on a busy runtime applies after handoff, and a supervisor
	// restart must not lose why the restart was requested.
	PendingRestart *RestartCause `json:"pending_restart,omitempty"`
	Desired        bool          `json:"desired"`
	PrincipalID    string        `json:"principal_id"`
	ContextID      string        `json:"context_id"`
	RuntimeID      string        `json:"runtime_id"`
	RuntimeMode    string        `json:"runtime_mode"`
	Generation     string        `json:"generation"`
	// PolicyVersion records the policy hash this generation was spawned with so
	// a later Ensure can detect a rotated policy and replace the runtime
	// instead of reusing one whose own envelope gate would reject the request.
	PolicyVersion string    `json:"policy_version,omitempty"`
	Container     string    `json:"container"`
	Address       string    `json:"address"`
	State         State     `json:"state"`
	ImageSource   string    `json:"image_source,omitempty"`
	LastUsed      time.Time `json:"last_used"`
	IdleDeadline  time.Time `json:"idle_deadline,omitempty"`
	Managed       bool      `json:"managed,omitempty"`
	// Consumers, Leases and WaitReason are read projections, never persisted
	// state: they describe live demand at read time and stay empty on disk.
	// Leases is the count older hubctl still reads, derived from lease rows.
	Consumers  []LeaseConsumer `json:"consumers,omitempty"`
	Leases     int             `json:"leases"`
	WaitReason string          `json:"wait_reason,omitempty"`
	// Resource counters sampled once per watchdog sweep; reporting only, they
	// never feed remediation decisions.
	CPUPercent float64 `json:"cpu_percent,omitempty"`
	MemBytes   int64   `json:"mem_bytes,omitempty"`
}

// RestartCause is the owner-safe record of why a runtime restart was
// requested: a fixed reason vocabulary plus the operation/generation ids —
// never free text or payloads.
type RestartCause struct {
	Reason      string `json:"reason"`
	OperationID string `json:"operation_id,omitempty"`
	Revision    uint64 `json:"revision,omitempty"`
	Generation  string `json:"generation"`
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
	// metrics watcher state — in-memory only: a restart re-baselines
	// consecutive-breach counters while the persisted incident record keeps
	// a fired anomaly from re-alerting as new.
	metricsCheckedAt time.Time
	metricBreaches   map[string]int
	metricPrev       hubruntime.RuntimeStatus
	lastMetrics      hubruntime.RuntimeStatus
	lastStat         ContainerStat
}

type Manager struct {
	ownedIdentity identitycontract.Client
	backend       ComputeBackend
	holds         map[string]AdministrativeHold
	pins          map[string]hubruntime.ExecuteRequest
	orphans       []OrphanRuntime
	cfg           Config
	mu            sync.Mutex
	items         map[string]*runtimeEntry
	locks         map[string]*sync.Mutex
	sem           chan struct{}
	// Warm runtimes hold sem for their lifetime; disposable scripts need their own bound.
	scriptSem chan struct{}
	// recoverySem bounds detached observation and missing-work workers so one
	// stuck runtime cannot delay another owner's recovery sweep.
	recoverySem     chan struct{}
	slots           map[string]struct{}
	gen             uint64
	leaseSeq        uint64
	statePath       string
	jobs            map[string]jobRecord
	watchdog        WatchdogMetrics
	personalPending map[string]*personalWake
	personalRunning map[string]bool
	wakeNotify      chan struct{}
	// sidecarIdle holds per-container idle deadlines for browser sidecars
	// whose space has no live runtime; in-memory only — a restart grants one
	// grace period, never a premature stop.
	sidecarIdle map[string]time.Time
	// statusSig dedups the transitions journal: last-emitted
	// phase|condition signature per runtime/job key. In-memory only — on
	// restart the first persist re-journals current state, a correct
	// (if slightly chatty) re-baseline.
	statusSig map[string]string
	// backups is the per-space scheduled-backup ledger (#200): last
	// success, last bounded error class, drill result. Persisted in
	// backups.json next to supervisor.json.
	backups map[string]BackupRecord
}

type persistedManager struct {
	Backend          string                               `json:"execution_backend,omitempty"`
	ContainerRuntime string                               `json:"container_runtime,omitempty"`
	RuntimeClassName string                               `json:"runtime_class_name,omitempty"`
	Holds            map[string]AdministrativeHold        `json:"holds,omitempty"`
	Pins             map[string]hubruntime.ExecuteRequest `json:"pins,omitempty"`
	Orphans          []OrphanRuntime                      `json:"orphans,omitempty"`
	Generation       uint64                               `json:"generation"`
	LeaseSeq         uint64                               `json:"lease_seq"`
	Items            []persistedRuntime                   `json:"items"`
	Jobs             map[string]jobRecord                 `json:"jobs,omitempty"`
}

type persistedRuntime struct {
	Binding   Binding             `json:"binding,omitempty"`
	Runtime   Runtime             `json:"runtime"`
	Leases    map[string]Lease    `json:"leases,omitempty"`
	Incidents map[string]Incident `json:"incidents,omitempty"`
}

type jobRecord struct {
	CaptureContext    api.RunContext              `json:"capture_context,omitempty"`
	AdmissionID       string                      `json:"admission_id,omitempty"`
	RequestTextSHA256 string                      `json:"request_text_sha256,omitempty"`
	OperationalEpoch  uint64                      `json:"operational_epoch,omitempty"`
	Dispatching       bool                        `json:"dispatching,omitempty"`
	CancelRequested   bool                        `json:"cancel_requested,omitempty"`
	Controls          map[string]controlRecord    `json:"controls,omitempty"`
	ApprovalDeadline  time.Time                   `json:"approval_deadline,omitempty"`
	Request           hubruntime.ExecuteRequest   `json:"request"`
	Fingerprint       string                      `json:"fingerprint"`
	Response          hubruntime.ExecuteResponse  `json:"response"`
	Status            string                      `json:"status"`
	Phase             string                      `json:"phase,omitempty"`
	Unconfirmed       bool                        `json:"unconfirmed,omitempty"`
	UnconfirmedSince  time.Time                   `json:"unconfirmed_since,omitempty"`
	Generation        string                      `json:"generation,omitempty"`
	ActingIdentity    taskcontract.ActingIdentity `json:"acting_identity,omitzero"`
	PolicyRevision    string                      `json:"policy_revision,omitempty"`
	PassportDigest    string                      `json:"passport_digest,omitempty"`
	Initiator         string                      `json:"initiator,omitempty"`
	RunDeadline       time.Time                   `json:"run_deadline,omitzero"`
	RuntimeMode       string                      `json:"runtime_mode,omitempty"`
	UpdatedAt         time.Time                   `json:"updated_at"`
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
	listener, err := net.Listen("tcp", listen)
	if err != nil {
		return err
	}
	defer listener.Close()
	if ctx.Err() != nil {
		return nil
	}
	identityClient := manager.cfg.Identity
	if identityClient == nil {
		path, err := identityhub.StorePath(manager.cfg.StateDir, manager.environment())
		if err != nil {
			return err
		}
		registry, err := identityhub.Open(path)
		if err != nil {
			return fmt.Errorf("%s: %w", locale.T(locale.Operator(), "supervisor.identity-unavailable"), err)
		}
		if err := registry.EnsureSigningKeys(ctx); err != nil {
			_ = registry.Close()
			if ctx.Err() != nil {
				return nil
			}
			return fmt.Errorf("%s: %w", locale.T(locale.Operator(), "supervisor.identity-unavailable"), err)
		}
		identityClient = registry
		manager.mu.Lock()
		manager.ownedIdentity = registry
		manager.mu.Unlock()
		defer func() { manager.mu.Lock(); manager.ownedIdentity = nil; manager.mu.Unlock(); _ = registry.Close() }()
	}
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	server := &http.Server{Addr: listen, Handler: diagnostics.HTTP("supervisor", manager.Handler()), ReadHeaderTimeout: 5 * time.Second, MaxHeaderBytes: 64 * 1024}
	var loops sync.WaitGroup
	if manager.cfg.Decide != nil {
		loops.Add(1)
		go func() {
			defer loops.Done()
			manager.personalLoop(ctx)
		}()
	}
	sweepInterval := min(manager.cfg.WarmTTL/2, 5*time.Second)
	if sweepInterval < time.Second {
		sweepInterval = time.Second
	}
	loops.Add(1)
	go func() {
		defer loops.Done()
		ticker := time.NewTicker(sweepInterval)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case now := <-ticker.C:
				manager.sweep(ctx, now)
				if registry, ok := identityClient.(*identityhub.Registry); ok {
					if err := registry.ExpirePendingLinks(ctx, now); err != nil && ctx.Err() == nil {
						log.Print(locale.T(locale.Operator(), "supervisor.identity-upkeep-pending"))
					}
				}
			}
		}
	}()
	loops.Add(1)
	go func() {
		defer loops.Done()
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
	shutdownDone := make(chan struct{})
	go func() { // #nosec G118 -- joined shutdown worker
		defer close(shutdownDone)
		<-ctx.Done()
		shutdown, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if err := server.Shutdown(shutdown); err != nil {
			_ = server.Close()
		}
	}()
	serveErr := server.Serve(listener)
	cancel()
	// Do not report the service stopped while an in-flight pass may still
	// write spool/control state underneath a shutting-down caller.
	loops.Wait()
	<-shutdownDone
	if serveErr != nil && !errors.Is(serveErr, http.ErrServerClosed) {
		return serveErr
	}
	return nil
}

func New(cfg Config) (*Manager, error) {
	if cfg.ContainerRuntime == "" {
		cfg.ContainerRuntime = envOr("HUB_CONTAINER_RUNTIME", "runc")
	}
	switch cfg.ContainerRuntime {
	case "runc", "runsc", "kata":
	default:
		return nil, errors.New("unsupported container runtime; expected runc, runsc or kata")
	}
	if cfg.ExecutionBackend == "" {
		cfg.ExecutionBackend = os.Getenv("HUB_EXECUTION_BACKEND")
	}
	if cfg.ExecutionBackend == "" {
		cfg.ExecutionBackend = "docker"
	}
	if cfg.ExecutionBackend != "docker" && cfg.ExecutionBackend != "kubernetes" && cfg.ExecutionBackend != "firecracker" {
		return nil, errors.New("unsupported execution backend")
	}
	if cfg.ExecutionBackend == "firecracker" {
		cfg.UnoURL = firstEnv(cfg.UnoURL, "HUB_UNO_URL")
		cfg.UnoToken = firstEnv(cfg.UnoToken, "HUB_UNO_TOKEN")
		cfg.UnoImage = firstEnv(cfg.UnoImage, "HUB_UNO_IMAGE")
		if cfg.UnoBoxRAMMB <= 0 {
			cfg.UnoBoxRAMMB = intEnv("HUB_UNO_BOX_RAM_MB", 1024)
		}
		if cfg.UnoBoxVCPU <= 0 {
			cfg.UnoBoxVCPU = intEnv("HUB_UNO_BOX_VCPU", 2)
		}
		if cfg.UnoProxyHost == "" {
			cfg.UnoProxyHost = envOr("HUB_UNO_PROXY_HOST", "127.0.0.1")
		}
	}
	if cfg.SpacesRoot == "" || cfg.RuntimeAuth == "" || cfg.Image == "" {
		return nil, errors.New("spaces root, runtime auth and image are required")
	}
	root, err := filepath.Abs(filepath.Clean(cfg.SpacesRoot))
	if err != nil {
		return nil, fmt.Errorf("spaces root: %w", err)
	}
	cfg.SpacesRoot = root
	if cfg.ExecutionBackend == "kubernetes" {
		cfg.HostSpacesRoot = ""
	} else if cfg.HostSpacesRoot == "" {
		cfg.HostSpacesRoot = os.Getenv("HUB_SUPERVISOR_HOST_SPACES")
	}
	if strings.ContainsAny(cfg.HostSpacesRoot, ",\r\n") {
		return nil, errors.New("invalid host spaces root")
	}
	if _, err := (&Manager{cfg: cfg}).hostBindSource(root); cfg.ExecutionBackend == "docker" && err != nil {
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
	if cfg.BrowserIdleTTL <= 0 {
		cfg.BrowserIdleTTL = DefaultBrowserIdleTTL
	}
	if cfg.MaxConcurrent <= 0 {
		cfg.MaxConcurrent = 16
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
	if cfg.RuntimePort <= 0 || cfg.RuntimePort > 65535 {
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
			return exec.CommandContext(ctx, cfg.Docker, args...).CombinedOutput() // #nosec G702 -- docker binary is operator-configured; argv is built internally
		}
	}
	if cfg.Stats == nil && cfg.ExecutionBackend == "docker" {
		cfg.Stats = dockerStatsCommand(cfg.Command)
	}
	if cfg.AdminAuth == "" {
		cfg.AdminAuth = os.Getenv("HUB_SUPERVISOR_ADMIN_AUTH")
	}
	if cfg.IdentityLinkAuth == "" {
		cfg.IdentityLinkAuth = os.Getenv("HUB_IDENTITY_LINK_AUTH")
	}
	if cfg.IdentityLinkAuth != "" && cfg.IdentityLinkAuth == cfg.RuntimeAuth {
		return nil, errors.New(locale.T(locale.Operator(), "supervisor.identity-link-auth-distinct"))
	}
	if cfg.CommunicationServiceAuth == "" {
		cfg.CommunicationServiceAuth = strings.TrimSpace(os.Getenv(identitycontract.CommunicationServiceAuthEnv))
	}
	if auth := cfg.CommunicationServiceAuth; auth != "" && (auth == cfg.RuntimeAuth || auth == cfg.AdminAuth || auth == cfg.IdentityLinkAuth) {
		return nil, errors.New(locale.T(locale.Operator(), "supervisor.communication-service-auth-distinct"))
	}
	if cfg.DreamingServiceAuth == "" {
		cfg.DreamingServiceAuth = strings.TrimSpace(os.Getenv(identitycontract.DreamingServiceAuthEnv))
	}
	if auth := cfg.DreamingServiceAuth; auth != "" && (auth == cfg.RuntimeAuth || auth == cfg.AdminAuth || auth == cfg.IdentityLinkAuth || auth == cfg.CommunicationServiceAuth) {
		return nil, errors.New(locale.T(locale.Operator(), "supervisor.dreaming-service-auth-distinct"))
	}
	if cfg.BackupEvery == 0 {
		cfg.BackupEvery = backupDurationEnv("HUB_BACKUP_EVERY", 24*time.Hour)
	}
	if cfg.BackupKeep == 0 {
		cfg.BackupKeep = backupIntEnv("HUB_BACKUP_KEEP", 7)
	}
	if cfg.BackupDrillEvery == 0 {
		cfg.BackupDrillEvery = backupDurationEnv("HUB_BACKUP_DRILL_EVERY", 7*24*time.Hour)
	}
	// The personal supervisor is opt-in: a configured model name activates
	// the relay decider; without it the machinery never calls a model.
	m := &Manager{holds: map[string]AdministrativeHold{}, pins: map[string]hubruntime.ExecuteRequest{}, cfg: cfg, items: map[string]*runtimeEntry{}, locks: map[string]*sync.Mutex{}, sem: make(chan struct{}, cfg.MaxConcurrent), scriptSem: make(chan struct{}, min(2, cfg.MaxConcurrent)), recoverySem: make(chan struct{}, recoveryWorkers), slots: map[string]struct{}{}, jobs: map[string]jobRecord{}, personalPending: map[string]*personalWake{}, personalRunning: map[string]bool{}, wakeNotify: make(chan struct{}, 1), statePath: filepath.Join(cfg.StateDir, "supervisor.json")}
	if m.cfg.Decide == nil && os.Getenv("HUB_PERSONAL_MODEL") != "" {
		m.cfg.Decide = m.relayDecide
	}
	m.backend = cfg.Backend
	if m.backend == nil {
		if m.usingKubernetes() {
			m.backend, err = newKubernetesBackend(m)
			if err != nil {
				return nil, err
			}
		} else if cfg.ExecutionBackend == "firecracker" {
			m.backend, err = newFirecrackerBackend(m)
			if err != nil {
				return nil, err
			}
		} else {
			m.backend = dockerBackend{m}
		}
	}
	m.loadBackups()
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
	backend := state.Backend
	if backend == "" {
		backend = "docker"
	}
	if backend != "docker" && backend != "kubernetes" && backend != "firecracker" {
		return errors.New("invalid persisted execution backend")
	}
	runtime := state.ContainerRuntime
	if runtime == "" {
		runtime = "runc"
	}
	if runtime != "runc" && runtime != "runsc" && runtime != "kata" {
		return errors.New("invalid persisted container runtime")
	}
	if backend != m.cfg.ExecutionBackend || runtime != m.cfg.ContainerRuntime || state.RuntimeClassName != m.cfg.Kubernetes.RuntimeClassName {
		if len(state.Orphans) != 0 {
			return errors.New("execution backend or isolation change requires cleared orphans")
		}
		for _, item := range state.Items {
			if item.Runtime.State != Stopped {
				return errors.New("execution backend or isolation change requires stopped runtimes")
			}
		}
		for _, record := range state.Jobs {
			if !terminalRunStatus(record.Status) {
				return errors.New("execution backend or isolation change requires settled jobs")
			}
		}
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
	state := persistedManager{Backend: m.cfg.ExecutionBackend, ContainerRuntime: m.cfg.ContainerRuntime, RuntimeClassName: m.cfg.Kubernetes.RuntimeClassName, Holds: m.holds, Pins: m.pins, Generation: m.gen, LeaseSeq: m.leaseSeq, Items: make([]persistedRuntime, 0, len(m.items)), Jobs: m.jobs, Orphans: m.orphans}
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
	if err == nil {
		m.emitStatusTransitionsLocked(m.now())
	}
	return err
}

// now tolerates hand-built Manager values in tests where cfg.Now was never
// normalized by New.
func (m *Manager) now() time.Time {
	if m.cfg.Now == nil {
		return time.Now()
	}
	return m.cfg.Now()
}

func (m *Manager) sweep(ctx context.Context, now time.Time) {
	sweepCtx, cancel := context.WithTimeout(ctx, 150*time.Second)
	defer cancel()
	_ = m.DetectOrphans(sweepCtx)
	m.ReapOrphans(sweepCtx)
	m.ReconcileHealth(sweepCtx, now)
	if sweepCtx.Err() == nil {
		m.ExpireApprovals(sweepCtx, now)
		m.sweepStuckRunning(now)
		m.reconcileUnconfirmed(sweepCtx, now)
		m.ExpireUncertain(sweepCtx, now)
		m.CompactJobs(now)
	}
	if sweepCtx.Err() == nil {
		_ = m.Reap(sweepCtx, now)
		m.ReapIdleBrowserSidecars(sweepCtx, now)
	}
	if sweepCtx.Err() == nil {
		m.backupSweep(sweepCtx, now)
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
	provided, bearer := strings.CutPrefix(r.Header.Get("Authorization"), "Bearer ")
	return bearer && m.cfg.RuntimeAuth != "" && len(provided) == len(m.cfg.RuntimeAuth) && subtle.ConstantTimeCompare([]byte(provided), []byte(m.cfg.RuntimeAuth)) == 1
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
		runtime.Leases = len(item.leases)
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
		eff, err := passportview.LoadEffective(filepath.Join(m.cfg.SpacesRoot, contextID))
		if err != nil || eff.Stack.Identity.Kind != string(stack.GroupScope) || eff.Stack.Identity.User != contextID {
			return Binding{}, errors.New("group requires a dedicated group home")
		}
	}
	organizationRoot := ""
	if strings.HasPrefix(request.ScopeID, "user:") && request.OrganizationID != "" && request.OrganizationID != "personal" {
		organizationRoot = filepath.Join(m.cfg.SpacesRoot, request.OrganizationID)
	}
	mode := "gateway"
	if key := executeJobKey(request); key != "" {
		m.mu.Lock()
		if record, ok := m.jobs[key]; ok && record.Request.Envelope.Equal(request.Envelope) && record.RuntimeMode != "" {
			mode = record.RuntimeMode
		}
		m.mu.Unlock()
	}
	subject := request.UserID
	if strings.HasPrefix(request.ScopeID, "group:") {
		external, ok := strings.CutPrefix(request.ExternalIdentityID, "telegram-")
		if !ok {
			return Binding{}, identitycontract.ErrForbidden
		}
		principal, _, err := identitycontract.ResolveChannel(context.Background(), m.identityClient(), identitycontract.ChannelIdentity{Channel: identitycontract.Telegram, ID: external})
		if err != nil {
			return Binding{}, err
		}
		subject = principal.ID
	}
	bound := Binding{SubjectID: subject, AgentID: "agent:" + subject + ":default", PrincipalID: request.PrincipalID, ContextID: contextID, RuntimeID: request.RuntimeID, RuntimeMode: mode, ScopeID: request.ScopeID, UserID: request.UserID, OrganizationID: request.OrganizationID, PolicyVersion: request.PolicyVersion, OrganizationRoot: organizationRoot}
	_, revision, err := m.identityTemplate(context.Background(), bound)
	if err != nil {
		return Binding{}, err
	}
	bound.IdentityRevision = revision
	return m.normalize(bound)
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
