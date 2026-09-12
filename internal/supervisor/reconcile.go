package supervisor

import (
	"context"
	"encoding/json"
	"errors"
	hubruntime "github.com/letya999/hermes-hub/internal/runtime"
	"net/http"
	"strings"
	"time"
)

// ReconcileHealth inspects actual runtime state independently from the desired
// work ledger. Unknown inspection failures never authorize idle shutdown.
func (m *Manager) ReconcileHealth(ctx context.Context, now time.Time) {
	m.mu.Lock()
	keys := make([]string, 0, len(m.items))
	for key := range m.items {
		keys = append(keys, key)
	}
	m.mu.Unlock()
	for _, key := range keys {
		lock := m.lockFor(key)
		lock.Lock()
		m.mu.Lock()
		entry := m.items[key]
		if entry == nil {
			m.mu.Unlock()
			lock.Unlock()
			continue
		}
		snapshot := entry.Runtime
		desired := m.runtimeDesiredLocked(entry)
		auth := entry.auth
		m.mu.Unlock()
		health, readiness := "stopped", "not_checked"
		connectorHealth := "not_checked"
		ownership := "not_checked"
		if snapshot.State != Stopped {
			probeCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
			ownership = "unverified"
			if m.VerifyOwnership(probeCtx, snapshot) == nil {
				ownership = "verified"
			}
			out, err := m.command(probeCtx, "inspect", "--format", "{{.State.Status}}", snapshot.Container)
			if err != nil {
				health = "unknown"
				if present, presenceErr := m.containerPresent(probeCtx, snapshot.Container); presenceErr == nil && !present {
					health = "missing"
					ownership = "absent"
				}
			} else {
				health = strings.TrimSpace(string(out))
				if health == "running" {
					readiness = "unavailable"
					if auth != "" && m.ready(probeCtx, snapshot.Address, auth) == nil {
						readiness = "ready"
					}
					connectorHealth = "unknown"
					if auth != "" && m.cfg.Probe == nil {
						readiness = "unavailable"
						observed, e := m.runtimeHealth(probeCtx, snapshot.Address, auth)
						if e == nil {
							readiness = observed.HermesReadiness
							connectorHealth = observed.ConnectorHealth
						}
					}

				}
			}
			cancel()
		}
		m.mu.Lock()
		if current := m.items[key]; current != nil && current.Generation == snapshot.Generation {
			previous := current.Runtime
			current.RuntimeHealth, current.HermesReadiness, current.HealthCheckedAt, current.Desired = health, readiness, now, desired
			current.Ownership = ownership
			current.ConnectorHealth = connectorHealth
			if desired && recoverableRuntime(current.Runtime) && current.FailureGeneration != current.Generation {
				m.recordRuntimeFailureLocked(&current.Runtime)
			}
			absentWithoutWork := health == "missing" && !desired && current.State != Idle
			if absentWithoutWork {
				current.State, current.IdleDeadline = Stopped, time.Time{}
			}
			if err := m.persistLocked(); err != nil {
				current.Runtime = previous
			} else if absentWithoutWork {
				m.releaseSlotLocked(key)
			}
		}
		m.mu.Unlock()
		lock.Unlock()
	}
}

func (m *Manager) runtimeDesiredLocked(entry *runtimeEntry) bool {
	if _, ok := m.pins[runtimeKey(Binding{PrincipalID: entry.PrincipalID, ContextID: entry.ContextID, RuntimeMode: entry.RuntimeMode})]; ok {
		return true
	}
	if entry.Leases > 0 {
		return true
	}
	for _, record := range m.jobs {
		if record.Request.PrincipalID == entry.PrincipalID && record.Request.ContextID == entry.ContextID && !terminalRunStatus(record.Status) {
			return true
		}
	}
	return false
}

// RecoverMissingWork replaces missing or verified exited infrastructure. Admission and
// result recovery remain tied to the original durable job/run, never its prompt.
func (m *Manager) RecoverMissingWork(ctx context.Context) {
	m.mu.Lock()
	var requests []jobRecord
	for _, record := range m.jobs {
		if terminalRunStatus(record.Status) {
			continue
		}
		for _, entry := range m.items {
			if entry.PrincipalID == record.Request.PrincipalID && entry.ContextID == record.Request.ContextID && recoverableRuntime(entry.Runtime) {
				requests = append(requests, record)
				break
			}
		}
	}
	m.mu.Unlock()
	for _, record := range requests {
		binding, err := m.bindingFor(record.Request)
		if err != nil {
			continue
		}
		key := runtimeKey(binding)
		recoveryLock := m.lockFor("recovery:" + key)
		recoveryLock.Lock()
		lock := m.lockFor(key)
		lock.Lock()
		m.mu.Lock()
		entry := m.items[key]
		latest := m.jobs[executeJobKey(record.Request)]
		if entry == nil || !recoverableRuntime(entry.Runtime) || terminalRunStatus(latest.Status) {
			m.mu.Unlock()
			lock.Unlock()
			recoveryLock.Unlock()
			continue
		}
		entry.restored = true
		m.mu.Unlock()
		lock.Unlock()
		callCtx, cancel := context.WithTimeout(ctx, 130*time.Second)
		lease, _, err := m.Acquire(callCtx, binding, LeaseUncertain)
		if err == nil {
			// Keep the hold even when ownership persistence fails.
			_ = m.attachJobLease(lease.ID, executeJobKey(record.Request))
		}
		cancel()
		recoveryLock.Unlock()
	}
	// Lifecycle operations own no Hermes prompt. Persisted binding and opaque
	// lease IDs let infrastructure recover while the caller retains its hold.
	m.mu.Lock()
	var bindings []Binding
	for _, entry := range m.items {
		if recoverableRuntime(entry.Runtime) && entry.binding.PrincipalID != "" {
			for _, lease := range entry.leases {
				if lease.Kind == LeaseLifecycle {
					bindings = append(bindings, entry.binding)
					break
				}
			}
		}
	}
	m.mu.Unlock()
	for _, binding := range bindings {
		key := runtimeKey(binding)
		coordinator := m.lockFor("recovery:" + key)
		coordinator.Lock()
		lock := m.lockFor(key)
		lock.Lock()
		m.mu.Lock()
		entry := m.items[key]
		needed := false
		if entry != nil && recoverableRuntime(entry.Runtime) {
			for _, lease := range entry.leases {
				if lease.Kind == LeaseLifecycle {
					needed = true
					break
				}
			}
			if needed {
				entry.restored = true
			}
		}
		m.mu.Unlock()
		lock.Unlock()
		if needed {
			callCtx, cancel := context.WithTimeout(ctx, 130*time.Second)
			if _, err := m.Ensure(callCtx, binding); err == nil {
				_ = m.ReleaseBinding(binding)
			}
			cancel()
		}
		coordinator.Unlock()
	}
}

func recoverableRuntime(runtime Runtime) bool {
	return runtime.RuntimeHealth == "missing" || (runtime.Ownership == "verified" && (runtime.RuntimeHealth == "exited" || runtime.RuntimeHealth == "dead"))
}

func (m *Manager) runtimeHealth(ctx context.Context, address, auth string) (hubruntime.RuntimeHealth, error) {
	var health hubruntime.RuntimeHealth
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, address+"/v1/health", nil)
	if err != nil {
		return health, err
	}
	request.Header.Set("Authorization", "Bearer "+auth)
	response, err := m.cfg.HTTP.Do(request)
	if err != nil {
		return health, err
	}
	defer response.Body.Close()
	if response.StatusCode != 200 || json.NewDecoder(http.MaxBytesReader(nil, response.Body, 16*1024)).Decode(&health) != nil {
		return health, errors.New("runtime health unavailable")
	}
	if health.Connected < 0 || health.Degraded < 0 || health.Unknown < 0 || health.Connected > 64 || health.Degraded > 64 || health.Unknown > 64 || health.Connected+health.Degraded+health.Unknown > 64 || health.ExternalConnections != "not_probed" {
		return health, errors.New("invalid connection health bounds")
	}
	if health.HermesReadiness != "ready" && health.HermesReadiness != "unavailable" {
		return health, errors.New("invalid Hermes health")
	}
	switch health.ConnectorHealth {
	case "healthy", "degraded", "unknown", "not_configured":
	default:
		return health, errors.New("invalid connector health")
	}
	return health, nil
}
