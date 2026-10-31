package supervisor

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"time"

	hubruntime "github.com/letya999/hermes-hub/internal/runtime"
)

var (
	wedgedReadinessWindow     = 3 * time.Minute
	wedgedRemediationCooldown = 5 * time.Minute
)

// ReconcileHealth inspects actual runtime state independently from the desired
// work ledger. Unknown inspection failures never authorize idle shutdown.
func (m *Manager) ReconcileHealth(ctx context.Context, now time.Time) {
	started := time.Now()
	m.mu.Lock()
	holds := make([]AdministrativeHold, 0, len(m.holds))
	for _, hold := range m.holds {
		if hold.Active {
			holds = append(holds, hold)
		}
	}
	m.mu.Unlock()
	for _, hold := range holds {
		bounded, cancel := context.WithTimeout(ctx, 45*time.Second)
		_ = m.containHeldScope(bounded, hold)
		cancel()
	}
	m.mu.Lock()
	keys := make([]string, 0, len(m.items))
	for key := range m.items {
		keys = append(keys, key)
	}
	m.mu.Unlock()
	for _, key := range keys {
		lock := m.lockFor(key)
		// A request-held key lock (a spawn parked on a full capacity semaphore)
		// must not wedge the sweep: skip it and retry on the next tick.
		if !lock.TryLock() {
			continue
		}
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
		lock.Unlock()
		health, readiness := "stopped", "not_checked"
		connectorHealth := "not_checked"
		ownership := "not_checked"
		var observed *hubruntime.RuntimeHealth
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
						if result, e := m.runtimeHealth(probeCtx, snapshot.Address, auth); e == nil {
							observed = &result
							readiness = observed.HermesReadiness
							connectorHealth = observed.ConnectorHealth
						}
					}

				}
			}
			cancel()
		}
		remediate := false
		if !lock.TryLock() {
			continue
		}
		m.mu.Lock()
		if current := m.items[key]; current != nil && current.Generation == snapshot.Generation {
			previous := current.Runtime
			current.RuntimeHealth, current.HermesReadiness, current.HealthCheckedAt, current.Desired = health, readiness, now, desired
			current.Ownership = ownership
			current.ConnectorHealth = connectorHealth
			if observed != nil {
				current.ToolSurface, current.MissingTools, current.ToolReasons, current.ModelStatus = observed.ToolSurface, observed.MissingTools, observed.ToolReasons, observed.ModelStatus
			}
			// A degraded runtime still serves (e.g. host disk over the
			// readiness threshold): restart cannot remediate a host-side
			// cause and kills in-flight runs. Remediate only unavailable.
			wedged := desired && ownership == "verified" && health == "running" && readiness == "unavailable"
			if wedged {
				if current.readinessBadSince.IsZero() {
					current.readinessBadSince = now
				}
			} else {
				current.readinessBadSince = time.Time{}
			}
			// A live typed lease means a job, stream, approval or lifecycle
			// is in flight — restarting now would kill it, so remediation
			// waits for a free runtime rather than racing the lease holder.
			// A verified personal-supervisor restart request bypasses the
			// timing gates only — ownership and lease checks still apply.
			// An undesired scope (administrative hold, no demand) is never
			// restarted: containment authority outranks any advisory request.
			personal := desired && current.personalRestart && ownership == "verified" && len(current.leases) == 0
			current.personalRestart = false
			remediate = personal || wedged && len(current.leases) == 0 && current.consecutiveUncertain >= 2 &&
				now.Sub(current.readinessBadSince) >= wedgedReadinessWindow &&
				now.Sub(current.lastRemediatedAt) >= wedgedRemediationCooldown
			if remediate {
				m.recordIncidentLocked(current, "wedged")
			}
			if desired && recoverableRuntime(current.Runtime) && current.FailureGeneration != current.Generation {
				m.recordRuntimeFailureLocked(&current.Runtime)
			}
			absentWithoutWork := health == "missing" && !desired
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
		if remediate && snapshot.Container != "" {
			if _, err := m.command(ctx, "restart", "--time", "30", snapshot.Container); err == nil {
				m.mu.Lock()
				if current := m.items[key]; current != nil && current.Generation == snapshot.Generation {
					current.restored, current.consecutiveUncertain, current.lastRemediatedAt = true, 0, now
					_ = m.persistLocked()
				}
				m.mu.Unlock()
			}
		}
		lock.Unlock()
	}
	m.sampleStats(ctx)
	m.mu.Lock()
	m.watchdog.Sweeps++
	m.watchdog.Entries = len(keys)
	m.watchdog.LastSweepAt = now
	m.watchdog.LastSweepMS = time.Since(started).Milliseconds()
	m.mu.Unlock()
}

// sampleStats takes one bounded resource reading for every owned container
// per sweep. A failed or absent sampler records nothing and never feeds
// remediation decisions — stats are reporting, not authority.
func (m *Manager) sampleStats(ctx context.Context) {
	if m.cfg.Stats == nil {
		return
	}
	m.mu.Lock()
	containers := make([]string, 0, len(m.items))
	for _, entry := range m.items {
		if entry.Container != "" && entry.State != Stopped {
			containers = append(containers, entry.Container)
		}
	}
	m.mu.Unlock()
	if len(containers) == 0 {
		return
	}
	statsCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
	stats, err := m.cfg.Stats(statsCtx, containers)
	cancel()
	if err != nil {
		return
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	for _, entry := range m.items {
		if stat, ok := stats[entry.Container]; ok {
			entry.CPUPercent, entry.MemBytes = stat.CPUPercent, stat.MemBytes
		}
	}
}

// reconcileUnconfirmed retries observation for jobs whose outcome was never
// confirmed. A run-bound job is re-observed through the runtime's stream so a
// late or replayed terminal event settles the record once.
func (m *Manager) reconcileUnconfirmed(ctx context.Context, now time.Time) {
	m.mu.Lock()
	var targets []jobRecord
	for _, record := range m.jobs {
		if !record.Unconfirmed || terminalRunStatus(record.Status) || record.UnconfirmedSince.IsZero() || !m.operationallyCurrentLocked(record) {
			continue
		}
		if now.Sub(record.UnconfirmedSince) < 30*time.Second {
			continue
		}
		targets = append(targets, record)
	}
	m.mu.Unlock()
	for _, record := range targets {
		if !now.Before(record.UnconfirmedSince.Add(hubruntime.RunBudget())) {
			// The run's own budget has elapsed: an outcome that never
			// confirmed is still named — failed with a timeout cause —
			// rather than left unknown forever.
			timeout := hubruntime.ExecuteResponse{JobID: record.Request.JobID, RunID: record.Response.RunID, SessionID: record.Response.SessionID, RuntimeGeneration: record.Generation, Status: "failed", LastEvent: "run.timeout"}
			_ = m.finishJobGeneration(record.Request, timeout, "failed", record.Generation)
			m.mu.Lock()
			for _, entry := range m.items {
				if entry.PrincipalID == record.Request.PrincipalID && entry.ContextID == record.Request.ContextID {
					m.recordIncidentLocked(entry, "timeout")
					break
				}
			}
			_ = m.persistLocked()
			m.mu.Unlock()
			continue
		}
		lock := m.lockFor("observation:" + executeJobKey(record.Request))
		if !lock.TryLock() {
			continue
		}
		record := record
		// Reconciliation outlives the sweep tick that scheduled it; the
		// stream client's own deadlines bound each observe/readmit call.
		go func() { // #nosec G118 -- detached reconcile worker
			defer lock.Unlock()
			m.reconcileJob(context.Background(), record)
		}()
	}
}

// reconcileJob re-observes one unconfirmed run against its binding's current
// runtime generation. Events land through the same durable path as a live
// stream, so a replayed or late terminal outcome settles the record once.
func (m *Manager) reconcileJob(ctx context.Context, record jobRecord) {
	m.mu.Lock()
	current := m.operationallyCurrentLocked(record)
	m.mu.Unlock()
	if !current {
		return
	}
	binding, err := m.bindingFor(record.Request)
	if err != nil {
		return
	}
	callCtx, cancel := context.WithTimeout(ctx, 130*time.Second)
	lease, runtime, err := m.Acquire(callCtx, binding, LeaseUncertain)
	cancel()
	if err != nil {
		return
	}
	_ = m.attachJobLease(lease.ID, executeJobKey(record.Request))
	defer m.settleJobLeases(record.Request, lease)
	// The attempt hold is request-scoped: the nonterminal record already pins
	// the runtime as desired, so keeping the lease after the observe/readmit
	// call would accumulate one permanent hold per sweep on an open record.
	defer func() { _ = m.ReleaseLease(lease.ID) }()
	fresh, err := m.rebindJobObservation(record.Request, record, runtime)
	if err != nil || terminalRunStatus(fresh.Status) {
		return
	}
	var endpoint string
	var body []byte
	if fresh.Response.RunID != "" {
		// The run was admitted before the observation loss: re-attach to it.
		endpoint = "/v1/observe"
		body, _ = json.Marshal(hubruntime.RunReference{ExecuteRequest: record.Request, RunID: fresh.Response.RunID, SessionID: fresh.Response.SessionID, RuntimeGeneration: runtime.Generation})
	} else {
		// Admission itself was lost before a run id arrived. Re-post the
		// durable request under its original idempotency key: Hermes replays
		// the same run rather than duplicating the work.
		endpoint = "/v1/readmit"
		body, _ = json.Marshal(record.Request)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, runtime.Address+endpoint, bytes.NewReader(body))
	if err != nil {
		return
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", hubruntime.RunStreamContentType)
	req.Header.Set("Authorization", "Bearer "+binding.runtimeAuth)
	response, err := m.cfg.StreamHTTP.Do(req)
	if err != nil {
		return
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK || response.Header.Get("Content-Type") != hubruntime.RunStreamContentType {
		// A readmit refusal is permanent: the immutable durable request can
		// never produce a different verdict, and a lost admission receipt
		// cannot reappear. Name the outcome failed rather than re-asking
		// the same question until the run budget expires.
		if endpoint == "/v1/readmit" && response.StatusCode == http.StatusConflict {
			lost := hubruntime.ExecuteResponse{JobID: record.Request.JobID, RuntimeGeneration: fresh.Generation, Status: "failed", LastEvent: "run.failed"}
			_ = m.finishJobGeneration(record.Request, lost, "failed", fresh.Generation)
		}
		return
	}
	m.consumeJobStream(record.Request, runtime, response.Body, nil)
}

func (m *Manager) runtimeDesiredLocked(entry *runtimeEntry) bool {
	if m.holds[holdKey(entry.PrincipalID, entry.ContextID)].Active {
		return false
	}
	if _, ok := m.pins[runtimeKey(Binding{PrincipalID: entry.PrincipalID, ContextID: entry.ContextID, RuntimeMode: entry.RuntimeMode})]; ok {
		return true
	}
	if entry.Leases > 0 {
		return true
	}
	for _, record := range m.jobs {
		if record.Request.PrincipalID == entry.PrincipalID && record.Request.ContextID == entry.ContextID && !terminalRunStatus(record.Status) && m.operationallyCurrentLocked(record) {
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
		if terminalRunStatus(record.Status) || !m.operationallyCurrentLocked(record) {
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
		if entry == nil || !recoverableRuntime(entry.Runtime) || terminalRunStatus(latest.Status) || !m.operationallyCurrentLocked(latest) {
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
	if health.HermesReadiness != "ready" && health.HermesReadiness != "degraded" && health.HermesReadiness != "unavailable" {
		return health, errors.New("invalid Hermes health")
	}
	switch health.ConnectorHealth {
	case "healthy", "degraded", "unknown", "not_configured":
	default:
		return health, errors.New("invalid connector health")
	}
	// R1.4-R1.6 additive observations: an older runtime may omit them, a
	// current one stays inside the fixed vocabulary and bounded lists.
	switch health.ToolSurface {
	case "", "ok", "degraded", "not_probed":
	default:
		return health, errors.New("invalid tool surface")
	}
	switch health.ModelStatus {
	case "", "ok", "model_not_found", "unavailable", "not_configured":
	default:
		return health, errors.New("invalid model status")
	}
	if len(health.MissingTools) > 64 || len(health.ToolReasons) > 16 {
		return health, errors.New("invalid tool surface bounds")
	}
	for _, name := range health.MissingTools {
		if len(name) == 0 || len(name) > 128 {
			return health, errors.New("invalid tool surface bounds")
		}
	}
	for key, reason := range health.ToolReasons {
		if len(key) > 64 || len(reason) > 256 {
			return health, errors.New("invalid tool surface bounds")
		}
	}
	return health, nil
}
