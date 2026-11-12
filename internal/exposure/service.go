package exposure

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"slices"
	"sync"
	"time"

	"github.com/letya999/hermes-hub/internal/audit"
	"github.com/letya999/hermes-hub/internal/identity"
)

// Provider starts and stops one tunnel workload per open exposure (ADR-0079
// D2). The docker-backed implementation lands with admission wiring (S4).
type Provider interface {
	Open(ctx context.Context, spec Spec) (handle string, err error)
	Close(ctx context.Context, handle string) error
}

type Spec struct {
	Provider   string
	Endpoint   string
	Bind       string
	Target     string
	PublicName string
	Idle       time.Duration
	Lifetime   time.Duration
}

type Tunnel struct {
	Endpoint   string    `json:"endpoint"`
	PublicName string    `json:"public_name"`
	Target     string    `json:"target"`
	Provider   string    `json:"provider"`
	OpenedAt   time.Time `json:"opened_at"`
	ExpiresAt  time.Time `json:"expires_at"`
	Uncertain  bool      `json:"uncertain,omitempty"`
	operation  *audit.Operation
	cleanup    *audit.Operation
	handle     string
}

type ManagerOptions struct {
	Provider Provider
	Auth     identity.Envelope
	Ledger   *audit.Ledger
	Now      func() time.Time
	// ReapInterval sets the background lifetime-reap cadence; zero keeps the
	// default minute.
	ReapInterval time.Duration
}

// Manager owns the open exposures of one principal — name-only opens against
// the reviewed config, bounded by its caps, audited on every transition.
type Manager struct {
	cfg      *Config
	provider Provider
	auth     identity.Envelope
	ledger   *audit.Ledger
	now      func() time.Time

	mu        sync.Mutex
	tunnels   map[string]*Tunnel
	uncertain map[string]bool
	done      chan struct{}
	stopped   chan struct{}
}

func NewManager(cfg *Config, opts ManagerOptions) *Manager {
	now := opts.Now
	if now == nil {
		now = time.Now
	}
	interval := opts.ReapInterval
	if interval <= 0 {
		interval = time.Minute
	}
	m := &Manager{cfg: cfg, provider: opts.Provider, auth: opts.Auth,
		ledger: opts.Ledger, now: now, tunnels: map[string]*Tunnel{}, uncertain: map[string]bool{},
		done: make(chan struct{}), stopped: make(chan struct{})}
	go func() {
		defer close(m.stopped)
		ticker := time.NewTicker(interval)
		defer ticker.Stop()
		for {
			select {
			case <-m.done:
				return
			case <-ticker.C:
				if _, err := m.Reap(context.Background()); err != nil {
					slog.Error("exposure cleanup failed", "error_class", "uncertain")
				}
			}
		}
	}()
	return m
}

// Shutdown stops the background reaper. Idempotent; live tunnels die with
// the process via their provider handles.
func (m *Manager) Shutdown() {
	select {
	case <-m.done:
	default:
		close(m.done)
	}
	<-m.stopped
}

// Open starts the tunnel workload for a declared endpoint. Re-opening a live
// endpoint is idempotent, not a second workload.
func (m *Manager) Open(ctx context.Context, name string) (*Tunnel, error) {
	ep, err := m.cfg.EndpointFor(name)
	if err != nil {
		return nil, errors.Join(err, m.audit("open", name, "denied", "unknown-endpoint"))
	}
	var public, bind string
	if m.cfg.Provider == "sish" {
		if bind, err = m.cfg.BindName(name); err != nil {
			return nil, err
		}
		if public, err = m.cfg.PublicName(name); err != nil {
			return nil, err
		}
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.uncertain[name] {
		return nil, audit.ErrUncertain
	}
	if t, ok := m.tunnels[name]; ok {
		return t, nil
	}
	occupied := len(m.tunnels)
	for target := range m.uncertain {
		if _, known := m.tunnels[target]; !known {
			occupied++
		}
	}
	if occupied >= m.cfg.Max() {
		return nil, errors.Join(fmt.Errorf("%w: %d concurrent exposures", ErrBusy, m.cfg.Max()), m.audit("open", name, "busy", "capacity"))
	}
	if m.provider == nil {
		return nil, errors.Join(ErrUnavailable, m.audit("open", name, "unavailable", "no-provider"))
	}
	spec := Spec{Provider: m.cfg.Provider, Endpoint: name, Bind: bind,
		Target: ep.Target, PublicName: public, Idle: m.cfg.Idle(), Lifetime: m.cfg.Lifetime()}
	event := m.auditRecord("open", public, "admitted", "")
	if event.Name == "" {
		event.Name = name
	}
	event.Action = "exposure.open"
	event.CleanupAction = "exposure.close"
	event.CorrelationID = "exposure:" + name
	operation, err := m.ledger.Begin(event)
	if err != nil {
		return nil, err
	}
	handle, err := m.provider.Open(ctx, spec)
	if err != nil || handle == "" {
		m.uncertain[name] = true
		if handle != "" {
			now := m.now()
			m.tunnels[name] = &Tunnel{Endpoint: name, PublicName: public, Target: ep.Target, Provider: m.cfg.Provider, OpenedAt: now, ExpiresAt: now.Add(spec.Lifetime), handle: handle, operation: operation, Uncertain: true}
		}
		// No final execution claim: admission and completion reserve remain unsettled.
		return nil, audit.ErrUncertain
	}
	now := m.now()
	t := &Tunnel{Endpoint: name, PublicName: public, Target: ep.Target,
		Provider: m.cfg.Provider, OpenedAt: now, ExpiresAt: now.Add(spec.Lifetime), handle: handle, operation: operation}
	m.tunnels[name] = t
	if err := operation.Complete("succeeded", "not_applicable", "open", ""); err != nil {
		t.Uncertain = true
		m.uncertain[name] = true
		return nil, err
	}
	return t, nil
}

// Close stops the tunnel workload for an endpoint and emits a close receipt.
func (m *Manager) Close(ctx context.Context, name string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	t, ok := m.tunnels[name]
	if !ok {
		if m.uncertain[name] {
			return audit.ErrUncertain
		}
		return fmt.Errorf("%w: %q", ErrNotFound, name)
	}
	var admissionErr error
	if t.cleanup == nil {
		t.cleanup, admissionErr = t.operation.BeginCleanup()
	}
	operation := t.cleanup
	// Cleanup must still stop access when the audit store is unavailable.
	if err := m.provider.Close(ctx, t.handle); err != nil {
		t.Uncertain = true
		m.uncertain[name] = true
		return errors.Join(audit.ErrUncertain, admissionErr)
	}
	delete(m.tunnels, name)
	delete(m.uncertain, name)
	if operation == nil {
		return errors.Join(audit.ErrUncertain, admissionErr)
	}
	return operation.Complete("succeeded", "not_applicable", "close", "")
}

// Status reports one endpoint's tunnel, or ErrNotFound when it is closed.
func (m *Manager) Status(name string) (*Tunnel, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	t, ok := m.tunnels[name]
	if !ok {
		if m.uncertain[name] {
			return nil, audit.ErrUncertain
		}
		return nil, fmt.Errorf("%w: %q", ErrNotFound, name)
	}
	clone := *t
	return &clone, nil
}

// Reap closes tunnels past their lifetime ceiling and emits a close
// receipt per tunnel; returns the closed endpoint names.
//
// ponytail: idle reap is deferred — a meaningful "no traffic" signal needs
// provider-side channel stats (sish) the workload cannot report today; the
// lifetime ceiling is the backstop. Upgrade path: provider reports
// forwarded-connection activity and Reap also checks a lastUsed mark.
func (m *Manager) Reap(ctx context.Context) ([]string, error) {
	m.mu.Lock()
	var expired []string
	for name, t := range m.tunnels {
		if !m.now().Before(t.ExpiresAt) {
			expired = append(expired, name)
		}
	}
	m.mu.Unlock()
	var closed []string
	var failure error
	for _, name := range expired {
		err := m.Close(ctx, name)
		if err != nil {
			failure = errors.Join(failure, err)
		}
		m.mu.Lock()
		_, remains := m.tunnels[name]
		m.mu.Unlock()
		if !remains {
			closed = append(closed, name)
		}
	}
	return closed, failure
}

// List returns the live tunnels without provider handles.
func (m *Manager) List() []Tunnel {
	m.mu.Lock()
	out := make([]Tunnel, 0, len(m.tunnels))
	for _, t := range m.tunnels {
		out = append(out, *t)
	}
	m.mu.Unlock()
	return out
}

// DeclaredEndpoints reports only the reviewed names and target classes.
func (m *Manager) DeclaredEndpoints() []map[string]string {
	names := make([]string, 0, len(m.cfg.Endpoints))
	for name := range m.cfg.Endpoints {
		names = append(names, name)
	}
	slices.Sort(names)
	out := make([]map[string]string, 0, len(names))
	for _, name := range names {
		kind := "service"
		if m.cfg.Endpoints[name].Target == TargetHubListener {
			kind = TargetHubListener
		}
		out = append(out, map[string]string{"name": name, "target_kind": kind})
	}
	return out
}

func (m *Manager) auditRecord(op, name, outcome, receipt string) audit.Event {
	event := audit.NewEvent("exposure", m.auth.PrincipalID, outcome)
	event.Name = name
	event.ContextID = m.auth.ContextID
	event.RuntimeID = m.auth.RuntimeID
	event.PolicyRevision = m.auth.PolicyVersion
	event.Environment = m.auth.Environment
	event.Generation = m.auth.Generation
	// Only static operation/reason codes enter this producer's receipt.
	event.Receipt = op + " " + receipt
	return event
}

func (m *Manager) audit(op, name, outcome, receipt string) error {
	event := m.auditRecord(op, name, outcome, receipt)
	event.OperationID = event.EventID
	event.Phase = "decision"
	event.Action = "exposure." + op
	event.Authorization = "allow"
	if outcome == "denied" {
		event.Authorization = "deny"
	}
	event.Execution, event.Delivery = "not_started", "not_applicable"
	return m.ledger.Append(event)
}
