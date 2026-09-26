package supervisor

import (
	"context"
	"encoding/hex"
	"encoding/json"
	"errors"
	"path/filepath"
	"runtime"
	"strings"
)

type OrphanRuntime struct {
	Container  string `json:"container"`
	ContextKey string `json:"context_key"`
	Generation string `json:"generation"`
}

var ErrRuntimeMissing = errors.New("runtime container missing")

func (m *Manager) containerPresent(ctx context.Context, container string) (bool, error) {
	if strings.ContainsAny(container, "\r\n") || container == "" {
		return false, errors.New("invalid container name")
	}
	out, err := m.command(ctx, "ps", "-a", "--filter", "name=^/"+container+"$", "--format", "{{.Names}}")
	if err != nil || len(out) > 64*1024 {
		return false, errors.New("container presence unavailable")
	}
	name := strings.TrimSpace(string(out))
	if name == "" {
		return false, nil
	}
	if name != container {
		return false, errors.New("ambiguous container presence")
	}
	return true, nil
}

func (m *Manager) ownerID() string { return hex.EncodeToString(hashBytes(m.statePath)) }

func (m *Manager) runtimeLabels(ctx context.Context, container string) (map[string]string, error) {
	labels, err := m.command(ctx, "inspect", "--format", "{{json .Config.Labels}}", container)
	var metadata map[string]string
	if err != nil || len(labels) > 64*1024 || json.Unmarshal(labels, &metadata) != nil {
		return nil, errors.New("runtime ownership unavailable")
	}
	return metadata, nil
}

func (m *Manager) VerifyOwnership(ctx context.Context, runtime Runtime) error {
	metadata, err := m.runtimeLabels(ctx, runtime.Container)
	if err != nil {
		if present, presenceErr := m.containerPresent(ctx, runtime.Container); presenceErr == nil && !present {
			return ErrRuntimeMissing
		}
		return err
	}
	key := runtime.PrincipalID + "\x00" + runtime.ContextID + "\x00" + runtime.RuntimeMode
	if metadata["hermes-hub.owner"] != m.ownerID() || metadata["hermes-hub.context"] != hex.EncodeToString(hashBytes(key)) || metadata["hermes-hub.generation"] != runtime.Generation || runtime.Generation == "" {
		return errors.New("runtime ownership mismatch")
	}
	if err := m.verifyScopeMount(ctx, runtime); err != nil {
		return err
	}
	return nil
}

func (m *Manager) verifyScopeMount(ctx context.Context, item Runtime) error {
	out, err := m.command(ctx, "inspect", "--format", "{{json .Mounts}}", item.Container)
	var mounts []struct{ Type, Source, Destination string }
	if err != nil || len(out) > 64*1024 || json.Unmarshal(out, &mounts) != nil || len(mounts) > 64 {
		return errors.New("runtime mounts unavailable")
	}
	expected := filepath.Join(m.cfg.SpacesRoot, item.ContextID)
	for _, mount := range mounts {
		if mount.Destination != "/scope" {
			continue
		}
		if mount.Type != "bind" || !sameHostMountPath(mount.Source, expected) {
			return errors.New("runtime scope mount mismatch")
		}
		return nil
	}
	return errors.New("runtime scope mount missing")
}

func sameHostMountPath(source, expected string) bool {
	source, expected = filepath.Clean(source), filepath.Clean(expected)
	if source == expected {
		return true
	}
	if runtime.GOOS != "windows" {
		return false
	}
	return sameWindowsDockerSource(source, expected)
}

func sameWindowsDockerSource(source, expected string) bool {
	if strings.EqualFold(source, expected) {
		return true
	}
	translated, ok := dockerDesktopWindowsSource(source)
	return ok && strings.EqualFold(filepath.Clean(translated), expected)
}

func dockerDesktopWindowsSource(source string) (string, bool) {
	const prefix = "/run/desktop/mnt/host/"
	normalized := strings.ReplaceAll(source, `\`, "/")
	if !strings.HasPrefix(strings.ToLower(normalized), prefix) {
		return "", false
	}
	rest := normalized[len(prefix):]
	if len(rest) < 3 || rest[1] != '/' || !((rest[0] >= 'a' && rest[0] <= 'z') || (rest[0] >= 'A' && rest[0] <= 'Z')) {
		return "", false
	}
	return strings.ToUpper(rest[:1]) + `:\` + strings.ReplaceAll(rest[2:], "/", `\`), true
}

func (m *Manager) ownedContainerID(ctx context.Context, runtime Runtime) (string, error) {
	out, err := m.command(ctx, "inspect", "--format", "{{.Id}}", runtime.Container)
	id := strings.TrimSpace(string(out))
	if err != nil {
		if present, presenceErr := m.containerPresent(ctx, runtime.Container); presenceErr == nil && !present {
			return "", ErrRuntimeMissing
		}
	}
	decoded, decodeErr := hex.DecodeString(id)
	if err != nil || decodeErr != nil || len(decoded) != 32 {
		return "", errors.New("container identity unavailable")
	}
	// Verify labels by immutable ID, so a same-name replacement cannot be removed.
	runtime.Container = id
	if err := m.VerifyOwnership(ctx, runtime); err != nil {
		return "", err
	}
	return id, nil
}

func (m *Manager) removeOwnedRuntime(ctx context.Context, runtime Runtime) error {
	id, err := m.ownedContainerID(ctx, runtime)
	if err != nil {
		return err
	}
	if _, err = m.command(ctx, "rm", "-f", id); err != nil {
		return errors.New("owned runtime removal failed")
	}
	return nil
}

// DetectOrphans inventories only this supervisor's labelled containers.
// Detection never deletes a home, volume, job or an unrecognized container.
func (m *Manager) DetectOrphans(ctx context.Context) error {
	out, err := m.command(ctx, "ps", "-a", "--filter", "label=hermes-hub.owner="+m.ownerID(), "--format", "{{.ID}}")
	if err != nil {
		return errors.New("owned runtime inventory unavailable")
	}
	if len(out) > 64*1024 {
		return errors.New("runtime inventory limit exceeded")
	}
	var orphans []OrphanRuntime
	ids := strings.Fields(string(out))
	if len(ids) > 256 {
		return errors.New("runtime inventory count exceeded")
	}
	for _, id := range ids {
		if len(id) > 64 {
			return errors.New("invalid container identity")
		}
		for _, c := range id {
			if !strings.ContainsRune("0123456789abcdef", c) {
				return errors.New("invalid container identity")
			}
		}
		metadata, err := m.runtimeLabels(ctx, id)
		if err != nil {
			return err
		}
		if metadata["hermes-hub.owner"] != m.ownerID() {
			continue
		}
		key, generation := metadata["hermes-hub.context"], metadata["hermes-hub.generation"]
		decoded, decodeErr := hex.DecodeString(key)
		if decodeErr != nil || len(decoded) != 32 || generation == "" || len(generation) > 256 {
			return errors.New("invalid runtime ownership")
		}
		name, err := m.command(ctx, "inspect", "--format", "{{.Name}}", id)
		if err != nil || len(name) > 1024 {
			return errors.New("runtime inventory identity unavailable")
		}
		m.mu.Lock()
		current := false
		for logicalKey, entry := range m.items {
			if hex.EncodeToString(hashBytes(logicalKey)) == key && entry.Generation == generation && entry.State != Stopped && strings.TrimPrefix(strings.TrimSpace(string(name)), "/") == entry.Container {
				current = true
				break
			}
		}
		m.mu.Unlock()
		if !current {
			orphans = append(orphans, OrphanRuntime{Container: id, ContextKey: key, Generation: generation})
		}
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	previous := m.orphans
	m.orphans = orphans
	if err := m.persistLocked(); err != nil {
		m.orphans = previous
		return err
	}
	return nil
}

// ReapOrphans removes only generations with a known terminal job ledger.
// An unrecognized generation may still own an external result, so it stays
// visible for inspection and blocks new cold-start capacity.
func (m *Manager) ReapOrphans(ctx context.Context) {
	for _, orphan := range m.Orphans() {
		m.mu.Lock()
		var runtime Runtime
		for key, entry := range m.items {
			if hex.EncodeToString(hashBytes(key)) == orphan.ContextKey {
				runtime = entry.Runtime
				break
			}
		}
		known, uncertain := false, false
		for _, job := range m.jobs {
			if job.Request.ContextID == runtime.ContextID && job.Request.PrincipalID == runtime.PrincipalID && job.Generation == orphan.Generation {
				known = true
				if !terminalRunStatus(job.Status) {
					uncertain = true
				}
			}
		}
		m.mu.Unlock()
		if !known || uncertain || runtime.Generation == orphan.Generation {
			continue
		}
		lock := m.lockFor(runtimeKey(Binding{PrincipalID: runtime.PrincipalID, ContextID: runtime.ContextID, RuntimeMode: runtime.RuntimeMode}))
		lock.Lock()
		runtime.Container, runtime.Generation = orphan.Container, orphan.Generation
		// Compute removal does not include Docker's volume deletion option.
		_ = m.removeOwnedRuntime(ctx, runtime)
		lock.Unlock()
	}
}

func (m *Manager) Orphans() []OrphanRuntime {
	m.mu.Lock()
	defer m.mu.Unlock()
	return append([]OrphanRuntime(nil), m.orphans...)
}
