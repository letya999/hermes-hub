package toolhub

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"

	"github.com/letya999/hermes-hub/internal/identity"
)

const ReconnectMarkerName = "toolhub-reconnect.request"

type ProjectionChange struct {
	Revision uint64 `json:"revision"`
	Reason   string `json:"reason"`
}

// ReconnectController turns monotonic projection changes into at-most-once
// callbacks per revision. The callback is the transport-specific reconnect
// hook; this package never rebuilds Hermes homes or owns an MCP session.
type ReconnectController struct {
	Store    *Store
	Auth     identity.Envelope
	OnChange func(ProjectionChange) error
	mu       sync.Mutex
	last     uint64
}

func (c *ReconnectController) Reconcile() (ProjectionChange, bool, error) {
	if c == nil || c.Store == nil || c.OnChange == nil {
		return ProjectionChange{}, false, fmt.Errorf("%w: reconnect controller", ErrInvalid)
	}
	revision, err := c.Store.ProjectionRevision(c.Auth)
	if err != nil {
		return ProjectionChange{}, false, err
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if revision <= c.last {
		return ProjectionChange{}, false, nil
	}
	change := ProjectionChange{Revision: revision, Reason: "effective-binding-changed"}
	if err := c.OnChange(change); err != nil {
		return change, true, err
	}
	c.last = revision
	return change, true, nil
}

// WriteReconnectMarker records a projection change for the runtime watcher.
// The watcher restarts Hermes from its persistent owner home when needed.
func WriteReconnectMarker(stateDir string, change ProjectionChange) error {
	if strings.TrimSpace(stateDir) == "" || change.Revision == 0 {
		return fmt.Errorf("%w: reconnect marker", ErrInvalid)
	}
	if err := noSymlinkPath(stateDir); err != nil {
		return err
	}
	info, err := os.Stat(stateDir)
	if err != nil || !info.IsDir() {
		return fmt.Errorf("%w: reconnect marker directory", ErrInvalid)
	}
	body, err := json.Marshal(change)
	if err != nil {
		return err
	}
	path := filepath.Join(stateDir, ReconnectMarkerName)
	tmp, err := os.CreateTemp(stateDir, ".toolhub-reconnect-*")
	if err != nil {
		return err
	}
	name := tmp.Name()
	defer os.Remove(name)
	if err = tmp.Chmod(0600); err == nil {
		_, err = tmp.Write(body)
	}
	if closeErr := tmp.Close(); err == nil {
		err = closeErr
	}
	if err != nil {
		return err
	}
	return os.Rename(name, path)
}

// ProjectedTool is the server-side mapping exposed to an authenticated Hermes
// runtime. BindingID is metadata for the gateway; it is never accepted from a
// model tool argument.
type ProjectedTool struct {
	Name         string
	BindingID    string
	DefinitionID string
	Version      string
	Tool         ToolSpec
}

// ListProjectedTools and ResolveProjectedTool are the shared authorization
// path for tools/list and tools/call. A disabled or stale binding disappears
// from list and is denied again on call; cached MCP tool visibility is not an
// authorization grant.
func (s *Store) ListProjectedTools(auth identity.Envelope) ([]ProjectedTool, error) {
	if err := s.Reload(); err != nil {
		return nil, err
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	if err := auth.Validate(auth.PrincipalID, auth.ContextID, auth.RuntimeID, auth.PolicyVersion); err != nil {
		return nil, fmt.Errorf("%w: %v", ErrUnauthorized, err)
	}
	result := make([]ProjectedTool, 0)
	seen := map[string]bool{}
	for _, binding := range s.bindings {
		effective, err := s.resolveLocked(auth, binding.ToolBindingID)
		if err != nil {
			if errors.Is(err, ErrUnauthorized) || errors.Is(err, ErrNotFound) || errors.Is(err, ErrStale) || errors.Is(err, ErrRevoked) || errors.Is(err, ErrDegraded) {
				continue
			}
			return nil, err
		}
		for _, tool := range effective.Definition.Tools {
			name := ProjectedToolName(effective.Definition.DefinitionID, effective.Definition.Version, tool.Name)
			if seen[name] {
				return nil, fmt.Errorf("%w: projected tool %s has multiple bindings", ErrConflict, name)
			}
			seen[name] = true
			result = append(result, ProjectedTool{Name: name, BindingID: effective.Binding.ToolBindingID, DefinitionID: effective.Definition.DefinitionID, Version: effective.Definition.Version, Tool: tool})
		}
	}
	slices.SortFunc(result, func(a, b ProjectedTool) int { return strings.Compare(a.Name, b.Name) })
	return result, nil
}

func (s *Store) ResolveProjectedTool(auth identity.Envelope, projectedName string) (ProjectedTool, EffectiveBinding, error) {
	if err := validateProjectedName(projectedName); err != nil {
		return ProjectedTool{}, EffectiveBinding{}, err
	}
	if err := s.Reload(); err != nil {
		return ProjectedTool{}, EffectiveBinding{}, err
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.findProjectedLocked(auth, projectedName)
}

// AuthorizeProjected holds the registry read lock through backend admission for
// a projected tool name. Cached MCP visibility is not an authorization grant.
func (s *Store) AuthorizeProjected(auth identity.Envelope, projectedName string, admit func(ProjectedTool, EffectiveBinding) error) error {
	if admit == nil {
		return fmt.Errorf("%w: nil admission callback", ErrInvalid)
	}
	if err := validateProjectedName(projectedName); err != nil {
		return err
	}
	if err := s.Reload(); err != nil {
		return err
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	projected, effective, err := s.findProjectedLocked(auth, projectedName)
	if err != nil {
		return err
	}
	return admit(projected, effective)
}

func validateProjectedName(projectedName string) error {
	if projectedName == "" || len(projectedName) > 128 || !toolNamePattern.MatchString(projectedName) || strings.ContainsAny(projectedName, "\r\n") {
		return fmt.Errorf("%w: projected tool name", ErrInvalid)
	}
	return nil
}

func (s *Store) findProjectedLocked(auth identity.Envelope, projectedName string) (ProjectedTool, EffectiveBinding, error) {
	if err := auth.Validate(auth.PrincipalID, auth.ContextID, auth.RuntimeID, auth.PolicyVersion); err != nil {
		return ProjectedTool{}, EffectiveBinding{}, fmt.Errorf("%w: %v", ErrUnauthorized, err)
	}
	var found *ProjectedTool
	var effective EffectiveBinding
	for _, binding := range s.bindings {
		candidate, err := s.resolveLocked(auth, binding.ToolBindingID)
		if err != nil {
			continue
		}
		for _, tool := range candidate.Definition.Tools {
			if ProjectedToolName(candidate.Definition.DefinitionID, candidate.Definition.Version, tool.Name) != projectedName {
				continue
			}
			if found != nil {
				return ProjectedTool{}, EffectiveBinding{}, fmt.Errorf("%w: projected tool %s has multiple bindings", ErrConflict, projectedName)
			}
			value := ProjectedTool{Name: projectedName, BindingID: candidate.Binding.ToolBindingID, DefinitionID: candidate.Definition.DefinitionID, Version: candidate.Definition.Version, Tool: tool}
			found = &value
			effective = candidate
		}
	}
	if found == nil {
		return ProjectedTool{}, EffectiveBinding{}, fmt.Errorf("%w: projected tool", ErrNotFound)
	}
	return *found, effective, nil
}
