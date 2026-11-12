package toolhub

import (
	"fmt"
	"github.com/letya999/hermes-hub/internal/identity"
	"github.com/letya999/hermes-hub/internal/toolhub/api"
	"maps"
	"slices"
	"time"
)

// cloneOnboarding is the store's copy boundary for mutable record fields:
// the remove ladder mutates Finalizers in place, so the map and callers
// must never share its backing array.
func cloneOnboarding(o api.Onboarding) api.Onboarding {
	if o.Initiator != nil {
		actor := *o.Initiator
		actor.Scopes = slices.Clone(actor.Scopes)
		o.Initiator = &actor
	}
	o.Finalizers = slices.Clone(o.Finalizers)
	if o.HumanConsent != nil {
		consent := *o.HumanConsent
		o.HumanConsent = &consent
	}
	return o
}

func (s *Store) PutGrant(grant api.Grant) error {
	if err := grant.Validate(); err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	existing, exists := s.grants[grant.GrantID]
	if exists && grant.Revision == existing.Revision && api.RecordsEqual(existing, grant) {
		if s.path != "" {
			return s.saveLocked(s.path)
		}
		return nil
	}
	if exists && grant.Revision <= existing.Revision {
		return fmt.Errorf("%w: grant revision must advance", api.ErrConflict)
	}
	before := len(s.capabilityChanges)
	s.capabilityChanges = append(s.capabilityChanges, api.CapabilityChange{Grant: &grant})
	s.grants[grant.GrantID] = grant
	if s.path != "" {
		if err := s.saveLocked(s.path); err != nil {
			s.capabilityChanges = s.capabilityChanges[:before]
			if exists {
				s.grants[grant.GrantID] = existing
			} else {
				delete(s.grants, grant.GrantID)
			}
			return err
		}
	}
	return nil
}

func (s *Store) PutPublication(pub api.DefinitionPublication) error {
	if err := pub.Validate(); err != nil {
		return err
	}
	s.mu.Lock()
	if !s.hasDefinitionID(pub.DefinitionID) {
		s.mu.Unlock()
		return fmt.Errorf("%w: publication definition", api.ErrNotFound)
	}
	s.publications[definitionKey(pub.DefinitionID, pub.Version)] = pub
	s.mu.Unlock()
	return s.persist()
}

func (s *Store) PutSharedCredentialPolicy(policy api.SharedCredentialPolicy) error {
	if err := policy.Validate(); err != nil {
		return err
	}
	s.mu.Lock()
	s.sharedPolicies[policy.PolicyID] = policy
	s.mu.Unlock()
	return s.persist()
}

func (s *Store) PutOnboarding(onboarding api.Onboarding) error {
	if err := onboarding.Validate(); err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.putOnboardingLocked(onboarding)
}

func (s *Store) putOnboardingLocked(onboarding api.Onboarding) error {
	previous, existed := s.onboardings[onboarding.OnboardingID]
	onboarding = prepareOnboardingWrite(previous, onboarding)
	if existed {
		if api.RecordsEqual(previous, onboarding) {
			return nil
		}
		if err := api.ValidateOnboardingTransition(previous, onboarding); err != nil {
			return err
		}
	}
	if err := onboarding.Validate(); err != nil {
		return err
	}
	return s.commitOnboardingLocked(onboarding)
}

// commitOnboardingLocked also stores metadata-only acknowledgement facts, whose
// version is NoticeVersion rather than the domain transition revision.
func (s *Store) commitOnboardingLocked(onboarding api.Onboarding) error {
	previous, existed := s.onboardings[onboarding.OnboardingID]
	oldOps := s.operations
	s.operations = maps.Clone(oldOps)
	for id, op := range s.operations {
		if op.OnboardingID == onboarding.OnboardingID {
			s.operations[id] = installProgress(op, onboarding)
		}
	}
	s.onboardings[onboarding.OnboardingID] = cloneOnboarding(onboarding)
	if s.path != "" {
		previousDigest := s.diskDigest
		if err := s.saveLocked(s.path); err != nil {
			if s.diskDigest == previousDigest {
				s.operations = oldOps
				if existed {
					s.onboardings[onboarding.OnboardingID] = previous
				} else {
					delete(s.onboardings, onboarding.OnboardingID)
				}
			}
			return err
		}
	}
	return nil
}

// PutOnboardingIfRevision prevents concurrent Broker polls from replacing a
// newer confirmation proof or resurrecting an onboarding being removed.
func (s *Store) PutOnboardingIfRevision(onboarding api.Onboarding, expected uint64) error {
	if err := onboarding.Validate(); err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	previous, ok := s.onboardings[onboarding.OnboardingID]
	if !ok || previous.Revision != expected || onboarding.Revision != expected+1 {
		return api.ErrStale
	}
	return s.putOnboardingLocked(onboarding)
}

// ConsumeConfirmation persists the single-use claim before binding work starts.
func (s *Store) ConsumeConfirmation(expected api.Onboarding) (api.Onboarding, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.consumeConfirmationLocked(expected, false, nil)
}

func (s *Store) consumeConfirmationLocked(expected api.Onboarding, completeWrites bool, consent *api.HumanConsent) (api.Onboarding, error) {
	current, ok := s.onboardings[expected.OnboardingID]
	if !ok || current.Revision != expected.Revision || current.ConfirmationUsed {
		return api.Onboarding{}, fmt.Errorf("%w: confirmation already consumed or stale", api.ErrUnauthorized)
	}
	current.ConfirmationUsed = true
	if consent != nil {
		current.HumanConsent = consent
	}
	if completeWrites {
		current.WritesEnabled, current.PendingWrites, current.Phase = true, false, api.PhaseEnabled
	}
	current.Revision++
	if err := s.putOnboardingLocked(current); err != nil {
		return api.Onboarding{}, err
	}
	return cloneOnboarding(s.onboardings[current.OnboardingID]), nil
}

// ClaimPreparingOnboarding atomically reports a fresh record already in flight
// or persists the new one under the store lock — concurrent identical prepares
// cannot both proceed to review+build. A record stale past maxAge belongs to a
// crashed prepare and is overwritten.
func (s *Store) ClaimPreparingOnboarding(onboarding api.Onboarding, maxAge time.Duration, now time.Time) (api.Onboarding, bool, error) {
	if err := onboarding.Validate(); err != nil {
		return api.Onboarding{}, false, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if existing, ok := s.onboardings[onboarding.OnboardingID]; ok && existing.Phase == api.PhasePreparing && now.Before(existing.CreatedAt.Add(maxAge)) {
		return existing, true, nil
	}
	if existing, ok := s.onboardings[onboarding.OnboardingID]; ok {
		onboarding.Revision = existing.Revision + 1
	}
	if err := s.putOnboardingLocked(onboarding); err != nil {
		return api.Onboarding{}, false, err
	}
	return cloneOnboarding(s.onboardings[onboarding.OnboardingID]), false, nil
}

func (s *Store) PromoteToCatalog(definitionID, version, operator string) error {
	if !identity.ValidID(operator) || operator == "model" || operator == "hermes" {
		return fmt.Errorf("%w: catalog promotion issuer", api.ErrUnauthorized)
	}
	s.mu.Lock()
	key := definitionKey(definitionID, version)
	if _, ok := s.definitions[key]; !ok {
		s.mu.Unlock()
		return fmt.Errorf("%w: definition", api.ErrNotFound)
	}
	pub := s.publications[key]
	pub.DefinitionID = definitionID
	pub.Version = version
	pub.Visibility = api.PublicationCatalog
	if err := pub.Validate(); err != nil {
		s.mu.Unlock()
		return err
	}
	s.publications[key] = pub
	s.mu.Unlock()
	return s.persist()
}

func (s *Store) publicationLocked(definition api.ToolDefinition) api.DefinitionPublication {
	if pub, ok := s.publications[definitionKey(definition.DefinitionID, definition.Version)]; ok {
		return pub
	}
	return api.DefinitionPublication{DefinitionID: definition.DefinitionID, Version: definition.Version, Visibility: api.PublicationCatalog}
}

func (s *Store) visibleDefinitionLocked(auth identity.Envelope, definition api.ToolDefinition) bool {
	pub := s.publicationLocked(definition)
	if pub.Visibility == api.PublicationUser {
		return pub.OwnerPrincipalID == auth.PrincipalID
	}
	return s.hasCatalogAccessLocked(auth, definition.DefinitionID, definition.Version)
}

func (s *Store) hasCatalogAccessLocked(auth identity.Envelope, definitionID, version string) bool {
	allowed := false
	for _, grant := range s.grants {
		if grant.PrincipalID != auth.PrincipalID {
			continue
		}
		if grant.Kind != api.GrantCatalogDefault && !(grant.Kind == api.GrantDefinition && grant.DefinitionID == definitionID && grant.DefinitionVersion == version) {
			continue
		}
		if grant.Status != api.ActiveStatus {
			return false
		}
		allowed = true
	}
	return allowed
}

func (s *Store) selfInstallAllowedLocked(auth identity.Envelope) bool {
	allowed := false
	for _, grant := range s.grants {
		if grant.Kind == api.GrantSelfInstall && grant.PrincipalID == auth.PrincipalID {
			if grant.Status != api.ActiveStatus {
				return false
			}
			allowed = true
		}
	}
	return allowed
}

func (s *Store) sharedPolicyLocked(auth identity.Envelope, definitionID string) *api.SharedCredentialPolicy {
	for _, policy := range s.sharedPolicies {
		if policy.Status != api.ActiveStatus || policy.DefinitionID != definitionID {
			continue
		}
		if slices.Contains(policy.Principals, auth.PrincipalID) {
			copy := policy
			return &copy
		}
	}
	return nil
}

// sharedConnectionCoversLocked reports whether a connection owned by a shared
// policy's store owner is usable by this member: the connection must name the
// policy and the policy must be active and list the caller. This is the
// per-call authorization for member bindings on a shared workload.
func (s *Store) sharedConnectionCoversLocked(auth identity.Envelope, connection api.Connection) bool {
	policyID := connection.Metadata["shared_policy"]
	if policyID == "" {
		return false
	}
	policy, ok := s.sharedPolicies[policyID]
	if !ok || policy.Status != api.ActiveStatus || policy.DefinitionID != connection.DefinitionID {
		return false
	}
	return policy.StoreOwner == connection.Owner.ID && slices.Contains(policy.Principals, auth.PrincipalID)
}

// sharedForeignConnection protects team connections and shared-policy
// connections from member-level lifecycle cleanup.
func (s *Store) sharedForeignConnection(principalID, connectionID string) bool {
	if connectionID == "" {
		return false
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	connection, ok := s.connections[connectionID]
	return ok && (connection.Owner.Type != api.PrincipalOwner || connection.Owner.ID != principalID)
}

// teamConnectionCoversLocked admits a member to a team-owned connection: the
// owner ref names the team and membership is read from the stored record on
// every call, so member removal takes effect without mutating bindings.
func (s *Store) teamConnectionCoversLocked(auth identity.Envelope, connection api.Connection) bool {
	if connection.Owner.Type != api.TeamOwner {
		return false
	}
	team, ok := s.teams[connection.Owner.ID]
	return ok && team.IsMember(auth.PrincipalID)
}

// teamMemberWritesLocked reports whether a call through this connection may
// carry write-effect tools: non-team connections keep their existing surface,
// team members need the operator's write policy on the team record.
func (s *Store) teamMemberWritesLocked(auth identity.Envelope, effective api.EffectiveBinding) bool {
	if effective.Connection == nil || effective.Connection.Owner.Type != api.TeamOwner {
		return true
	}
	team, ok := s.teams[effective.Connection.Owner.ID]
	return ok && team.CanWrite(auth.PrincipalID)
}

func (s *Store) RequireCatalogAccess(auth identity.Envelope, definition api.ToolDefinition) error {
	if err := auth.Validate(auth.PrincipalID, auth.ContextID, auth.RuntimeID, auth.PolicyVersion); err != nil {
		return fmt.Errorf("%w: %v", api.ErrUnauthorized, err)
	}
	if err := s.Reload(); err != nil {
		return err
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	if auth.CapabilityProfile != "" {
		tools, err := s.managedToolsLocked(auth)
		if err != nil {
			return err
		}
		for _, tool := range tools {
			if tool.DefinitionID == definition.DefinitionID && tool.Version == definition.Version {
				return nil
			}
		}
		return fmt.Errorf("%w: catalog grant", api.ErrUnauthorized)
	}
	pub := s.publicationLocked(definition)
	if pub.Visibility == api.PublicationUser {
		if pub.OwnerPrincipalID != auth.PrincipalID {
			return fmt.Errorf("%w: user definition owner", api.ErrUnauthorized)
		}
		return nil
	}
	if !s.hasCatalogAccessLocked(auth, definition.DefinitionID, definition.Version) {
		return fmt.Errorf("%w: catalog grant", api.ErrUnauthorized)
	}
	return nil
}

func (s *Store) RequireSelfInstall(auth identity.Envelope) error {
	// Managed installation is admitted only by an operator-confirmed profile
	// field — never by legacy principal grants. The pipeline still runs the
	// authenticated single-use confirmation before a binding materializes.
	if auth.CapabilityProfile != "" {
		if err := auth.Validate(auth.PrincipalID, auth.ContextID, auth.RuntimeID, auth.PolicyVersion); err != nil {
			return fmt.Errorf("%w: %v", api.ErrUnauthorized, err)
		}
		if err := s.Reload(); err != nil {
			return err
		}
		s.mu.RLock()
		defer s.mu.RUnlock()
		profile, _, err := s.capabilityProfileLocked(auth)
		if err != nil || !profile.SelfInstall {
			return fmt.Errorf("%w: managed installation is not admitted", api.ErrUnauthorized)
		}
		return nil
	}
	if err := auth.Validate(auth.PrincipalID, auth.ContextID, auth.RuntimeID, auth.PolicyVersion); err != nil {
		return fmt.Errorf("%w: %v", api.ErrUnauthorized, err)
	}
	if err := s.Reload(); err != nil {
		return err
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	if !s.selfInstallAllowedLocked(auth) {
		return fmt.Errorf("%w: self-install grant", api.ErrUnauthorized)
	}
	return nil
}

func (s *Store) controlAllowedLocked(auth identity.Envelope, operation string) bool {
	allowed := false
	for _, grant := range s.grants {
		if grant.Kind != api.GrantControlOperation || grant.PrincipalID != auth.PrincipalID || grant.Operation != operation {
			continue
		}
		if grant.Status != api.ActiveStatus {
			return false
		}
		allowed = true
	}
	return allowed
}

// AllowedControlOperations is an authorized projection, never an installation or
// catalog grant. Every invocation checks the current grant again independently.
func (s *Store) AllowedControlOperations(auth identity.Envelope) (map[string]bool, error) {
	if err := auth.Validate(auth.PrincipalID, auth.ContextID, auth.RuntimeID, auth.PolicyVersion); err != nil {
		return nil, fmt.Errorf("%w: %v", api.ErrUnauthorized, err)
	}
	if err := s.Reload(); err != nil {
		return nil, err
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	allowed := map[string]bool{}
	if auth.CapabilityProfile != "" {
		profile, _, err := s.capabilityProfileLocked(auth)
		if err != nil {
			return allowed, err
		}
		// The connectors bundle replaces the legacy per-operation projection
		// with the unified nine-tool surface. invoke rides with it: it is the
		// dispatch fallback for names missing from the client's cached
		// tools/list, and every call still routes through
		// AuthorizeProjectedCall (ADR-0089 D3). Under "" the profile's own
		// control_operations allowlist stays the strict contract.
		if profile.Connectors == api.ConnectorsPersonal || profile.Connectors == api.ConnectorsPersonalTeam {
			if decision, err := s.decideLocked(auth, "tools", "use", "", time.Now().UTC()); err == nil && decision.Allow {
				allowed["invoke"] = true
			}
			for _, tool := range api.ConnectorTools {
				allowed[tool] = true
			}
			return allowed, nil
		}
		if profile.Connectors == api.ConnectorsNone {
			return allowed, nil
		}
		// The published tools.use right admits the invoke control —
		// the tool_call dispatch path for extended selections — and the
		// profile's own control_operations allowlist cannot express it. The
		// right is re-decided on every call, so a revoked or ceiling-dropped
		// right closes the control on the next projection refresh.
		if decision, err := s.decideLocked(auth, "tools", "use", "", time.Now().UTC()); err == nil && decision.Allow {
			allowed["invoke"] = true
		}
		for _, operation := range profile.ControlOperations {
			if slices.Contains(api.ControlOperations, operation) {
				allowed[operation] = true
			}
		}
		return allowed, nil
	}
	for _, operation := range append(slices.Clone(api.ControlOperations), "invoke") {
		if s.controlAllowedLocked(auth, operation) {
			allowed[operation] = true
		}
	}
	return allowed, nil
}

func (s *Store) RequireControlOperation(auth identity.Envelope, operation string) error {
	if !api.ValidControlOperation(operation) {
		return fmt.Errorf("%w: unknown control operation", api.ErrInvalid)
	}
	allowed, err := s.AllowedControlOperations(auth)
	if err != nil {
		return err
	}
	if !allowed[operation] {
		return fmt.Errorf("%w: control operation", api.ErrUnauthorized)
	}
	return nil
}

func (s *Store) OnboardingFor(auth identity.Envelope, id string) (api.Onboarding, error) {
	if err := auth.Validate(auth.PrincipalID, auth.ContextID, auth.RuntimeID, auth.PolicyVersion); err != nil {
		return api.Onboarding{}, fmt.Errorf("%w: %v", api.ErrUnauthorized, err)
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	onboarding, ok := s.onboardings[id]
	if !ok {
		return api.Onboarding{}, fmt.Errorf("%w: onboarding", api.ErrNotFound)
	}
	if onboarding.PrincipalID != auth.PrincipalID || onboarding.ContextID != auth.ContextID || onboarding.RuntimeID != auth.RuntimeID || onboarding.PolicyVersion != auth.PolicyVersion {
		return api.Onboarding{}, fmt.Errorf("%w: onboarding owner", api.ErrUnauthorized)
	}
	return cloneOnboarding(onboarding), nil
}

func (s *Store) FindOnboardingByKey(auth identity.Envelope, key string) (api.Onboarding, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	var removed api.Onboarding
	found := false
	for _, onboarding := range s.onboardings {
		if onboarding.PrincipalID == auth.PrincipalID && onboarding.ContextID == auth.ContextID && onboarding.RuntimeID == auth.RuntimeID && onboarding.PolicyVersion == auth.PolicyVersion && onboarding.IdempotencyKey == key && key != "" {
			if onboarding.Phase == api.PhaseRemoved {
				removed, found = onboarding, true
				continue
			}
			return onboarding, true
		}
	}
	return removed, found
}
