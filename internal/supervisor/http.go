package supervisor

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	identitycontract "github.com/letya999/hermes-hub/contracts/identity/v1"
	hubruntime "github.com/letya999/hermes-hub/internal/runtime"
	"io"
	"log"
	"net/http"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/letya999/hermes-hub/internal/stack"
)

func (m *Manager) serveHTTP(w http.ResponseWriter, r *http.Request) {
	if strings.HasPrefix(r.URL.Path, "/v1/identity/links/") {
		client, ok := m.identityClient().(identitycontract.LinkClient)
		if !ok || m.cfg.IdentityLinkAuth == "" || m.cfg.IdentityLinkAuth == m.cfg.RuntimeAuth {
			writeJSON(w, http.StatusServiceUnavailable, map[string]string{"error": identitycontract.ErrUnavailable.Error()})
			return
		}
		identitycontract.LinkHandler(client, m.cfg.IdentityLinkAuth).ServeHTTP(w, r)
		return
	}
	if r.URL.Path == "/v1/admin/holds" {
		m.administrativeHold(w, r)
		return
	}
	if r.URL.Path == "/v1/admin/identity/deactivate" {
		m.deactivateIdentity(w, r)
		return
	}
	if !m.authorized(r) {
		writeJSON(w, http.StatusUnauthorized, map[string]string{"error": "unauthorized"})
		return
	}
	switch r.URL.Path {
	case "/v1/holds":
		if r.Method != http.MethodGet {
			writeJSON(w, 405, map[string]string{"error": "method not allowed"})
			return
		}
		m.mu.Lock()
		holds := make([]AdministrativeHold, 0, len(m.holds))
		for _, hold := range m.holds {
			holds = append(holds, hold)
		}
		m.mu.Unlock()
		writeJSON(w, 200, holds)
	case "/v1/holds/ack":
		m.ackHoldNotice(w, r)
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
	case "/v1/status":
		if r.Method != http.MethodGet {
			writeJSON(w, http.StatusMethodNotAllowed, map[string]string{"error": "method not allowed"})
			return
		}
		writeJSON(w, http.StatusOK, m.StatusView())
	case "/v1/metrics":
		if r.Method != http.MethodGet {
			writeJSON(w, http.StatusMethodNotAllowed, map[string]string{"error": "method not allowed"})
			return
		}
		w.Header().Set("Content-Type", "text/plain; version=0.0.4")
		m.StatusView().Metrics(w)
	case "/v1/leases":
		m.lease(w, r)
	case "/v1/wake":
		m.wake(w, r)
	case "/v1/self-env":
		m.selfEnv(w, r)
	case "/v1/settings":
		m.settingsEditHTTP(w, r)
	case "/v1/skill-apply":
		m.skillApply(w, r)
	case "/v1/skill-creds":
		m.skillCreds(w, r)
	case "/v1/skill-ref-lease":
		m.skillRefLease(w, r)
	case "/v1/restart":
		m.restartHTTP(w, r)
	case "/v1/restart-request":
		m.restartRequestHTTP(w, r)
	case "/v1/respawn":
		m.respawnHTTP(w, r)
	case "/v1/artifact":
		m.artifactHTTP(w, r)
	case "/v1/usage":
		m.usageHTTP(w, r)
	case "/v1/session-read":
		m.sessionRead(w, r)
	case "/v1/compact":
		m.compactHTTP(w, r)
	case "/v1/goal":
		m.goalHTTP(w, r)
	case "/v1/script":
		m.scriptHTTP(w, r)
	default:
		if strings.HasPrefix(r.URL.Path, "/v1/identity/") {
			client := m.identityClient()
			if client == nil {
				writeJSON(w, http.StatusServiceUnavailable, map[string]string{"error": identitycontract.ErrUnavailable.Error()})
				return
			}
			identitycontract.Handler(client, m.cfg.RuntimeAuth).ServeHTTP(w, r)
			return
		}
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

// wake warms the request's runtime without a lease or job record (SPEC-0053
// R2.1): the gateway fires it right after spooling so a cold spawn overlaps
// its own work. bindingFor gives the same authorization as /v1/jobs; Ensure
// is idempotent, and the request never carries prompt text.
func (m *Manager) wake(w http.ResponseWriter, r *http.Request) {
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
	hold := m.holds[holdKey(request.PrincipalID, request.ContextID)]
	m.mu.Unlock()
	if hold.Active || request.OperationalEpoch != hold.Epoch {
		writeJSON(w, http.StatusConflict, map[string]string{"error": "administrative admission fence"})
		return
	}
	runtime, err := m.Ensure(r.Context(), binding)
	if err != nil {
		log.Printf("wake failed principal=%s context=%s class=%s", binding.PrincipalID, binding.ContextID, runtimeFailureClass(err))
		writeRuntimeFailure(w, http.StatusServiceUnavailable, err)
		return
	}
	// Wake is a warm-up peek, not a hold: the Ensure reference returns here so
	// the runtime settles back to its warm idle hold instead of accumulating a
	// lease per wake call.
	_ = m.ReleaseBinding(binding)
	writeJSON(w, http.StatusOK, runtime)
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
	if !m.rightsGate(w, r, binding.PrincipalID, "agents", "restart_self", "runtime") {
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
		writeRuntimeFailure(w, http.StatusServiceUnavailable, err)
		return
	}
	defer m.ReleaseLease(lease.ID)
	applied, err := m.applyRuntimeRestart(r.Context(), runtime, binding.runtimeAuth, 1)
	if err != nil {
		writeJSON(w, http.StatusBadGateway, map[string]string{"error": "runtime restart unavailable"})
		return
	}
	writeJSON(w, http.StatusOK, map[string]bool{"applied": applied})
}

// Hold the acquisition lock through the actual daemon restart and readiness.
// A successful marker response precedes SIGINT by 250ms; it is not readiness.
func (m *Manager) applyRuntimeRestart(ctx context.Context, runtime Runtime, auth string, ownLeases int) (bool, error) {
	key := runtimeKey(Binding{PrincipalID: runtime.PrincipalID, ContextID: runtime.ContextID, RuntimeMode: runtime.RuntimeMode})
	lock := m.lockFor(key)
	lock.Lock()
	defer lock.Unlock()
	return m.applyRuntimeRestartLocked(ctx, runtime, auth, ownLeases)
}

func (m *Manager) applyRuntimeRestartLocked(ctx context.Context, runtime Runtime, auth string, ownLeases int) (bool, error) {
	key := runtimeKey(Binding{PrincipalID: runtime.PrincipalID, ContextID: runtime.ContextID, RuntimeMode: runtime.RuntimeMode})
	m.mu.Lock()
	entry := m.items[key]
	valid := entry != nil && entry.Container == runtime.Container && entry.Generation == runtime.Generation
	busy := valid && entry.Leases > ownLeases
	m.mu.Unlock()
	if !valid {
		return false, errors.New("runtime changed before restart")
	}
	if busy {
		return false, nil
	}
	if m.usingKubernetes() {
		return m.restartKubernetesLocked(ctx, runtime)
	}
	container, err := m.ownedContainerID(ctx, runtime)
	if err != nil {
		return false, err
	}
	if runtime.Managed {
		// The agent's config is read-only: refresh its host source before
		// Docker remounts it, otherwise a saved overlay fails preflight.
		if _, err := m.materializeHermesConfig(entry.binding, m.environment()); err != nil {
			return false, err
		}
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, runtime.Address+"/v1/restart", nil)
	if err != nil {
		return false, err
	}
	request.Header.Set("Authorization", "Bearer "+auth)
	response, err := m.cfg.HTTP.Do(request)
	if err != nil {
		return false, err
	}
	var result struct {
		Scheduled bool `json:"scheduled"`
	}
	err = json.NewDecoder(io.LimitReader(response.Body, 64*1024)).Decode(&result)
	_ = response.Body.Close()
	if response.StatusCode != http.StatusOK || err != nil {
		return false, errors.New("runtime restart response invalid")
	}
	if !result.Scheduled {
		return false, nil
	}
	m.mu.Lock()
	entry.restored = true // A failed restart must force fresh ownership/readiness checks.
	entry.Runtime.Restarts++
	m.mu.Unlock()
	// Complete an accepted restart even when the notifying client's short
	// deadline expires. New jobs remain behind this same acquisition lock.
	restart, cancel := context.WithTimeout(context.Background(), 4*time.Minute)
	defer cancel()
	// SIGTERM first, then 30 s before SIGKILL — an abrupt kill loses Hermes's
	// own cleanup and any in-flight work the lease check could not see.
	if _, err := m.command(restart, "restart", "--time", "30", container); err != nil {
		return false, err
	}
	if err := m.ready(restart, runtime.Address, auth); err != nil {
		return false, err
	}
	m.mu.Lock()
	entry.restored = false
	entry.Runtime.PendingRestart = nil // The cause is spent once the restart applies.
	_ = m.persistLocked()
	m.mu.Unlock()
	return true, nil
}

// restartRequestHTTP marks a managed runtime for a deferred restart. ToolHub
// calls it when a principal's tool projection changes: the managed ToolHub
// container cannot write the runtime's state dir itself, so it asks the
// supervisor, which owns the runtime entry and its auth. Identity fields are a
// lookup key, not an authorization grant — they can only mark that principal's
// own runtime. A busy runtime keeps the marker for the post-delivery restart
// poll; an idle one is signaled immediately; a stopped runtime needs nothing.
func (m *Manager) restartRequestHTTP(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeJSON(w, http.StatusMethodNotAllowed, map[string]string{"error": "method not allowed"})
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, 64*1024)
	defer r.Body.Close()
	var request struct {
		PrincipalID string `json:"principal_id"`
		ContextID   string `json:"context_id"`
		RuntimeID   string `json:"runtime_id"`
		Revision    uint64 `json:"revision"`
		OperationID string `json:"operation_id"`
		Reason      string `json:"reason"`
	}
	if err := json.NewDecoder(r.Body).Decode(&request); err != nil || !validID(request.PrincipalID) || !validID(request.ContextID) || (request.RuntimeID != "" && !validID(request.RuntimeID)) ||
		(request.Revision != 0 || request.OperationID != "" || request.Reason != "") && (request.Revision == 0 || !validID(request.OperationID) || request.Reason != "tool_projection_change" || request.RuntimeID == "") {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid restart request"})
		return
	}
	m.mu.Lock()
	var entry *runtimeEntry
	for _, candidate := range m.items {
		if candidate.Runtime.PrincipalID == request.PrincipalID && candidate.Runtime.ContextID == request.ContextID && (request.RuntimeID == "" || candidate.Runtime.RuntimeID == request.RuntimeID) {
			entry = candidate
			break
		}
	}
	running := entry != nil && entry.Container != "" && (entry.State == Ready || entry.State == Busy || entry.State == Idle)
	m.mu.Unlock()
	if !running {
		writeJSON(w, http.StatusOK, map[string]bool{"scheduled": false})
		return
	}
	// The decide round-trip must run unlocked: a slow PDP cannot stall every
	// runtime transition behind m.mu.
	if !m.rightsGate(w, r, request.PrincipalID, "agents", "restart_self", "runtime") {
		return
	}
	m.mu.Lock()
	// The runtime may have stopped during the decide — re-check before
	// trusting the snapshot.
	if entry == nil || entry.Container == "" || (entry.State != Ready && entry.State != Busy && entry.State != Idle) {
		m.mu.Unlock()
		writeJSON(w, http.StatusOK, map[string]bool{"scheduled": false})
		return
	}
	address, auth, idle := entry.Address, entry.auth, entry.State == Idle || entry.State == Ready
	snapshot := entry.Runtime
	m.mu.Unlock()
	var markerBody []byte
	if request.OperationID != "" {
		markerBody, _ = json.Marshal(struct {
			OperationID string `json:"operation_id"`
			Reason      string `json:"reason"`
			Revision    uint64 `json:"revision"`
			Generation  string `json:"generation"`
		}{request.OperationID, request.Reason, request.Revision, snapshot.Generation})
	}
	marker, err := http.NewRequestWithContext(r.Context(), http.MethodPost, address+"/v1/restart-request", bytes.NewReader(markerBody))
	if err != nil {
		writeRuntimeFailure(w, http.StatusBadGateway, err)
		return
	}
	marker.Header.Set("Authorization", "Bearer "+auth)
	if jobID := r.Header.Get("X-Hub-Job-ID"); validID(jobID) {
		marker.Header.Set("X-Hub-Job-ID", jobID)
	}
	response, err := m.cfg.HTTP.Do(marker)
	if err != nil {
		writeRuntimeFailure(w, http.StatusBadGateway, err)
		return
	}
	_, _ = io.Copy(io.Discard, io.LimitReader(response.Body, 64*1024))
	_ = response.Body.Close()
	if response.StatusCode != http.StatusOK {
		writeJSON(w, http.StatusBadGateway, map[string]string{"error": "runtime marker refused"})
		return
	}
	if !idle && request.OperationID != "" {
		m.mu.Lock()
		if current := m.items[runtimeKey(Binding{PrincipalID: snapshot.PrincipalID, ContextID: snapshot.ContextID, RuntimeMode: snapshot.RuntimeMode})]; current != nil && current.Generation == snapshot.Generation {
			current.Runtime.PendingRestart = &RestartCause{Reason: request.Reason, OperationID: request.OperationID, Revision: request.Revision, Generation: snapshot.Generation}
			_ = m.persistLocked()
		}
		m.mu.Unlock()
	}
	applied := false
	if idle {
		lock := m.lockFor(runtimeKey(Binding{PrincipalID: snapshot.PrincipalID, ContextID: snapshot.ContextID, RuntimeMode: snapshot.RuntimeMode}))
		lock.Lock()
		defer lock.Unlock()
		// A notifier whose deadline already fired cannot collect the ack —
		// its restart would be pure convoy: timed-out retries queueing full
		// daemon restarts behind this lock starve Ensure/wake for minutes.
		// The durable marker stays armed for the post-delivery poll or spawn.
		if r.Context().Err() != nil {
			return
		}
		// Acknowledge the durable marker before the notifier's short deadline;
		// keep new acquisitions blocked until the accepted restart is ready.
		body, _ := json.Marshal(map[string]bool{"scheduled": true, "applied": false})
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("Content-Length", strconv.Itoa(len(body)))
		_, _ = w.Write(body)
		if flusher, ok := w.(http.Flusher); ok {
			flusher.Flush()
		}
		ctx, cancel := context.WithTimeout(context.Background(), 4*time.Minute)
		defer cancel()
		_, err = m.applyRuntimeRestartLocked(ctx, snapshot, auth, 0)
		if err != nil {
			log.Printf("supervisor accepted projection restart failed: %v", err)
		}
		return
	}
	writeJSON(w, http.StatusOK, map[string]bool{"scheduled": true, "applied": applied})
}

// respawnHTTP replaces a managed runtime's container outright — the recreate
// counterpart to restart-request: a deferred restart remounts files but keeps
// the container's baked env, so env-carried passport config (ports, feature
// flags) only takes effect on a real re-create (SPEC-0092 R4.2, A4).
func (m *Manager) respawnHTTP(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeJSON(w, http.StatusMethodNotAllowed, map[string]string{"error": "method not allowed"})
		return
	}
	defer r.Body.Close()
	var request struct {
		PrincipalID string `json:"principal_id"`
		ContextID   string `json:"context_id"`
		RuntimeID   string `json:"runtime_id"`
	}
	if err := json.NewDecoder(r.Body).Decode(&request); err != nil || !validID(request.PrincipalID) || !validID(request.ContextID) || (request.RuntimeID != "" && !validID(request.RuntimeID)) {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid respawn request"})
		return
	}
	if !m.applyRespawnAuthorized(r, request.PrincipalID, request.ContextID, request.RuntimeID) &&
		!m.rightsGate(w, r, request.PrincipalID, "agents", "restart_self", "runtime") {
		return
	}
	applied, err := m.respawnRuntime(request.PrincipalID, request.ContextID, request.RuntimeID)
	if err != nil {
		writeRuntimeFailure(w, http.StatusBadGateway, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]bool{"scheduled": applied, "applied": applied})
}

// ReconcileSettingsRuntime applies the authenticated local publisher's self-owned
// runtime effect while its publication journal still blocks external admission.
func (m *Manager) ReconcileSettingsRuntime(principal string) (bool, error) {
	if !validID(principal) {
		return false, errors.New("invalid publication principal")
	}
	return m.respawnRuntime(principal, principal, "")
}

func (m *Manager) respawnRuntime(principal, contextID, runtimeID string) (bool, error) {
	m.mu.Lock()
	var entry *runtimeEntry
	for _, candidate := range m.items {
		if candidate.Runtime.PrincipalID == principal && candidate.Runtime.ContextID == contextID && (runtimeID == "" || candidate.Runtime.RuntimeID == runtimeID) {
			entry = candidate
			break
		}
	}
	// Only a lease-free idle container may be replaced; a busy or absent one
	// answers scheduled:false — nothing is queued, the next spawn reads the
	// new revision anyway.
	if entry == nil || entry.Container == "" || (entry.State != Ready && entry.State != Idle) || entry.Leases != 0 {
		m.mu.Unlock()
		return false, nil
	}
	snapshot := entry.Runtime
	key := runtimeKey(Binding{PrincipalID: snapshot.PrincipalID, ContextID: snapshot.ContextID, RuntimeMode: snapshot.RuntimeMode})
	m.mu.Unlock()
	// Serialize on the runtime key and re-verify under it — a lease acquired
	// between the lookup and here makes the destroy a mid-lease kill.
	lock := m.lockFor(key)
	lock.Lock()
	m.mu.Lock()
	current := m.items[key]
	valid := current != nil && current.Container == snapshot.Container && current.Generation == snapshot.Generation && current.Leases == 0
	var binding Binding
	if valid {
		binding = current.binding
	}
	m.mu.Unlock()
	if !valid {
		lock.Unlock()
		return false, nil
	}
	settings, err := stack.ReadEffective(binding.ContextRoot, m.environment())
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		lock.Unlock()
		return false, err
	}
	if err == nil {
		binding.PolicyVersion = stack.PolicyVersion(settings)
	}
	// The respawn outlives the client's deadline like an accepted restart.
	ctx, cancel := context.WithTimeout(context.Background(), 4*time.Minute)
	defer cancel()
	_, _ = m.command(ctx, "stop", "--time", "30", snapshot.Container) // a dead container makes rm authoritative anyway
	if err := m.removeOwnedRuntime(ctx, snapshot); err != nil && !errors.Is(err, ErrRuntimeMissing) {
		lock.Unlock()
		return false, err
	}
	m.mu.Lock()
	current.State, current.IdleDeadline = Stopped, time.Time{}
	_ = m.persistLocked()
	m.mu.Unlock()
	lock.Unlock()
	respawned, err := m.Ensure(ctx, binding)
	if err != nil {
		return false, err
	}
	// Like wake: the respawn is a warm-up peek, not a hold — the Ensure
	// reference returns so the fresh runtime settles to idle.
	_ = m.ReleaseBinding(binding)
	return respawned.Container != "", nil
}

// artifactHTTP forwards one bounded artifact fetch to the runtime bound to the
// request envelope. No lease: the gateway asks while the runtime is answering
// the job, so a lookup-only forward never keeps a runtime alive for it.
func (m *Manager) artifactHTTP(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeJSON(w, http.StatusMethodNotAllowed, map[string]string{"error": "method not allowed"})
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, 128*1024)
	defer r.Body.Close()
	body, err := io.ReadAll(r.Body)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "request body too large"})
		return
	}
	var request hubruntime.ArtifactRequest
	if json.Unmarshal(body, &request) != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid request"})
		return
	}
	binding, err := m.bindingFor(request.ExecuteRequest)
	if err != nil {
		writeJSON(w, http.StatusConflict, map[string]string{"error": err.Error()})
		return
	}
	if !m.rightsGate(w, r, binding.PrincipalID, "agents", "monitor_self", "runtime") {
		return
	}
	m.mu.Lock()
	entry := m.items[runtimeKey(binding)]
	running := entry != nil && (entry.State == Ready || entry.State == Busy || entry.State == Idle) && entry.Address != ""
	address := ""
	if running {
		address = entry.Address
	}
	m.mu.Unlock()
	if !running {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "runtime unavailable"})
		return
	}
	upstream, err := http.NewRequestWithContext(r.Context(), http.MethodPost, address+"/v1/artifact", bytes.NewReader(body))
	if err != nil {
		writeRuntimeFailure(w, http.StatusBadGateway, err)
		return
	}
	if request.JobID != "" {
		upstream.Header.Set("X-Hub-Job-ID", request.JobID)
	}
	m.forwardRuntimeRequest(w, r, upstream, binding)
}

// usageHTTP forwards one bounded usage query to the runtime bound to the
// request envelope. Lookup-only like /v1/artifact: no lease is taken.
func (m *Manager) usageHTTP(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeJSON(w, http.StatusMethodNotAllowed, map[string]string{"error": "method not allowed"})
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, 128*1024)
	defer r.Body.Close()
	body, err := io.ReadAll(r.Body)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "request body too large"})
		return
	}
	var request hubruntime.UsageRequest
	if json.Unmarshal(body, &request) != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid request"})
		return
	}
	binding, err := m.bindingFor(request.ExecuteRequest)
	if err != nil {
		writeJSON(w, http.StatusConflict, map[string]string{"error": err.Error()})
		return
	}
	if !m.rightsGate(w, r, binding.PrincipalID, "agents", "monitor_self", "runtime") {
		return
	}
	// Usage is a user command: a stopped runtime is spawned exactly like a
	// message would spawn it, and the lifecycle lease pins it for the query.
	var lease Lease
	var runtime Runtime
	if request.LookupOnly {
		m.mu.Lock()
		entry := m.items[runtimeKey(binding)]
		if entry != nil && (entry.State == Ready || entry.State == Busy || entry.State == Idle) {
			runtime = entry.Runtime
		}
		m.mu.Unlock()
	} else {
		lease, runtime, err = m.Acquire(r.Context(), binding, LeaseLifecycle)
	}
	if err != nil || runtime.Address == "" {
		writeRuntimeFailure(w, http.StatusServiceUnavailable, err)
		return
	}
	if lease.ID != "" {
		defer m.ReleaseLease(lease.ID)
	}
	upstream, err := http.NewRequestWithContext(r.Context(), http.MethodPost, runtime.Address+"/v1/usage", bytes.NewReader(body))
	if err != nil {
		writeRuntimeFailure(w, http.StatusBadGateway, err)
		return
	}
	if request.JobID != "" {
		upstream.Header.Set("X-Hub-Job-ID", request.JobID)
	}
	m.forwardRuntimeRequest(w, r, upstream, binding)
}

// compactHTTP forwards one bounded compaction request to the runtime bound to
// the request envelope. Unlike usage it mutates session state, so it always
// acquires: a stopped runtime spawns exactly like a message would spawn it,
// and the lifecycle lease pins it for the call.
func (m *Manager) compactHTTP(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeJSON(w, http.StatusMethodNotAllowed, map[string]string{"error": "method not allowed"})
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, 128*1024)
	defer r.Body.Close()
	body, err := io.ReadAll(r.Body)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "request body too large"})
		return
	}
	var request hubruntime.CompactRequest
	if json.Unmarshal(body, &request) != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid request"})
		return
	}
	binding, err := m.bindingFor(request.ExecuteRequest)
	if err != nil {
		writeJSON(w, http.StatusConflict, map[string]string{"error": err.Error()})
		return
	}
	if !m.rightsGate(w, r, binding.PrincipalID, "agents", "monitor_self", "runtime") {
		return
	}
	lease, runtime, err := m.Acquire(r.Context(), binding, LeaseLifecycle)
	if err != nil || runtime.Address == "" {
		writeRuntimeFailure(w, http.StatusServiceUnavailable, err)
		return
	}
	defer m.ReleaseLease(lease.ID)
	upstream, err := http.NewRequestWithContext(r.Context(), http.MethodPost, runtime.Address+"/v1/compact", bytes.NewReader(body))
	if err != nil {
		writeRuntimeFailure(w, http.StatusBadGateway, err)
		return
	}
	if request.JobID != "" {
		upstream.Header.Set("X-Hub-Job-ID", request.JobID)
	}
	m.forwardRuntimeRequest(w, r, upstream, binding)
}

// goalHTTP forwards one bounded goal request to the runtime bound to the
// request envelope. Goal verbs mutate session state (set/pause/resume/clear,
// evaluation), so it always acquires — a stopped runtime spawns under a
// lifecycle lease exactly like compact.
func (m *Manager) goalHTTP(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeJSON(w, http.StatusMethodNotAllowed, map[string]string{"error": "method not allowed"})
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, 128*1024)
	defer r.Body.Close()
	body, err := io.ReadAll(r.Body)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "request body too large"})
		return
	}
	var request hubruntime.GoalRequest
	if json.Unmarshal(body, &request) != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid request"})
		return
	}
	binding, err := m.bindingFor(request.ExecuteRequest)
	if err != nil {
		writeJSON(w, http.StatusConflict, map[string]string{"error": err.Error()})
		return
	}
	if !m.rightsGate(w, r, binding.PrincipalID, "agents", "monitor_self", "runtime") {
		return
	}
	lease, runtime, err := m.Acquire(r.Context(), binding, LeaseLifecycle)
	if err != nil || runtime.Address == "" {
		writeRuntimeFailure(w, http.StatusServiceUnavailable, err)
		return
	}
	defer m.ReleaseLease(lease.ID)
	upstream, err := http.NewRequestWithContext(r.Context(), http.MethodPost, runtime.Address+"/v1/goal", bytes.NewReader(body))
	if err != nil {
		writeRuntimeFailure(w, http.StatusBadGateway, err)
		return
	}
	m.forwardRuntimeRequest(w, r, upstream, binding)
}

// forwardRuntimeRequest sends a prepared runtime request with the binding's
// runtime credential and streams the bounded response back.
func (m *Manager) forwardRuntimeRequest(w http.ResponseWriter, r *http.Request, upstream *http.Request, binding Binding) {
	upstream.Header.Set("Content-Type", "application/json")
	upstream.Header.Set("Authorization", "Bearer "+binding.runtimeAuth)
	response, err := m.cfg.HTTP.Do(upstream)
	if err != nil {
		writeRuntimeFailure(w, http.StatusBadGateway, err)
		return
	}
	defer response.Body.Close()
	if contentType := response.Header.Get("Content-Type"); contentType != "" {
		w.Header().Set("Content-Type", contentType)
	}
	w.WriteHeader(response.StatusCode)
	_, _ = io.Copy(w, io.LimitReader(response.Body, 9<<20))
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
