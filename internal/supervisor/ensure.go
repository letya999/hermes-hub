package supervisor

import (
	"context"
	"encoding/hex"
	"errors"
	"fmt"
	spawnv1 "github.com/letya999/hermes-hub/contracts/spawn/v1"
	"github.com/letya999/hermes-hub/internal/stack"
	"io"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

// Ensure starts or reuses a runtime and records an anonymous hold that
// ReleaseBinding returns. Every hold is a lease row: the runtime's demand is
// len(leases), persisted in the same write as the state change.
func (m *Manager) Ensure(ctx context.Context, binding Binding) (Runtime, error) {
	return m.ensure(ctx, binding, &Lease{})
}

// ensure is Ensure with the hold row supplied by the caller: Acquire passes
// its named kind so the lease lands in the same persist as the Busy
// transition. A failed ensure removes the row it may have added.
func (m *Manager) ensure(ctx context.Context, binding Binding, hold *Lease) (Runtime, error) {
	runtime, err := m.ensureHold(ctx, binding, hold)
	if err != nil && hold.ID != "" {
		m.mu.Lock()
		for _, entry := range m.items {
			if _, ok := entry.leases[hold.ID]; ok {
				delete(entry.leases, hold.ID)
				if persistErr := m.persistLocked(); persistErr != nil {
					// The row is gone in memory but may still pin the runtime
					// on disk — surface it instead of swallowing the cleanup.
					log.Printf("supervisor ensure cleanup hold_id=%q persistence failed: %v", hold.ID, persistErr)
				}
			}
		}
		m.mu.Unlock()
		hold.ID = ""
	}
	return runtime, err
}

// ensureHoldReason marks an anonymous Ensure reference among lease rows.
const ensureHoldReason = "ensure"

// addHoldLocked inserts the caller's hold row into entry. An anonymous hold
// (no kind) is an Ensure reference; like Acquire's lease it is owned by
// "supervisor" with a claim deadline, so a holder that died is swept instead
// of pinning the runtime. m.mu must be held.
func (m *Manager) addHoldLocked(entry *runtimeEntry, binding Binding, hold *Lease) {
	m.leaseSeq++
	kind, reason := hold.Kind, ""
	if kind == "" {
		kind, reason = LeaseJob, ensureHoldReason
	}
	*hold = Lease{ID: fmt.Sprintf("lease-%x", m.leaseSeq), Kind: kind, PrincipalID: binding.PrincipalID, ContextID: binding.ContextID, RuntimeMode: binding.RuntimeMode, Scope: binding.ScopeID, Generation: entry.Generation, Owner: "supervisor", Reason: reason, ExpiresAt: m.cfg.Now().Add(5 * time.Minute)}
	if entry.leases == nil {
		entry.leases = map[string]Lease{}
	}
	entry.leases[hold.ID] = *hold
}

func (m *Manager) ensureHold(ctx context.Context, binding Binding, hold *Lease) (Runtime, error) {
	m.mu.Lock()
	held := m.holds[holdKey(binding.PrincipalID, binding.ContextID)].Active
	m.mu.Unlock()
	if held {
		return Runtime{}, errors.New("administrative hold")
	}
	binding, err := m.normalize(ctx, binding)
	if err != nil {
		return Runtime{}, err
	}
	key := runtimeKey(binding)
	lock := m.lockFor(key)
	lock.Lock()
	defer lock.Unlock()
	_, identityRevision, err := m.identityTemplate(ctx, binding)
	if err != nil {
		return Runtime{}, err
	}
	binding.IdentityRevision = identityRevision
	selection, selected, selectionErr := stack.ReadExecution(binding.ContextRoot, m.environment(), binding.UserID)
	if selectionErr != nil {
		return Runtime{}, selectionErr
	}
	if selected && selection.Mode != "supervisor" {
		return Runtime{}, errors.New("context selected the legacy executor")
	}
	managedSettings, managedEff, managedErr := stack.ReadEffectiveSpaceBundle(ctx, m.cfg.Spawn, binding.ContextRoot)
	if managedErr != nil && !os.IsNotExist(managedErr) {
		return Runtime{}, fmt.Errorf("context settings unavailable: %w", managedErr)
	}
	managed := managedErr == nil
	// The stack passport is the spawn contract (SPEC-0092 R3.2): a declared
	// supervised hermes-runtime selects the supervisor even when the rollout
	// execution file was never written. A file that picks another executor
	// was already rejected above.
	if managed && selection.Mode != "supervisor" && managedSettings.PassportServices["hermes-runtime"] != "supervised" {
		return Runtime{}, errors.New("managed capability mode requires supervisor execution selection")
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
	if current != nil && ((current.PolicyVersion != "" && binding.PolicyVersion != "" && current.PolicyVersion != binding.PolicyVersion) || current.IdentityRevision != binding.IdentityRevision) && (current.State == Ready || current.State == Idle || current.State == Busy || current.restored) {
		snapshot := current.Runtime
		claimed := current.spawnLease != ""
		m.mu.Unlock()
		log.Printf("runtime %s policy rotated: replacing stale generation", snapshot.Container)
		if claimed {
			// The stale-policy row is spawn's: drain stops it under the
			// backend budget and clears the local hold; the claim below
			// binds a fresh generation for the rotated digest.
			if err := m.drainClaim(ctx, binding); err != nil {
				return Runtime{}, errors.New("stale-policy runtime drain failed")
			}
			m.releaseClaim(ctx, current)
		} else if err := m.removeOwnedRuntime(ctx, snapshot); err != nil && !errors.Is(err, ErrRuntimeMissing) {
			// A verified-absent container already satisfies the removal
			// goal; only a real removal failure blocks the rotated spawn.
			return Runtime{}, errors.New("stale-policy runtime removal failed")
		}
		m.mu.Lock()
		current = m.items[key]
		if current != nil && current.Generation == snapshot.Generation {
			current.State = Stopped
			current.restored = false
			if err := m.persistLocked(); err != nil {
				m.mu.Unlock()
				return Runtime{}, err
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
		address, auth := current.Address, binding.runtimeAuth
		snapshot := current.Runtime
		_, slotHeld := m.slots[key]
		spawnLease := current.spawnLease
		m.mu.Unlock()
		// A spawn-claimed entry restores by re-arming its hold: the row is
		// the source of truth, a live lease means the workload outlived the
		// supervisor restart. Not-found drops the entry to a fresh claim.
		if spawnLease != "" {
			if err := m.renewClaim(ctx, spawnLease, snapshot.ContextID); err != nil {
				if !errors.Is(err, spawnv1.ErrNotFound) {
					return snapshot, err
				}
			} else {
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
				current.State, current.LastUsed, current.IdleDeadline = Busy, m.cfg.Now(), time.Time{}
				m.addHoldLocked(current, binding, hold)
				m.slots[key] = struct{}{}
				result := current.Runtime
				if err := m.persistLocked(); err != nil {
					current.Runtime = previous
					delete(current.leases, hold.ID)
					m.mu.Unlock()
					return snapshot, err
				}
				current.restored, current.auth = false, auth
				m.mu.Unlock()
				return result, nil
			}
			m.mu.Lock()
			current = m.items[key]
			if current != nil {
				previous, previousLeases := current.Runtime, current.leases
				current.State, current.leases = Stopped, map[string]Lease{}
				if err := m.persistLocked(); err != nil {
					current.Runtime, current.leases = previous, previousLeases
					m.mu.Unlock()
					return snapshot, err
				}
				current.restored = false
			}
			m.mu.Unlock()
		} else {
			ownershipErr := m.VerifyOwnership(ctx, snapshot)
			if ownershipErr != nil && !errors.Is(ownershipErr, ErrRuntimeMissing) {
				return snapshot, ownershipErr
			}
			observation, inspectErr := m.observeCompute(ctx, snapshot)
			if inspectErr != nil && !errors.Is(ownershipErr, ErrRuntimeMissing) {
				return snapshot, errors.New("restored runtime health unavailable")
			}
			if inspectErr == nil && observation.State == "running" {
				if m.usingKubernetes() && observation.Address != "" {
					address = observation.Address
				}
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
				current.State, current.LastUsed, current.IdleDeadline = Busy, m.cfg.Now(), time.Time{}
				current.Address = address
				m.addHoldLocked(current, binding, hold)
				m.slots[key] = struct{}{}
				result := current.Runtime
				if err := m.persistLocked(); err != nil {
					current.Runtime = previous
					delete(current.leases, hold.ID)
					m.mu.Unlock()
					return snapshot, err
				}
				current.restored, current.auth = false, auth
				m.mu.Unlock()
				return result, nil
			}
			if !errors.Is(ownershipErr, ErrRuntimeMissing) {
				status := observation.State
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
				current.State, current.leases = Stopped, map[string]Lease{}
				if err := m.persistLocked(); err != nil {
					current.Runtime, current.leases = previous, previousLeases
					m.mu.Unlock()
					return snapshot, err
				}
				current.restored = false
			}
			m.mu.Unlock()
		}
	} else {
		m.mu.Unlock()
	}
	// Re-arm the spawn hold before serving a live entry — a drained or
	// forgotten row means the workload is gone; re-claim below instead.
	m.mu.Lock()
	serving := m.items[key]
	live := serving != nil && (serving.State == Ready || serving.State == Idle || serving.State == Busy)
	var servingLease, servingContext string
	if serving != nil {
		servingLease, servingContext = serving.spawnLease, serving.Runtime.ContextID
	}
	m.mu.Unlock()
	serveLive := false
	if live {
		switch err := m.renewClaim(ctx, servingLease, servingContext); {
		case err == nil:
			serveLive = true
		case errors.Is(err, spawnv1.ErrNotFound):
			m.mu.Lock()
			if cur := m.items[key]; cur == serving {
				cur.State = Stopped
				_ = m.persistLocked()
			}
			m.mu.Unlock()
		default:
			return Runtime{}, err
		}
	}
	m.mu.Lock()
	if current := m.items[key]; serveLive && current != nil && (current.State == Ready || current.State == Idle || current.State == Busy) {
		if m.usingKubernetes() {
			snapshot := current.Runtime
			m.mu.Unlock()
			observed, err := m.observeCompute(ctx, snapshot)
			if err != nil {
				return snapshot, err
			}
			if observed.State != "running" {
				return snapshot, errors.New("runtime Pod is not running")
			}
			m.mu.Lock()
			current = m.items[key]
			current.Address, current.ComputeConditions = observed.Address, observed.Conditions
		}
		previous := current.Runtime
		current.State, current.LastUsed, current.IdleDeadline = Busy, m.cfg.Now(), time.Time{}
		m.addHoldLocked(current, binding, hold)
		result := current.Runtime
		if err := m.persistLocked(); err != nil {
			current.Runtime = previous
			delete(current.leases, hold.ID)
			m.mu.Unlock()
			return previous, err
		}
		m.mu.Unlock()
		return result, nil
	}
	m.mu.Unlock()
	if m.usingKubernetes() {
		// Fence the old process before changing the durable generation used by jobs.
		if err := m.prepareKubernetesReplacement(ctx, key, containerName(key)); err != nil {
			return Runtime{}, err
		}
	}
	m.mu.Lock()
	_, slotHeld := m.slots[key]
	if !slotHeld && len(m.orphans) != 0 {
		m.mu.Unlock()
		return Runtime{}, errors.New("owned orphan runtimes require inspection before new capacity")
	}
	if !slotHeld && m.cfg.MaxPerPrincipal > 0 {
		live := 0
		for k, entry := range m.items {
			if k != key && entry.PrincipalID == binding.PrincipalID && (entry.State == Starting || entry.State == Ready || entry.State == Idle || entry.State == Busy) {
				live++
			}
		}
		if live >= m.cfg.MaxPerPrincipal {
			m.mu.Unlock()
			return Runtime{}, errors.New("per-principal runtime ceiling reached")
		}
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
	m.mu.Lock()
	port, portErr := m.allocatePortLocked(key)
	if m.usingKubernetes() {
		port, portErr = m.cfg.RuntimePort, nil
	}
	if portErr != nil {
		m.mu.Unlock()
		if !slotHeld {
			m.releaseSlot(key)
		}
		return Runtime{}, portErr
	}
	address := m.runtimeAddress(container, port, managed)
	m.gen++
	logical := &runtimeEntry{Runtime: Runtime{IdentityRevision: binding.IdentityRevision, PrincipalID: binding.PrincipalID, ContextID: binding.ContextID, RuntimeID: binding.RuntimeID, RuntimeMode: binding.RuntimeMode, Generation: fmt.Sprintf("gen-%d", m.gen), PolicyVersion: binding.PolicyVersion, Container: container, Address: address, State: Starting, ImageSource: imageSourceOf(binding, m.environment(), m.cfg.Image), LastUsed: m.cfg.Now(), Managed: managed}, auth: binding.runtimeAuth, leases: map[string]Lease{}}
	logical.binding = binding
	for id, lease := range lifecycle {
		lease.Generation = logical.Generation
		logical.leases[id] = lease
	}
	m.addHoldLocked(logical, binding, hold)
	previousEntry := m.items[key]
	if previousEntry != nil {
		// Accepted-restart churn is cumulative context state, not
		// crash-window state — carry it across the rebuild like a respawn.
		logical.Runtime.Restarts = previousEntry.Runtime.Restarts
		if !previousEntry.CrashWindowStart.IsZero() && m.cfg.Now().Sub(previousEntry.CrashWindowStart) < 10*time.Minute {
			logical.CrashCount, logical.CrashWindowStart, logical.NextRetryAt = previousEntry.CrashCount, previousEntry.CrashWindowStart, previousEntry.NextRetryAt
		}
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
	// Workload start diverges by backend: the compile-only kubernetes path
	// spawns through the supervisor's own backend (decision 13 / M40.15);
	// the docker/process managed path claims through spawn-controller
	// (S7c), which binds routing and identity to a generation row, starts
	// or reuses its workload, and answers the endpoint to forward to.
	var computeID, computeGeneration, claimLease string
	var computeConditions []Condition
	if m.usingKubernetes() {
		observed, spawnErr := m.backend.Spawn(ctx, SpawnRequest{Binding: binding, Runtime: logical.Runtime, Settings: managedSettings, Effective: managedEff, Port: port})
		if spawnErr != nil {
			m.markDegraded(key)
			cleanupErr := m.removeOwnedRuntime(ctx, logical.Runtime)
			if cleanupErr == nil || errors.Is(cleanupErr, ErrRuntimeMissing) {
				m.releaseSlot(key)
			}
			return Runtime{}, spawnErr
		}
		if observed.Address != "" {
			address = observed.Address
		}
		computeID, computeConditions = observed.ID, observed.Conditions
	} else {
		claimed, spawnErr := m.claimManaged(ctx, binding)
		if spawnErr != nil {
			m.markDegraded(key)
			m.releaseSlot(key)
			return Runtime{}, spawnErr
		}
		address = claimed.Runtime.Address
		computeID, computeGeneration, claimLease = claimed.Runtime.ID, fmt.Sprintf("gen-%d", claimed.Runtime.Generation), claimed.Lease
		// The compose-owned browser sidecar still answers to demand from
		// this space — spawn owns the workload container, not the sidecar.
		if managedSettings.Has("browser") || managedSettings.Has("meet") {
			if err := m.ensureBrowserSidecar(ctx, binding.UserID); err != nil {
				m.markDegraded(key)
				_ = m.cfg.Spawn.ReleaseLease(ctx, claimLease, spawnv1.LeaseRequest{Org: "local", Owner: binding.ContextID})
				m.releaseSlot(key)
				return Runtime{}, err
			}
		}
	}
	m.mu.Lock()
	logical.Address, logical.ComputeID, logical.ComputeConditions = address, computeID, computeConditions
	if computeGeneration != "" {
		logical.Generation = computeGeneration
	}
	logical.spawnLease = claimLease
	if persistErr := m.persistLocked(); persistErr != nil {
		m.mu.Unlock()
		m.markDegraded(key)
		// A claimed row has no local container to inspect later; drop
		// the lease instead of orphaning it.
		m.releaseClaim(ctx, logical)
		return Runtime{}, persistErr
	}
	m.mu.Unlock()
	if err = m.ready(ctx, address, binding.runtimeAuth); err != nil {
		m.markDegraded(key)
		cleanupErr := m.releaseWorkload(ctx, logical)
		if cleanupErr == nil || errors.Is(cleanupErr, ErrRuntimeMissing) {
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

func (m *Manager) normalize(ctx context.Context, binding Binding) (Binding, error) {
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
	if _, err := stack.ReadEffective(ctx, m.cfg.Spawn, abs, m.environment()); err != nil && !os.IsNotExist(err) {
		return Binding{}, fmt.Errorf("context settings unavailable: %w", err)
	}
	runtimeEnv := "managed-runtime." + m.environment() + ".env"
	if info, err := os.Lstat(filepath.Join(abs, runtimeEnv)); err != nil || !info.Mode().IsRegular() || info.Mode()&os.ModeSymlink != 0 {
		return Binding{}, errors.New("runtime env file is unavailable")
	}
	for _, file := range []string{filepath.Join("generated", "hermes."+m.environment()+".yaml"), "SOUL.md"} {
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
	// Match static Compose data paths without allowing a symlink to another
	// home. Contexts keep a separate private layout: no shared state,
	// workspace or connection directories ever enter the spawned runtime.
	dirs := []string{"managed/" + m.environment() + "/runtime", "managed/" + m.environment() + "/hermes", "managed/" + m.environment() + "/home", "managed/" + m.environment() + "/cache", "managed/" + m.environment() + "/workspace"}
	if binding.RuntimeMode != "gateway" {
		dirs = append(dirs, "managed/"+m.environment()+"/runtime/"+binding.RuntimeMode)
	}
	for _, name := range dirs {
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

// managedAgentNetworkName mirrors the Compose topology: one internal network
// per user/environment where only the reviewed relays and runtime resolve.
func managedAgentNetworkName(user, env string) string {
	return "hermes-hub-agent-" + user + "-" + env
}

// controlNetworkName is the supervisor-owned bridge carrying the per-runtime
// control relay's loopback-published port. It is not Compose-owned so a
// project teardown never strands a live supervised runtime.
const controlNetworkName = "hermes-hub-control"

// managedExtensionRootTmpfs are the empty read-only overlays covering every
// executable import path under HERMES_HOME; the compile-only firecracker and
// kubernetes plans reuse the same contract.
var managedExtensionRootTmpfs = []string{"skills", "hooks", "plugins", "skill-bundles", "scripts", "bin", "node", "lsp"}

func fileExists(path string) bool {
	info, err := os.Lstat(path) // #nosec G703 -- callers pass paths under the validated binding context root
	return err == nil && info.Mode().IsRegular()
}

// ensureManagedInfra verifies the Compose-owned private agent network and
// creates the supervisor's control network plus this runtime's control relay:
// the dual-homed sidecar is the only inbound route to an internal-network
// runtime, publishing only on the host loopback.

func (m *Manager) spawnControlRelay(ctx context.Context, binding Binding, container string, port int, generation, agentNet, target, mountDir string, publish bool) error {
	relay := container + "-ctl"
	image, _, err := resolveRuntimeImage(binding.ContextRoot, binding.UserID, binding.ContextID, m.cfg.Environment, m.cfg.Image)
	if err != nil {
		return err
	}
	args := []string{"run", "-d", "--name", relay, "--network", agentNet, "--restart=no", "--log-driver", "local", "--log-opt", "max-size=10m", "--log-opt", "max-file=3", "--read-only", "--init", "--user", "10001:10001", "--cap-drop", "ALL", "--security-opt", "no-new-privileges:true", "--pids-limit", strconv.Itoa(m.cfg.PIDs), "--memory", m.cfg.Memory, "--cpus", m.cfg.CPU, "--tmpfs", "/tmp:uid=10001,gid=10001,mode=1777", "-e", "HUB_CONTROL_RELAY_TARGET=" + target, "--label", "hermes-hub.owner=" + m.ownerID(), "--label", "hermes-hub.context=" + hex.EncodeToString(hashBytes(runtimeKey(binding))), "--label", "hermes-hub.generation=" + generation, "--label", "hermes-hub.role=control-relay", "--entrypoint", "hub-runtime"}
	if publish {
		args = append(args, "-p", fmt.Sprintf("127.0.0.1:%d:%d", port, controlRelayContainerPort))
	}
	if mountDir != "" {
		args = append(args, "-v", mountDir+":"+mountDir)
	}
	args = append(args, image, "control-relay")
	if out, err := m.command(ctx, args...); err != nil {
		// A stale same-name relay from an older generation blocks the spawn;
		// reclaim it only when its labels still prove our ownership.
		m.reclaimSidecarName(ctx, binding, container)
		if retryOut, retryErr := m.command(ctx, args...); retryErr != nil {
			return fmt.Errorf("start control relay: %w: %s; retry: %s", retryErr, firstLine(string(retryOut)), firstLine(string(out)))
		}
	}
	if out, err := m.command(ctx, "network", "connect", controlNetworkName, relay); err != nil {
		_, _ = m.command(ctx, "rm", "-f", relay)
		return fmt.Errorf("attach control relay: %w: %s", err, firstLine(string(out)))
	}
	return nil
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
	if m.usingKubernetes() {
		return nil, errors.New("docker operation is unavailable on the kubernetes backend")
	}
	callCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	if m.cfg.HostSpacesRoot != "" && len(args) > 0 && args[0] == "run" {
		var err error
		args, err = m.containerDockerArgs(args)
		if err != nil {
			return nil, err
		}
	}
	return m.cfg.Command(callCtx, args...)
}

const controlRelayContainerPort = 8091
