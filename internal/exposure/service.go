package exposure

import (
	"context"
	"fmt"
	"sync"
	"time"

	"github.com/letya999/hermes-hub/internal/audit"
	"github.com/letya999/hermes-hub/internal/identity"
)

// Provider starts and stops one tunnel workload per open exposure (ADR-0077
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

	mu      sync.Mutex
	tunnels map[string]*Tunnel
	done    chan struct{}
	stopped chan struct{}
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
		ledger: opts.Ledger, now: now, tunnels: map[string]*Tunnel{},
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
				m.Reap(context.Background())
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
		m.audit("open", name, "denied", "unknown endpoint")
		return nil, err
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
	if t, ok := m.tunnels[name]; ok {
		return t, nil
	}
	if len(m.tunnels) >= m.cfg.Max() {
		m.audit("open", name, "denied", "concurrency cap")
		return nil, fmt.Errorf("%w: %d concurrent exposures", ErrBusy, m.cfg.Max())
	}
	if m.provider == nil {
		m.audit("open", name, "denied", "no tunnel provider")
		return nil, ErrUnavailable
	}
	spec := Spec{Provider: m.cfg.Provider, Endpoint: name, Bind: bind,
		Target: ep.Target, PublicName: public, Idle: m.cfg.Idle(), Lifetime: m.cfg.Lifetime()}
	handle, err := m.provider.Open(ctx, spec)
	if err != nil {
		m.audit("open", name, "failed", "provider")
		return nil, fmt.Errorf("%w: %v", ErrUnavailable, err)
	}
	now := m.now()
	t := &Tunnel{Endpoint: name, PublicName: public, Target: ep.Target,
		Provider: m.cfg.Provider, OpenedAt: now, ExpiresAt: now.Add(spec.Lifetime), handle: handle}
	m.tunnels[name] = t
	m.audit("open", public, "open", bind)
	return t, nil
}

// Close stops the tunnel workload for an endpoint and emits a close receipt.
func (m *Manager) Close(ctx context.Context, name string) error {
	m.mu.Lock()
	t, ok := m.tunnels[name]
	if ok {
		delete(m.tunnels, name)
	}
	m.mu.Unlock()
	if !ok {
		return fmt.Errorf("%w: %q", ErrNotFound, name)
	}
	if err := m.provider.Close(ctx, t.handle); err != nil {
		return fmt.Errorf("%w: %v", ErrUnavailable, err)
	}
	m.audit("close", t.PublicName, "close", t.PublicName)
	return nil
}

// Status reports one endpoint's tunnel, or ErrNotFound when it is closed.
func (m *Manager) Status(name string) (*Tunnel, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	t, ok := m.tunnels[name]
	if !ok {
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
func (m *Manager) Reap(ctx context.Context) []string {
	m.mu.Lock()
	var expired []*Tunnel
	for _, t := range m.tunnels {
		if !m.now().Before(t.ExpiresAt) {
			expired = append(expired, t)
		}
	}
	m.mu.Unlock()
	var closed []string
	for _, t := range expired {
		if err := m.provider.Close(ctx, t.handle); err != nil {
			m.audit("reap", t.PublicName, "failed", "provider")
			continue
		}
		m.mu.Lock()
		delete(m.tunnels, t.Endpoint)
		m.mu.Unlock()
		m.audit("reap", t.PublicName, "close", "lifetime")
		closed = append(closed, t.Endpoint)
	}
	return closed
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

// audit appends a bounded "exposure" event; op names the operation. Audit
// failures never unblock a call.
func (m *Manager) audit(op, name, outcome, receipt string) {
	if m.ledger == nil {
		return
	}
	event := audit.NewEvent("exposure", m.auth.PrincipalID, outcome)
	event.Name = name
	event.ContextID = m.auth.ContextID
	event.RuntimeID = m.auth.RuntimeID
	event.PolicyRevision = m.auth.PolicyVersion
	receipt = op + " " + receipt
	if len(receipt) > 240 {
		receipt = receipt[:240]
	}
	event.Receipt = receipt
	_ = m.ledger.Append(event)
}
