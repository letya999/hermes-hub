package supervisor

import (
	"context"
	"encoding/hex"
	"errors"
	"fmt"
	"github.com/letya999/hermes-hub/internal/stack"
	"io"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"
)

func (m *Manager) Ensure(ctx context.Context, binding Binding) (Runtime, error) {
	m.mu.Lock()
	held := m.holds[holdKey(binding.PrincipalID, binding.ContextID)].Active
	m.mu.Unlock()
	if held {
		return Runtime{}, errors.New("administrative hold")
	}
	binding, err := m.normalize(binding)
	if err != nil {
		return Runtime{}, err
	}
	key := runtimeKey(binding)
	lock := m.lockFor(key)
	lock.Lock()
	defer lock.Unlock()
	selection, selected, selectionErr := stack.ReadExecution(binding.ContextRoot, m.environment(), binding.UserID)
	if selectionErr != nil {
		return Runtime{}, selectionErr
	}
	if selected && selection.Mode != "supervisor" {
		return Runtime{}, errors.New("context selected the legacy executor")
	}
	managedSettings, managedErr := stack.Read(binding.ContextRoot)
	if managedErr != nil && !os.IsNotExist(managedErr) {
		return Runtime{}, fmt.Errorf("context settings unavailable: %w", managedErr)
	}
	managed := managedErr == nil && managedSettings.CapabilityMode == "managed"
	if managed && (!selected || selection.Mode != "supervisor") {
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
	if current != nil && current.PolicyVersion != "" && binding.PolicyVersion != "" && current.PolicyVersion != binding.PolicyVersion && (current.State == Ready || current.State == Idle || current.State == Busy || current.restored) {
		snapshot := current.Runtime
		m.mu.Unlock()
		log.Printf("runtime %s policy rotated: replacing stale generation", snapshot.Container)
		// A verified-absent container already satisfies the removal goal;
		// only a real removal failure blocks the rotated spawn.
		if err := m.removeOwnedRuntime(ctx, snapshot); err != nil && !errors.Is(err, ErrRuntimeMissing) {
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
	if portErr != nil {
		m.mu.Unlock()
		if !slotHeld {
			m.releaseSlot(key)
		}
		return Runtime{}, portErr
	}
	address := m.runtimeAddress(container, port, managed)
	m.gen++
	logical := &runtimeEntry{Runtime: Runtime{PrincipalID: binding.PrincipalID, ContextID: binding.ContextID, RuntimeID: binding.RuntimeID, RuntimeMode: binding.RuntimeMode, Generation: fmt.Sprintf("gen-%d", m.gen), PolicyVersion: binding.PolicyVersion, Container: container, Address: address, State: Starting, ImageSource: imageSourceOf(binding, m.environment(), m.cfg.Image), Leases: 1, LastUsed: m.cfg.Now(), Managed: managed}, auth: binding.runtimeAuth, leases: map[string]Lease{}}
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
	status := ""
	if out, inspectErr := m.command(ctx, "inspect", "--format", "{{.State.Status}}", container); inspectErr == nil {
		status = strings.TrimSpace(string(out))
	}
	if status == "running" {
		if _, ownershipErr := m.ownedContainerID(ctx, logical.Runtime); ownershipErr != nil {
			// A same-name container from an older generation blocks the respawn;
			// reclaim it only when its labels still prove our ownership.
			if !m.reclaimStaleRuntime(ctx, binding, container, logical.Generation) {
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
		} else {
			// Port allocation may have moved since this container started;
			// reuse requires its actually published port.
			if m.cfg.HostSpacesRoot == "" {
				if hostPort, portErr := m.publishedPort(ctx, logical.Runtime); portErr == nil {
					address = fmt.Sprintf("http://127.0.0.1:%d", hostPort)
				}
			}
			logical.Address = address
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
	} else if status != "" {
		// A dead or paused same-name container would make docker run fail on
		// the name; remove it only when it is verifiably ours and stale.
		m.reclaimStaleRuntime(ctx, binding, container, logical.Generation)
	}
	args, err := m.runArgsWithGeneration(binding, container, port, logical.Generation)
	if err != nil {
		m.markDegraded(key)
		return Runtime{}, fmt.Errorf("prepare runtime launch: %w", err)
	}
	if managed {
		if err := m.ensureManagedInfra(ctx, binding, managedSettings, container, port, logical.Generation); err != nil {
			cleanupErr := m.removeOwnedRuntime(ctx, logical.Runtime)
			m.markDegraded(key)
			if cleanupErr == nil || errors.Is(cleanupErr, ErrRuntimeMissing) {
				m.releaseSlot(key)
			}
			return Runtime{}, fmt.Errorf("managed topology: %w", err)
		}
	}
	if out, runErr := m.command(ctx, args...); runErr != nil {
		m.markDegraded(key)
		// Docker may have created the container before its acknowledgement was lost.
		cleanupErr := m.removeOwnedRuntime(ctx, logical.Runtime)
		if cleanupErr == nil || errors.Is(cleanupErr, ErrRuntimeMissing) {
			m.releaseSlot(key)
		}
		return Runtime{}, fmt.Errorf("start runtime: %w: %s", runErr, firstLine(string(out)))
	}
	if managedSettings.Has("browser") || managedSettings.Has("meet") {
		// SPEC-0083 R2: the sidecar sleeps between uses; `docker start` is
		// idempotent — running is a no-op, a slept one resumes before the
		// runtime probes readiness. A failed start is the same severity class
		// as a missing space network: fail the spawn and reclaim.
		if err := m.ensureBrowserSidecar(ctx, binding.UserID); err != nil {
			cleanupErr := m.removeOwnedRuntime(ctx, logical.Runtime)
			m.markDegraded(key)
			if cleanupErr == nil || errors.Is(cleanupErr, ErrRuntimeMissing) {
				m.releaseSlot(key)
			}
			return Runtime{}, err
		}
	}
	if !managed && (managedSettings.Has("browser") || managedSettings.Has("meet")) {
		// The browser sidecar answers as browser-<user> on the space's compose
		// default network; spawned runtimes join the shared net only, so
		// dual-home them here. Managed runtimes already sit on the agent net
		// the sidecar aliases into.
		spaceNet := "hermes-hub-" + binding.UserID + "-" + m.environment() + "_default"
		if out, connectErr := m.command(ctx, "network", "connect", spaceNet, container); connectErr != nil {
			cleanupErr := m.removeOwnedRuntime(ctx, logical.Runtime)
			m.markDegraded(key)
			if cleanupErr == nil || errors.Is(cleanupErr, ErrRuntimeMissing) {
				m.releaseSlot(key)
			}
			return Runtime{}, fmt.Errorf("attach browser network %s (run hubctl up for this space): %w: %s", spaceNet, connectErr, firstLine(string(out)))
		}
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
	settings, settingsErr := stack.ReadEnvironment(abs, m.environment())
	if settingsErr != nil && !os.IsNotExist(settingsErr) {
		return Binding{}, fmt.Errorf("context settings unavailable: %w", settingsErr)
	}
	managed := settingsErr == nil && settings.CapabilityMode == "managed"
	runtimeEnv := "runtime." + m.environment() + ".env"
	if managed {
		runtimeEnv = "managed-runtime." + m.environment() + ".env"
	}
	if info, err := os.Lstat(filepath.Join(abs, runtimeEnv)); err != nil || !info.Mode().IsRegular() || info.Mode()&os.ModeSymlink != 0 {
		return Binding{}, errors.New("runtime env file is unavailable")
	}
	for _, file := range []string{"hermes." + m.environment() + ".yaml", "SOUL.md"} {
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
	// home. Managed contexts keep a separate private layout: no shared state,
	// workspace or connection directories ever enter the spawned runtime.
	dirs := []string{"runtime", "hermes", "connections", "connections/google", "connections/telegram", "connections/browser", "home", "cache", "workspace", "archive"}
	if managed {
		dirs = []string{"managed/" + m.environment() + "/runtime", "managed/" + m.environment() + "/hermes", "managed/" + m.environment() + "/home", "managed/" + m.environment() + "/cache", "managed/" + m.environment() + "/workspace"}
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

func (m *Manager) runArgs(binding Binding, container string, port int) ([]string, error) {
	return m.runArgsWithGeneration(binding, container, port, "")
}

func (m *Manager) runArgsWithGeneration(binding Binding, container string, port int, generation string) ([]string, error) {
	env := m.environment()
	// Environment-aware: the managed env contract (HUB_CAPABILITY_ENVIRONMENT,
	// dev port offsets, execution selection) must match the rendered compose.
	settings, settingsErr := stack.ReadEnvironment(binding.ContextRoot, env)
	if settingsErr != nil && !os.IsNotExist(settingsErr) {
		return nil, settingsErr
	}
	if settingsErr == nil && settings.CapabilityMode == "managed" {
		return m.managedRunArgs(binding, settings, container, generation)
	}
	m.prepareSpawnFiles(binding, env)
	project := "hermes-hub-" + binding.UserID + "-" + env
	// Spawned runtimes join the single shared runtime network where the shared
	// control plane (toolhub, credential-broker, cliproxy, communication-hub)
	// resolves; per-user isolation lives in mounts and principal-scoped auth.
	args := []string{"run", "-d", "--name", container, "--network", m.cfg.Network, "--restart=no", "--log-driver", "local", "--log-opt", "max-size=10m", "--log-opt", "max-file=3", "--read-only", "--init", "--user", "10001:10001", "--cap-drop", "ALL", "--security-opt", "no-new-privileges:true", "--pids-limit", strconv.Itoa(m.cfg.PIDs), "--memory", m.cfg.Memory, "--cpus", m.cfg.CPU, "-p", fmt.Sprintf("127.0.0.1:%d:%d", port, m.cfg.RuntimePort)}
	if info, err := os.Lstat(filepath.Join(binding.ContextRoot, "runtime."+env+".env")); err == nil && info.Mode().IsRegular() {
		args = append(args, "--env-file", filepath.Join(binding.ContextRoot, "runtime."+env+".env"))
	}
	args = append(args, "--env-file", binding.EnvFile)
	args = append(args, "--mount", "type=volume,src="+project+"_broker-secrets-runtime,dst=/run/broker-secrets,readonly")
	args = append(args, "--mount", "type=bind,src="+filepath.Join(binding.ContextRoot, "workspace")+",dst=/scope,readonly")
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
	for _, mount := range []struct{ source, target string }{{"runtime", "/state"}, {"hermes", "/state/hermes"}, {"connections/google", "/state/google"}, {"connections/telegram", "/state/telegram"}, {"home", "/state/home"}, {"cache", "/state/cache"}, {"workspace", "/workspace"}, {"archive", "/archive"}} {
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
	// The mount is the per-space filtered copy materialized at spawn so
	// feature gating applies even when Render was not rerun after a toggle.
	spawnSettings, serr := stack.ReadEnvironment(binding.ContextRoot, env)
	if serr != nil {
		spawnSettings = stack.Settings{}
	}
	if strings.TrimSpace(spawnSettings.GlobalSkillsDir) == "" {
		spawnSettings.DefaultGlobalSkillsDir(filepath.Join(filepath.Dir(filepath.Dir(binding.ContextRoot)), "config", "skills"))
		// Same contract as RenderEnvironment: a defaulted platform catalog is
		// created empty so its absence never breaks the spawn.
		if err := os.MkdirAll(spawnSettings.GlobalSkillsDir, 0755); err != nil {
			return nil, fmt.Errorf("platform skills dir: %w", err)
		}
	}
	if _, err := stack.MaterializeEffectiveSkills(binding.ContextRoot, spawnSettings); err != nil {
		return nil, fmt.Errorf("effective skills view: %w", err)
	}
	args = append(args, "--mount", "type=bind,src="+stack.GeneratedEffectiveSkillsDir(binding.ContextRoot)+",dst=/skills,readonly")
	args = append(args, "--tmpfs", "/state/hermes/skills:ro,mode=0555")
	// R5.1: the owner store is writable beside the read-only effective view;
	// the env contract tells the executor which path is which. skill_manage
	// writes drafts here — never into /skills.
	args = append(args, "--mount", "type=bind,src="+filepath.Join(binding.ContextRoot, "skills")+",dst=/skills-store")
	args = append(args, "-e", "HUB_SKILLS_STORE=/skills-store", "-e", "HUB_SKILLS_EFFECTIVE=/skills")
	// R5.2: org-bound spawns also mount the writable org store for org drafts;
	// HUB_SKILLS_ORG reaches the env through the rendered service contract.
	if spawnSettings.OrgScoped() && spawnSettings.OrganizationSkillsDir != "" {
		args = append(args, "--mount", "type=bind,src="+spawnSettings.OrganizationSkillsDir+",dst=/skills-store-org")
	}
	// Same contract for hub plugins: materialize the feature-filtered copy at
	// spawn and mount each plugin read-only into upstream's bundled plugins
	// dir, where kind=backend manifests auto-load. A disabled feature leaves
	// the dir unmounted, so the plugin is never discovered.
	if err := stack.MaterializeHubPlugins(binding.ContextRoot, filepath.Dir(filepath.Dir(binding.ContextRoot)), spawnSettings); err != nil {
		log.Printf("supervisor plugins materialization skipped: %v", err)
	}
	if entries, err := os.ReadDir(stack.GeneratedPluginsDir(binding.ContextRoot)); err == nil {
		for _, entry := range entries {
			if !entry.IsDir() {
				continue
			}
			src := filepath.Join(stack.GeneratedPluginsDir(binding.ContextRoot), entry.Name())
			args = append(args, "--mount", "type=bind,src="+src+",dst=/opt/hermes/plugins/"+entry.Name()+",readonly")
		}
	}
	// Materialize the effective Hermes config host-side and mount it read-only
	// over the agent-writable state dir: mcp_servers must come from ToolHub
	// onboarding, never from terminal edits inside the runtime.
	effectiveConfig, err := m.materializeHermesConfig(binding, env)
	if err != nil {
		return nil, err
	}
	args = append(args, "--mount", "type=bind,src="+effectiveConfig+",dst=/state/hermes/config.yaml,readonly")
	// The executor's compiled governance extract: same contract the rendered
	// compose mounts read-only at /config/tool-policy.json (HUB_TOOL_POLICY).
	// Written at spawn so it tracks the space's current settings.
	if settings.Environment == "" {
		settings.Environment = env
	}
	policyPath, err := stack.MaterializeToolPolicySnapshot(binding.ContextRoot, settings)
	if err != nil {
		return nil, fmt.Errorf("materialize tool policy: %w", err)
	}
	args = append(args, "--mount", "type=bind,src="+policyPath+",dst=/config/tool-policy.json,readonly")
	// The render-time tool-surface expectation, mounted read-only like the
	// policy snapshot; the runtime's /v1/health probe reads it and nothing
	// inside the container can rewrite it.
	if manifest := filepath.Join(binding.ContextRoot, "generated", "tool-manifest."+env+".json"); fileExists(manifest) {
		args = append(args, "--mount", "type=bind,src="+manifest+",dst=/config/tool-manifest.json,readonly")
		args = append(args, "-e", "HUB_TOOL_MANIFEST=/config/tool-manifest.json")
	}
	// The rendered effective SOUL — persona plus channel contract — is what
	// Hermes reads at $HERMES_HOME/SOUL.md. Without it (pre-R1.8 space) the
	// heal path copies the raw persona as before.
	if soul := filepath.Join(binding.ContextRoot, "generated", "soul-effective."+env+".md"); fileExists(soul) {
		args = append(args, "--mount", "type=bind,src="+soul+",dst=/state/hermes/SOUL.md,readonly")
	}
	args = append(args, "--label", "hermes-hub.owner="+m.ownerID(), "--label", "hermes-hub.context="+hex.EncodeToString(hashBytes(runtimeKey(binding))), "--label", "hermes-hub.generation="+generation)
	if binding.OrganizationRoot != "" {
		args = append(args, "--mount", "type=bind,src="+binding.OrganizationRoot+",dst=/org,readonly")
	}
	for _, file := range []struct{ source, target string }{{filepath.Join(binding.ContextRoot, "hermes."+env+".yaml"), "/config/config.yaml"}, {filepath.Join(binding.ContextRoot, "SOUL.md"), "/config/SOUL.md"}} {
		if _, err := os.Stat(file.source); err == nil {
			args = append(args, "--mount", "type=bind,src="+file.source+",dst="+file.target+",readonly")
		}
	}
	args = append(args, "--add-host", "host.docker.internal:host-gateway", "--tmpfs", "/tmp:uid=10001,gid=10001,mode=1777")
	args = append(args, "-e", "HUB_RUNTIME_LISTEN=0.0.0.0:"+strconv.Itoa(m.cfg.RuntimePort), "-e", "HUB_STATE=/state", "-e", "HUB_WORKSPACE=/workspace", "-e", "HERMES_HOME=/state/hermes", "-e", "HOME=/state/home", "-e", "HUB_USER_ID="+binding.UserID, "-e", "HUB_ORGANIZATION_ID="+binding.OrganizationID, "-e", "HUB_RUNTIME_ID="+binding.RuntimeID, "-e", "HUB_POLICY_VERSION="+binding.PolicyVersion, "-e", "API_SERVER_ENABLED=true", "-e", "API_SERVER_HOST=127.0.0.1", "-e", "API_SERVER_PORT=8642")
	if generation != "" {
		args = append(args, "-e", "HUB_RUNTIME_GENERATION="+generation)
	}
	if value := strings.TrimSpace(os.Getenv("HUB_RUN_STALL_TIMEOUT")); value != "" {
		args = append(args, "-e", "HUB_RUN_STALL_TIMEOUT="+value)
	}
	image, _, err := resolveRuntimeImage(binding.ContextRoot, binding.UserID, binding.ContextID, env, m.cfg.Image)
	if err != nil {
		return nil, err
	}
	args = append(args, image, "serve")
	return args, nil
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
// executable import path under HERMES_HOME.
var managedExtensionRootTmpfs = []string{"skills", "hooks", "plugins", "skill-bundles", "scripts", "bin", "node", "lsp"}

// managedRunArgs mirrors the managed Compose service: private managed state
// layout, read-only effective config, scratch workspace, sealed extension
// roots and the agent network only. No published port (internal network), no
// broker secrets, no shared mounts, no host gateway.
func (m *Manager) managedRunArgs(binding Binding, settings stack.Settings, container string, generation string) ([]string, error) {
	env := m.environment()
	managedDir := filepath.Join(binding.ContextRoot, "managed", env)
	args := []string{"run", "-d", "--name", container, "--network", managedAgentNetworkName(settings.User, env), "--network-alias", container, "--restart=no", "--log-driver", "local", "--log-opt", "max-size=10m", "--log-opt", "max-file=3", "--read-only", "--init", "--user", "10001:10001", "--cap-drop", "ALL", "--security-opt", "no-new-privileges:true", "--pids-limit", strconv.Itoa(m.cfg.PIDs), "--memory", m.cfg.Memory, "--cpus", m.cfg.CPU}
	args = append(args, "--env-file", filepath.Join(binding.ContextRoot, "managed-runtime."+env+".env"), "--env-file", binding.EnvFile)
	if service := stack.RuntimeService(settings, "", binding.ContextRoot); service != nil {
		if envMap, ok := service["environment"].(stack.M); ok {
			keys := make([]string, 0)
			for key := range envMap {
				keys = append(keys, key)
			}
			sort.Strings(keys)
			for _, key := range keys {
				args = append(args, "-e", key+"="+fmt.Sprint(envMap[key]))
			}
		}
	}
	for _, name := range []struct{ source, target string }{{"runtime", "/state"}, {"hermes", "/state/hermes"}, {"home", "/state/home"}, {"cache", "/state/cache"}, {"workspace", "/workspace"}} {
		args = append(args, "--mount", "type=bind,src="+filepath.Join(managedDir, name.source)+",dst="+name.target)
	}
	// Same skill contract as unmanaged spawns: the hub-built effective view
	// mounts read-only at /skills and a failed build refuses the spawn.
	if strings.TrimSpace(settings.GlobalSkillsDir) == "" {
		settings.DefaultGlobalSkillsDir(filepath.Join(filepath.Dir(filepath.Dir(binding.ContextRoot)), "config", "skills"))
		if err := os.MkdirAll(settings.GlobalSkillsDir, 0755); err != nil {
			return nil, fmt.Errorf("platform skills dir: %w", err)
		}
	}
	if _, err := stack.MaterializeEffectiveSkills(binding.ContextRoot, settings); err != nil {
		return nil, fmt.Errorf("effective skills view: %w", err)
	}
	args = append(args, "--mount", "type=bind,src="+stack.GeneratedEffectiveSkillsDir(binding.ContextRoot)+",dst=/skills,readonly")
	// R5.1: identical to unmanaged — owner store writable at /skills-store.
	args = append(args, "--mount", "type=bind,src="+filepath.Join(managedDir, "skills")+",dst=/skills-store")
	args = append(args, "-e", "HUB_SKILLS_STORE=/skills-store", "-e", "HUB_SKILLS_EFFECTIVE=/skills")
	if settings.OrgScoped() && settings.OrganizationSkillsDir != "" {
		args = append(args, "--mount", "type=bind,src="+settings.OrganizationSkillsDir+",dst=/skills-store-org")
	}
	effectiveConfig, err := m.materializeHermesConfig(binding, env)
	if err != nil {
		return nil, err
	}
	args = append(args, "--mount", "type=bind,src="+effectiveConfig+",dst=/state/hermes/config.yaml,readonly")
	if soul := filepath.Join(binding.ContextRoot, "generated", "soul-effective."+env+".md"); fileExists(soul) {
		args = append(args, "--mount", "type=bind,src="+soul+",dst=/state/hermes/SOUL.md,readonly")
	}
	if settings.Environment == "" {
		settings.Environment = env
	}
	policyPath, err := stack.MaterializeToolPolicySnapshot(binding.ContextRoot, settings)
	if err != nil {
		return nil, fmt.Errorf("materialize tool policy: %w", err)
	}
	args = append(args, "--mount", "type=bind,src="+policyPath+",dst=/config/tool-policy.json,readonly")
	if manifest := filepath.Join(binding.ContextRoot, "generated", "tool-manifest."+env+".json"); fileExists(manifest) {
		args = append(args, "--mount", "type=bind,src="+manifest+",dst=/config/tool-manifest.json,readonly")
		args = append(args, "-e", "HUB_TOOL_MANIFEST=/config/tool-manifest.json")
	}
	args = append(args, "--tmpfs", "/tmp:uid=10001,gid=10001,mode=1777")
	for _, name := range managedExtensionRootTmpfs {
		args = append(args, "--tmpfs", "/state/hermes/"+name+":ro,mode=0555")
	}
	args = append(args, "--label", "hermes-hub.owner="+m.ownerID(), "--label", "hermes-hub.context="+hex.EncodeToString(hashBytes(runtimeKey(binding))), "--label", "hermes-hub.generation="+generation)
	args = append(args, "-e", "HUB_RUNTIME_LISTEN=0.0.0.0:"+strconv.Itoa(m.cfg.RuntimePort), "-e", "HUB_STATE=/state", "-e", "HUB_WORKSPACE=/workspace", "-e", "HERMES_HOME=/state/hermes", "-e", "HOME=/state/home", "-e", "HUB_USER_ID="+binding.UserID, "-e", "HUB_ORGANIZATION_ID="+binding.OrganizationID, "-e", "HUB_RUNTIME_ID="+binding.RuntimeID, "-e", "HUB_POLICY_VERSION="+binding.PolicyVersion, "-e", "API_SERVER_ENABLED=true", "-e", "API_SERVER_HOST=127.0.0.1", "-e", "API_SERVER_PORT=8642")
	if generation != "" {
		args = append(args, "-e", "HUB_RUNTIME_GENERATION="+generation)
	}
	// Operator-tunable run watchdog rides into the spawned runtime when set.
	if value := strings.TrimSpace(os.Getenv("HUB_RUN_STALL_TIMEOUT")); value != "" {
		args = append(args, "-e", "HUB_RUN_STALL_TIMEOUT="+value)
	}
	image, _, err := resolveRuntimeImage(binding.ContextRoot, binding.UserID, binding.ContextID, env, m.cfg.Image)
	if err != nil {
		return nil, err
	}
	args = append(args, image, "serve")
	return args, nil
}

func fileExists(path string) bool {
	info, err := os.Lstat(path)
	return err == nil && info.Mode().IsRegular()
}

// ensureManagedInfra verifies the Compose-owned private agent network and
// creates the supervisor's control network plus this runtime's control relay:
// the dual-homed sidecar is the only inbound route to an internal-network
// runtime, publishing only on the host loopback.
func (m *Manager) ensureManagedInfra(ctx context.Context, binding Binding, settings stack.Settings, container string, port int, generation string) error {
	agentNet := managedAgentNetworkName(settings.User, m.environment())
	out, err := m.command(ctx, "network", "inspect", "--format", "{{.Internal}}", agentNet)
	if err != nil {
		return fmt.Errorf("managed agent network %s unavailable; run hubctl up for this space first", agentNet)
	}
	if strings.TrimSpace(string(out)) != "true" {
		return fmt.Errorf("managed agent network %s is not internal", agentNet)
	}
	if _, err := m.command(ctx, "network", "inspect", "--format", "{{.Id}}", controlNetworkName); err != nil {
		if _, cerr := m.command(ctx, "network", "create", controlNetworkName); cerr != nil {
			return fmt.Errorf("managed control network %s unavailable: %w", controlNetworkName, cerr)
		}
	}
	return m.spawnControlRelay(ctx, binding, container, port, generation, agentNet)
}

func (m *Manager) spawnControlRelay(ctx context.Context, binding Binding, container string, port int, generation, agentNet string) error {
	relay := container + "-ctl"
	image, _, err := resolveRuntimeImage(binding.ContextRoot, binding.UserID, binding.ContextID, m.cfg.Environment, m.cfg.Image)
	if err != nil {
		return err
	}
	args := []string{"run", "-d", "--name", relay, "--network", agentNet, "-p", fmt.Sprintf("127.0.0.1:%d:%d", port, controlRelayContainerPort), "--restart=no", "--log-driver", "local", "--log-opt", "max-size=10m", "--log-opt", "max-file=3", "--read-only", "--init", "--user", "10001:10001", "--cap-drop", "ALL", "--security-opt", "no-new-privileges:true", "--pids-limit", strconv.Itoa(m.cfg.PIDs), "--memory", m.cfg.Memory, "--cpus", m.cfg.CPU, "--tmpfs", "/tmp:uid=10001,gid=10001,mode=1777", "-e", "HUB_CONTROL_RELAY_TARGET=http://" + container + ":" + strconv.Itoa(m.cfg.RuntimePort), "--label", "hermes-hub.owner=" + m.ownerID(), "--label", "hermes-hub.context=" + hex.EncodeToString(hashBytes(runtimeKey(binding))), "--label", "hermes-hub.generation=" + generation, "--label", "hermes-hub.role=control-relay", "--entrypoint", "hub-runtime", image, "control-relay"}
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
