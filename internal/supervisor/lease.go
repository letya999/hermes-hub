package supervisor

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	hubruntime "github.com/letya999/hermes-hub/internal/runtime"
	"io"
	"log"
	"net/http"
	"strings"
	"time"
)

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
// only answers "stale runtime lease" for them), job-owned leases whose job
// is terminal or gone, and supervisor-owned leases past their claim deadline —
// owner "supervisor" is the transient state between Acquire and attachJobLease,
// so an expired one means the request that held it died and nothing can ever
// release it. Caller-held stream/approval/lifecycle leases attached to a job
// have no time bound — ExpiresAt is not a validity check for them. Uncertain
// jobs are resolved by ExpireUncertain through the durable control path, not
// here. Without this an orphaned lease keeps runtimeDesiredLocked true forever
// and the runtime is never reaped. m.mu must be held.
func (m *Manager) sweepStaleLeasesLocked(now time.Time) {
	for _, entry := range m.items {
		changed := false
		for id, lease := range entry.leases {
			stale := lease.Generation != entry.Generation
			if !stale {
				if jobID, ok := strings.CutPrefix(lease.Owner, "job:"); ok {
					record, found := m.jobs[jobID]
					stale = !found || terminalRunStatus(record.Status)
				} else if lease.Owner == "supervisor" && !lease.ExpiresAt.IsZero() {
					// The claim deadline is Acquire's own bound; WarmTTL of
					// grace keeps a slow-but-alive request from losing its
					// hold mid-flight.
					stale = now.After(lease.ExpiresAt.Add(m.cfg.WarmTTL))
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
		// A failed idle cleanup retains its deadline and must be retried.
		if (runtime.State == Idle || runtime.State == Degraded && !runtime.IdleDeadline.IsZero()) && !m.runtimeDesiredLocked(runtime) && runtime.RuntimeHealth != "unknown" && runtime.Ownership != "unverified" && !runtime.IdleDeadline.After(now) {
			keys = append(keys, key)
		}
	}
	m.mu.Unlock()
	for _, key := range keys {
		lock := m.lockFor(key)
		lock.Lock()
		m.mu.Lock()
		runtime := m.items[key]
		if runtime == nil || (runtime.State != Idle && (runtime.State != Degraded || runtime.IdleDeadline.IsZero())) || m.runtimeDesiredLocked(runtime) || runtime.RuntimeHealth == "unknown" || runtime.Ownership == "unverified" || runtime.IdleDeadline.After(now) {
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
		if err != nil {
			// A runtime may exit after the ownership check and before Docker stop.
			// Only an exact-ID stopped-state observation can resolve that race.
			if out, stateErr := m.command(ctx, "inspect", "--format", "{{.State.Running}}", container); stateErr == nil && strings.TrimSpace(string(out)) == "false" {
				err = nil
			}
		}
		if err == nil {
			_, err = m.command(ctx, "rm", "-f", container)
		}
		// A managed runtime's control-relay sidecar shares its generation; it is
		// removed only after the runtime container is gone and always by the
		// verified <name>-ctl ownership labels.
		if err == nil {
			err = m.removeOwnedSidecar(ctx, snapshot)
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

func validLeaseKind(kind LeaseKind) bool {
	switch kind {
	case LeaseJob, LeaseStream, LeaseApproval, LeaseLifecycle, LeaseUncertain:
		return true
	default:
		return false
	}
}
