package toolhub

import (
	"errors"
	"fmt"
	"github.com/letya999/hermes-hub/internal/identity"
	"github.com/letya999/hermes-hub/internal/toolhub/api"
	"log/slog"
	"slices"
	"strings"
)

// ListProjectedTools and ResolveProjectedTool are the shared authorization
// path for tools/list and tools/call. A disabled or stale binding disappears
// from list and is denied again on call; cached MCP tool visibility is not an
// authorization grant.
func (s *Store) ListProjectedTools(auth identity.Envelope) ([]api.ProjectedTool, error) {
	if err := s.Reload(); err != nil {
		return nil, err
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	if err := auth.Validate(auth.PrincipalID, auth.ContextID, auth.RuntimeID, auth.PolicyVersion); err != nil {
		return nil, fmt.Errorf("%w: %v", api.ErrUnauthorized, err)
	}
	if auth.CapabilityProfile != "" {
		return s.managedToolsLocked(auth)
	}
	result := make([]api.ProjectedTool, 0)
	seen := map[string]bool{}
	for _, binding := range s.bindings {
		if binding.PrincipalID != auth.PrincipalID || binding.ContextID != auth.ContextID || binding.RuntimeID != auth.RuntimeID {
			continue
		}
		effective, err := s.resolveLocked(auth, binding.ToolBindingID)
		if err != nil {
			if errors.Is(err, api.ErrUnauthorized) || errors.Is(err, api.ErrNotFound) || errors.Is(err, api.ErrStale) || errors.Is(err, api.ErrRevoked) || errors.Is(err, api.ErrDegraded) {
				s.noteExclusion(binding, api.ClassifyProjectionExclusion(binding, err))
				continue
			}
			return nil, err
		}
		for _, tool := range effective.Definition.ProjectedToolSpecs(effective.Binding) {
			if tool.Effect == api.WriteEffect && !s.teamMemberWritesLocked(auth, effective) {
				continue
			}
			name := api.ProjectedToolName(effective.Definition.DefinitionID, effective.Definition.Version, tool.Name)
			if seen[name] {
				return nil, fmt.Errorf("%w: projected tool %s has multiple bindings", api.ErrConflict, name)
			}
			seen[name] = true
			result = append(result, api.ProjectedTool{Name: name, BindingID: effective.Binding.ToolBindingID, DefinitionID: effective.Definition.DefinitionID, Version: effective.Definition.Version, Tool: tool})
		}
	}
	slices.SortFunc(result, func(a, b api.ProjectedTool) int { return strings.Compare(a.Name, b.Name) })
	return result, nil
}

// noteExclusion records which projection the binding failed into and why,
// logging once per (principal, binding, projection revision, reason). Called
// from registry-locked scans; exMu alone guards the set. Values come from
// ValidID-constrained fields or the fixed reason vocabulary, and slog quotes
// attribute values — log injection has no sink here.
func (s *Store) noteExclusion(binding api.ToolBinding, reason string) {
	s.exMu.Lock()
	defer s.exMu.Unlock()
	scope := projectionKey(binding.PrincipalID, binding.ContextID, binding.RuntimeID)
	revision := s.projectionRevisions[scope]
	key := scope + "\x00" + binding.ToolBindingID + "\x00" + reason
	if existing, ok := s.exclusions[key]; ok && existing.ProjectionRevision == revision {
		return
	}
	if _, ok := s.exclusions[key]; !ok {
		s.exclusionOrder = append(s.exclusionOrder, key)
		for len(s.exclusionOrder) > api.ExclusionLogLimit {
			delete(s.exclusions, s.exclusionOrder[0])
			s.exclusionOrder = s.exclusionOrder[1:]
		}
	}
	s.exclusions[key] = api.ProjectionExclusion{
		PrincipalID:        binding.PrincipalID,
		ContextID:          binding.ContextID,
		RuntimeID:          binding.RuntimeID,
		BindingID:          binding.ToolBindingID,
		Reason:             reason,
		ProjectionRevision: revision,
	}
	slog.Info("toolhub projection excluded",
		"principal_id", binding.PrincipalID, "context_id", binding.ContextID,
		"runtime_id", binding.RuntimeID, "binding_id", binding.ToolBindingID,
		"definition_id", binding.DefinitionID, "reason", reason,
		"projection_revision", revision)
}

// ProjectionExclusions reports the bindings excluded from the requesting
// envelope's own projection. It never returns another principal's exclusions.
func (s *Store) ProjectionExclusions(auth identity.Envelope) ([]api.ProjectionExclusion, error) {
	if err := auth.Validate(auth.PrincipalID, auth.ContextID, auth.RuntimeID, auth.PolicyVersion); err != nil {
		return nil, fmt.Errorf("%w: %v", api.ErrUnauthorized, err)
	}
	if err := s.Reload(); err != nil {
		return nil, err
	}
	// Scan on demand so the diagnostics surface reflects the current registry
	// even when no tools/list call preceded it.
	s.mu.RLock()
	for _, binding := range s.bindings {
		if binding.PrincipalID != auth.PrincipalID || binding.ContextID != auth.ContextID || binding.RuntimeID != auth.RuntimeID {
			continue
		}
		if _, err := s.resolveLocked(auth, binding.ToolBindingID); err != nil {
			if errors.Is(err, api.ErrUnauthorized) || errors.Is(err, api.ErrNotFound) || errors.Is(err, api.ErrStale) || errors.Is(err, api.ErrRevoked) || errors.Is(err, api.ErrDegraded) {
				s.noteExclusion(binding, api.ClassifyProjectionExclusion(binding, err))
			}
		}
	}
	s.mu.RUnlock()
	s.exMu.Lock()
	defer s.exMu.Unlock()
	out := make([]api.ProjectionExclusion, 0)
	for _, exclusion := range s.exclusions {
		if exclusion.PrincipalID != auth.PrincipalID || exclusion.ContextID != auth.ContextID || exclusion.RuntimeID != auth.RuntimeID {
			continue
		}
		out = append(out, exclusion)
	}
	slices.SortFunc(out, func(a, b api.ProjectionExclusion) int { return strings.Compare(a.BindingID, b.BindingID) })
	return out, nil
}

func (s *Store) ResolveProjectedTool(auth identity.Envelope, projectedName string) (api.ProjectedTool, api.EffectiveBinding, error) {
	if err := api.ValidateProjectedName(projectedName); err != nil {
		return api.ProjectedTool{}, api.EffectiveBinding{}, err
	}
	if err := s.Reload(); err != nil {
		return api.ProjectedTool{}, api.EffectiveBinding{}, err
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.findProjectedLocked(auth, projectedName)
}

// AuthorizeProjected holds the registry read lock through backend admission for
// a projected tool name. Cached MCP visibility is not an authorization grant.
func (s *Store) AuthorizeProjected(auth identity.Envelope, projectedName string, admit func(api.ProjectedTool, api.EffectiveBinding) error) error {
	return s.AuthorizeProjectedCall(auth, projectedName, nil, admit)
}

// AuthorizeProjectedCall evaluates the reviewed action/resource contract against
// the actual arguments before the same credential/backend admission callback.
func (s *Store) AuthorizeProjectedCall(auth identity.Envelope, projectedName string, arguments map[string]any, admit func(api.ProjectedTool, api.EffectiveBinding) error) error {
	if admit == nil {
		return fmt.Errorf("%w: nil admission callback", api.ErrInvalid)
	}
	if err := api.ValidateProjectedName(projectedName); err != nil {
		return err
	}
	if err := s.Reload(); err != nil {
		return err
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	if auth.CapabilityProfile != "" {
		release, err := s.lockCapabilityAdmission()
		if err != nil {
			return err
		}
		defer release()
		projected, effective, err := s.managedToolLocked(auth, projectedName, arguments, false)
		if err != nil {
			return err
		}
		return admit(projected, effective)
	}
	projected, effective, err := s.findProjectedLocked(auth, projectedName)
	if err != nil {
		return err
	}
	return admit(projected, effective)
}

func (s *Store) findProjectedLocked(auth identity.Envelope, projectedName string) (api.ProjectedTool, api.EffectiveBinding, error) {
	if err := auth.Validate(auth.PrincipalID, auth.ContextID, auth.RuntimeID, auth.PolicyVersion); err != nil {
		return api.ProjectedTool{}, api.EffectiveBinding{}, fmt.Errorf("%w: %v", api.ErrUnauthorized, err)
	}
	if auth.CapabilityProfile != "" {
		return s.managedToolLocked(auth, projectedName, nil, true)
	}
	var found *api.ProjectedTool
	var effective api.EffectiveBinding
	for _, binding := range s.bindings {
		candidate, err := s.resolveLocked(auth, binding.ToolBindingID)
		if err != nil {
			continue
		}
		for _, tool := range candidate.Definition.ProjectedToolSpecs(candidate.Binding) {
			if api.ProjectedToolName(candidate.Definition.DefinitionID, candidate.Definition.Version, tool.Name) != projectedName {
				continue
			}
			if tool.Effect == api.WriteEffect && !s.teamMemberWritesLocked(auth, candidate) {
				continue
			}
			if found != nil {
				return api.ProjectedTool{}, api.EffectiveBinding{}, fmt.Errorf("%w: projected tool %s has multiple bindings", api.ErrConflict, projectedName)
			}
			value := api.ProjectedTool{Name: projectedName, BindingID: candidate.Binding.ToolBindingID, DefinitionID: candidate.Definition.DefinitionID, Version: candidate.Definition.Version, Tool: tool}
			found = &value
			effective = candidate
		}
	}
	if found == nil {
		return api.ProjectedTool{}, api.EffectiveBinding{}, fmt.Errorf("%w: projected tool", api.ErrNotFound)
	}
	return *found, effective, nil
}
