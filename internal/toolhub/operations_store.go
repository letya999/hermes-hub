package toolhub

import (
	"fmt"
	"slices"
	"strings"

	"github.com/letya999/hermes-hub/internal/identity"
	"github.com/letya999/hermes-hub/internal/toolhub/api"
)

// PutOperation validates and durably stores one journal record.
func (s *Store) PutOperation(op api.Operation) error {
	if err := op.Validate(); err != nil {
		return err
	}
	s.mu.Lock()
	s.operations[op.OperationID] = op
	s.mu.Unlock()
	return s.persist()
}

// OperationFor returns one operation for its exact owner only.
func (s *Store) OperationFor(auth identity.Envelope, id string) (api.Operation, error) {
	if err := auth.Validate(auth.PrincipalID, auth.ContextID, auth.RuntimeID, auth.PolicyVersion); err != nil {
		return api.Operation{}, fmt.Errorf("%w: %v", api.ErrUnauthorized, err)
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	op, ok := s.operations[id]
	if !ok {
		return api.Operation{}, fmt.Errorf("%w: operation", api.ErrNotFound)
	}
	if op.PrincipalID != auth.PrincipalID || op.ContextID != auth.ContextID || op.RuntimeID != auth.RuntimeID || op.PolicyVersion != auth.PolicyVersion {
		return api.Operation{}, fmt.Errorf("%w: operation owner", api.ErrUnauthorized)
	}
	return op, nil
}

// OperationForRequestKey resolves the live operation for one
// (principal, intent, normalized target) tuple. Superseded records are
// never candidates — a retry opens a fresh operation.
func (s *Store) OperationForRequestKey(auth identity.Envelope, intent, target string) (api.Operation, bool) {
	key := api.OperationRequestKey(auth, intent, target)
	s.mu.RLock()
	defer s.mu.RUnlock()
	for _, op := range s.operations {
		if op.RequestKey != key || op.PrincipalID != auth.PrincipalID || op.SupersededBy != "" {
			continue
		}
		return op, true
	}
	return api.Operation{}, false
}

// ConnectorTarget is the resolved existing owned connector a lifecycle verb
// acts on.
type ConnectorTarget struct {
	OnboardingID string `json:"onboarding_id"`
	DefinitionID string `json:"definition_id,omitempty"`
	ConnectionID string `json:"connection_id,omitempty"`
	SourceURL    string `json:"source_url,omitempty"`
	Kind         string `json:"kind,omitempty"`
}

// ResolveConnectorTarget deterministically resolves a user-facing target
// string to one owned live connector. Precedence: exact id → registry alias
// → source URL → repository basename → registry name. Removed records and
// other principals' connectors are never candidates.
func (s *Store) ResolveConnectorTarget(auth identity.Envelope, target string) (ConnectorTarget, error) {
	if err := auth.Validate(auth.PrincipalID, auth.ContextID, auth.RuntimeID, auth.PolicyVersion); err != nil {
		return ConnectorTarget{}, fmt.Errorf("%w: %v", api.ErrUnauthorized, err)
	}
	target = strings.TrimSpace(target)
	if target == "" {
		return ConnectorTarget{}, api.ErrNotFound
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	type record struct {
		onboarding api.Onboarding
		ids        map[string]bool
		source     string
		basename   string
	}
	var records []record
	for _, o := range s.onboardings {
		if o.PrincipalID != auth.PrincipalID || o.ContextID != auth.ContextID || o.RuntimeID != auth.RuntimeID || o.PolicyVersion != auth.PolicyVersion {
			continue
		}
		if o.Phase == api.PhaseRemoved || o.Phase == api.PhaseFailed || o.SupersededBy != "" {
			continue
		}
		ids := map[string]bool{o.OnboardingID: true}
		if o.DefinitionID != "" {
			ids[o.DefinitionID] = true
		}
		if o.ConnectionID != "" {
			ids[o.ConnectionID] = true
		}
		records = append(records, record{onboarding: o, ids: ids, source: o.SourceURL, basename: sourceBasename(o.SourceURL)})
	}
	aliasIDs, nameIDs := map[string]string{}, map[string]string{}
	if entries, err := api.PreparedCatalog(); err == nil {
		for _, entry := range entries {
			for _, alias := range entry.Aliases {
				aliasIDs[alias] = entry.ID
			}
			nameIDs[strings.ToLower(entry.Name)] = entry.ID
		}
	}
	lower := strings.ToLower(target)
	stages := []func(record) bool{
		func(r record) bool { return r.ids[target] },
		func(r record) bool { id := aliasIDs[lower]; return id != "" && r.ids[id] },
		func(r record) bool { return r.source == target },
		func(r record) bool { return r.basename != "" && r.basename == lower },
		func(r record) bool { id := nameIDs[lower]; return id != "" && r.ids[id] },
	}
	for _, stage := range stages {
		var matches []api.Onboarding
		for _, r := range records {
			if stage(r) {
				matches = append(matches, r.onboarding)
			}
		}
		if len(matches) != 1 {
			if len(matches) > 1 {
				candidates := make([]api.OperationCandidate, 0, len(matches))
				for _, o := range matches {
					candidates = append(candidates, api.OperationCandidate{ID: o.OnboardingID, Source: o.SourceURL, Kind: o.Mode})
				}
				return ConnectorTarget{}, &ErrAmbiguousTarget{Candidates: candidates}
			}
			continue
		}
		o := matches[0]
		return ConnectorTarget{OnboardingID: o.OnboardingID, DefinitionID: o.DefinitionID, ConnectionID: o.ConnectionID, SourceURL: o.SourceURL, Kind: o.Mode}, nil
	}
	return ConnectorTarget{}, fmt.Errorf("%w: connector %q", api.ErrNotFound, target)
}

// ErrAmbiguousTarget is the typed ambiguous_target selector failure; the
// facade maps it to the stable result code and echoes Candidates.
type ErrAmbiguousTarget struct {
	Candidates []api.OperationCandidate
}

// RequireConnectorTool gates one connector_* tool on the caller's admitted
// set — bundle profiles only; legacy per-op grants cannot mint these names.
func (s *Store) RequireConnectorTool(auth identity.Envelope, tool string) error {
	if !slices.Contains(api.ConnectorTools, tool) {
		return fmt.Errorf("%w: unknown connector tool", api.ErrInvalid)
	}
	allowed, err := s.AllowedControlOperations(auth)
	if err != nil {
		return err
	}
	if !allowed[tool] {
		return fmt.Errorf("%w: connector tool", api.ErrUnauthorized)
	}
	return nil
}

func (e *ErrAmbiguousTarget) Error() string {
	ids := make([]string, 0, len(e.Candidates))
	for _, c := range e.Candidates {
		ids = append(ids, c.ID)
	}
	return api.OpCodeAmbiguousTarget + ": " + strings.Join(ids, ", ")
}
