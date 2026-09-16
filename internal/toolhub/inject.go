package toolhub

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"github.com/gofrs/flock"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"github.com/letya999/hermes-hub/internal/credstore"
	"github.com/letya999/hermes-hub/internal/identity"
)

const (
	HeaderJobID = "X-Hub-Job-ID"
	HeaderRunID = "X-Hub-Run-ID"
)

type correlationKey struct{}

type callCorrelation struct {
	JobID       string
	HermesRunID string
	ToolCallID  string
}

func withCallCorrelation(ctx context.Context, corr callCorrelation) context.Context {
	if ctx == nil {
		ctx = context.Background()
	}
	return context.WithValue(ctx, correlationKey{}, corr)
}

func callCorrelationFrom(ctx context.Context) callCorrelation {
	if ctx == nil {
		return callCorrelation{}
	}
	corr, _ := ctx.Value(correlationKey{}).(callCorrelation)
	return corr
}

func headerCorrelation(jobID, runID string) callCorrelation {
	return callCorrelation{JobID: sanitizeCorrelationID(jobID), HermesRunID: sanitizeCorrelationID(runID)}
}

func sanitizeCorrelationID(value string) string {
	value = strings.TrimSpace(value)
	if value == "" || len(value) > 64 || strings.ContainsAny(value, "\r\n") {
		return ""
	}
	return value
}

func newToolCallID() string {
	raw := make([]byte, 16)
	if _, err := rand.Read(raw); err != nil {
		return "call-unavailable"
	}
	return "call-" + hex.EncodeToString(raw)
}

// DecryptAuthorized reads ciphertext for a locator that AuthorizeProjected
// already admitted. It must not call AuthorizeProjected again.
func DecryptAuthorized(backend credstore.Backend, effective EffectiveBinding) (map[string]string, error) {
	if backend == nil {
		return nil, fmt.Errorf("%w: credential store", ErrInvalid)
	}
	if effective.Definition.Workload.Class == Shared {
		for _, input := range effective.Definition.Credentials {
			if !input.PerRequest {
				return nil, fmt.Errorf("%w: static credentials cannot be shared", ErrUnauthorized)
			}
		}
	}
	if effective.Credential == nil {
		return map[string]string{}, nil
	}
	owner := effective.Binding.PrincipalID
	if effective.Connection != nil {
		owner = effective.Connection.Owner.ID
	}
	values, err := backend.Get(effective.Credential.Locator, owner)
	if err != nil {
		return nil, err
	}
	allowed := map[string]bool{}
	for _, key := range effective.Credential.Keys {
		allowed[key] = true
	}
	injected := map[string]string{}
	for key, value := range values {
		if allowed[key] {
			injected[key] = value
		}
	}
	return injected, nil
}

// WriteWorkloadEnvFile writes KEY=value lines at 0600 under the workload directory.
func WriteWorkloadEnvFile(dir string, values map[string]string) error {
	if dir == "" || len(values) == 0 {
		return nil
	}
	lines := make([]string, 0, len(values))
	for key, value := range values {
		lines = append(lines, key+"="+value)
	}
	slices.Sort(lines)
	path := filepath.Join(dir, "credentials.env")
	tmp, err := os.CreateTemp(dir, ".cred-*")
	if err != nil {
		return err
	}
	name := tmp.Name()
	defer os.Remove(name)
	if err = tmp.Chmod(0600); err == nil {
		_, err = tmp.WriteString(strings.Join(lines, "\n"))
	}
	if closeErr := tmp.Close(); err == nil {
		err = closeErr
	}
	if err != nil {
		return err
	}
	return os.Rename(name, path)
}

func injectJobID(ctx context.Context, effective EffectiveBinding) string {
	if effective.Definition.Workload.Class != PerJob {
		return ""
	}
	jobID := callCorrelationFrom(ctx).JobID
	if !identity.ValidID(jobID) {
		return ""
	}
	return jobID
}

func writeAuthorizedFiles(ctx context.Context, root string, effective EffectiveBinding, environment map[string]string) (func() error, error) {
	if len(environment) == 0 {
		return func() error { return nil }, nil
	}
	if strings.TrimSpace(root) == "" {
		return nil, fmt.Errorf("%w: workload root", ErrIsolation)
	}
	workspace, err := OpenWorkloadWorkspace(root, effective, injectJobID(ctx, effective))
	if err != nil {
		return nil, err
	}
	if workspace.Path == "" {
		return nil, fmt.Errorf("%w: workload has no file inject path", ErrIsolation)
	}
	lockPath := filepath.Join(workspace.Path, "inject.lock")
	if info, err := os.Lstat(lockPath); err == nil && info.Mode()&os.ModeSymlink != 0 {
		return nil, ErrIsolation
	} else if err != nil && !os.IsNotExist(err) {
		return nil, err
	}
	lock := flock.New(lockPath)
	locked, err := lock.TryLockContext(ctx, 100*time.Millisecond)
	if err != nil || !locked {
		return nil, ErrIsolation
	}
	if err := WriteWorkloadEnvFile(workspace.Path, environment); err != nil {
		_ = lock.Unlock()
		if workspace.Cleanup != nil {
			_ = workspace.Cleanup()
		}
		return nil, err
	}
	return func() error {
		defer lock.Unlock()
		_ = os.Remove(filepath.Join(workspace.Path, "credentials.env"))
		if workspace.Cleanup != nil {
			return workspace.Cleanup()
		}
		return nil
	}, nil
}

// ConnectionsForSecret returns active connections owned by owner that already
// point at locator or declare any of names. Callers must not hold Store.mu.
func (s *Store) ConnectionsForSecret(locator, owner string, names []string) []CredentialReference {
	if s == nil {
		return nil
	}
	named := map[string]bool{}
	for _, name := range names {
		named[name] = true
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	seen := map[string]bool{}
	out := make([]CredentialReference, 0)
	for _, cred := range s.credentials {
		if cred.Status != ActiveStatus || seen[cred.ConnectionID] {
			continue
		}
		connection, ok := s.connections[cred.ConnectionID]
		if !ok || connection.Owner.ID != owner {
			continue
		}
		match := locator != "" && cred.Locator == locator
		if !match {
			for _, key := range cred.Keys {
				if named[key] {
					match = true
					break
				}
			}
		}
		if !match {
			continue
		}
		seen[cred.ConnectionID] = true
		copyKeys := append([]string(nil), cred.Keys...)
		out = append(out, CredentialReference{CredentialRefID: cred.CredentialRefID, ConnectionID: cred.ConnectionID, Locator: cred.Locator, Keys: copyKeys, Revision: cred.Revision, Backend: cred.Backend, Status: cred.Status})
	}
	return out
}
