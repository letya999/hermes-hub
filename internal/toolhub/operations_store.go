package toolhub

import (
	"errors"
	"fmt"
	"maps"
	"slices"
	"strings"
	"time"

	"github.com/letya999/hermes-hub/internal/identity"
	"github.com/letya999/hermes-hub/internal/toolhub/api"
)

// PutOperation validates and durably stores one journal record.
func (s *Store) PutOperation(op api.Operation) error {
	if err := op.Validate(); err != nil {
		return err
	}
	// Detach the caller's slice: journal verbs keep mutating their copy via
	// markStep, so the stored record must own its Steps.
	op.Steps = slices.Clone(op.Steps)
	s.mu.Lock()
	defer s.mu.Unlock()
	previous, exists := s.operations[op.OperationID]
	if o, ok := s.onboardings[op.OnboardingID]; ok {
		op = installProgress(op, o)
	}
	s.operations[op.OperationID] = op
	if s.path != "" {
		before := s.diskDigest
		if err := s.saveLocked(s.path); err != nil {
			if before == s.diskDigest {
				if exists {
					s.operations[op.OperationID] = previous
				} else {
					delete(s.operations, op.OperationID)
				}
			}
			return err
		}
	}
	return nil
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
	if o, ok := s.onboardings[op.OnboardingID]; ok {
		op = installProgress(op, o)
	}
	op.Steps = slices.Clone(op.Steps)
	return op, nil
}

// OperationForRequestKey resolves the live operation for one
// (principal, context, runtime, policy, environment, generation, intent, target) tuple. Superseded records are
// never candidates — a retry opens a fresh operation.
func (s *Store) OperationForRequestKey(auth identity.Envelope, intent, target string) (api.Operation, bool) {
	key := api.OperationRequestKey(auth, intent, target)
	legacyKey := api.DeterministicID("opkey", auth.PrincipalID, intent, strings.ToLower(strings.TrimSpace(target)))
	s.mu.RLock()
	defer s.mu.RUnlock()
	for _, op := range s.operations {
		if (op.RequestKey != key && op.RequestKey != legacyKey) || op.PrincipalID != auth.PrincipalID || op.ContextID != auth.ContextID || op.RuntimeID != auth.RuntimeID || op.PolicyVersion != auth.PolicyVersion || op.SupersededBy != "" {
			continue
		}
		op.Steps = slices.Clone(op.Steps)
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
		if o.DeadEnd() || o.SupersededBy != "" || o.Stage == api.StageFailed && target != o.OnboardingID && target != o.ConnectionID {
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
					candidates = append(candidates, api.OperationCandidate{ID: o.OnboardingID, Source: o.SourceURL, Kind: o.Mode, State: o.ModelState(false), YouMust: o.YouMust(time.Now())})
				}
				return ConnectorTarget{}, &ErrAmbiguousTarget{Candidates: candidates}
			}
			continue
		}
		o := matches[0]
		return ConnectorTarget{OnboardingID: o.OnboardingID, DefinitionID: o.DefinitionID, ConnectionID: o.ConnectionID, SourceURL: o.SourceURL, Kind: o.Mode}, nil
	}
	// A member naming a team-owned connection they may call but never mutate
	// resolves to the typed team denial rather than a bare not-found — the
	// resource exists and the facade reports not_permitted.
	for _, connection := range s.connections {
		if connection.Owner.Type != api.TeamOwner {
			continue
		}
		if target != connection.ConnectionID && target != connection.DefinitionID {
			continue
		}
		if team, ok := s.teams[connection.Owner.ID]; ok && team.IsMember(auth.PrincipalID) {
			return ConnectorTarget{}, fmt.Errorf("%w: connector %q", ErrTeamConnectionTarget, target)
		}
	}
	return ConnectorTarget{}, fmt.Errorf("%w: connector %q", api.ErrNotFound, target)
}

// ownsConnectorTarget reports whether target names an owned onboarding or
// connection across every policy epoch — it distinguishes a record that
// merely predates the caller's epoch (cleanup through remove) from a true
// not-found. Removed and superseded records are already dead ends.
func (s *Store) ownsConnectorTarget(auth identity.Envelope, target string) bool {
	if target == "" {
		return false
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	for _, o := range s.onboardings {
		if o.PrincipalID != auth.PrincipalID || o.ContextID != auth.ContextID || o.RuntimeID != auth.RuntimeID {
			continue
		}
		if o.DeadEnd() || o.SupersededBy != "" {
			continue
		}
		if o.OnboardingID == target || o.ConnectionID == target || o.DefinitionID == target || o.SourceURL == target {
			return true
		}
	}
	for _, connection := range s.connections {
		if connection.Owner.Matches(auth) && (connection.ConnectionID == target || connection.DefinitionID == target) {
			return true
		}
	}
	return false
}

// ErrTeamConnectionTarget marks a lifecycle target that resolved to a
// team-owned connection the caller may use as a member but never mutate —
// the facade maps it to the not_permitted result code.
var ErrTeamConnectionTarget = errors.New("team-owned connection")

// ErrAmbiguousTarget is the typed ambiguous_target selector failure; the
// facade maps it to the stable result code and echoes Candidates.
type ErrAmbiguousTarget struct {
	Candidates []api.OperationCandidate
}

// RequireConnectorTool admits an exact selected tool or its legacy bundle verb;
// legacy per-op grants alone cannot mint connector tools.
func (s *Store) RequireConnectorTool(auth identity.Envelope, tool, legacyTool string, args map[string]any) error {
	if tool != "connector_edit" && !slices.Contains(api.ConnectorTools, tool) {
		return fmt.Errorf("%w: unknown connector tool", api.ErrInvalid)
	}
	if auth.CapabilityProfile != "" {
		if err := s.Reload(); err != nil {
			return err
		}
		s.mu.RLock()
		_, _, err := s.managedToolLocked(auth, tool, args, false)
		s.mu.RUnlock()
		if err == nil {
			return nil
		}
	}
	allowed, err := s.AllowedControlOperations(auth)
	if err != nil {
		return err
	}
	if !allowed[legacyTool] {
		return fmt.Errorf("%w: connector tool", api.ErrUnauthorized)
	}
	return nil
}

// SweepConnectionOnboardings retires every non-terminal onboarding still
// bound to a removed connection: spammed or stale install attempts of the
// same connector are zombies once the shared resource is gone. The removing
// record is excluded — it carries the receipt.
func (s *Store) SweepConnectionOnboardings(connectionID, exceptOnboardingID string) error {
	if connectionID == "" {
		return nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	oldOnboardings, oldOperations := s.onboardings, s.operations
	s.onboardings, s.operations = maps.Clone(oldOnboardings), maps.Clone(oldOperations)
	for id, o := range s.onboardings {
		if o.ConnectionID != connectionID || o.OnboardingID == exceptOnboardingID {
			continue
		}
		if o.Terminal() || o.Stage == api.StageFailed || o.SupersededBy != "" {
			continue
		}
		o.Retire(api.TombstoneRemoved)
		o.Revision++
		s.onboardings[id] = prepareOnboardingWrite(oldOnboardings[id], o)
		for opID, op := range s.operations {
			if op.OnboardingID == id {
				s.operations[opID] = installProgress(op, o)
			}
		}
	}
	if s.path != "" {
		before := s.diskDigest
		if err := s.saveLocked(s.path); err != nil {
			if before == s.diskDigest {
				s.onboardings, s.operations = oldOnboardings, oldOperations
			}
			return err
		}
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
