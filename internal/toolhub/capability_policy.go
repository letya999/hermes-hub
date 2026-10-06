package toolhub

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path"
	"regexp"
	"slices"
	"strings"
	"time"

	"github.com/gofrs/flock"
	"github.com/letya999/hermes-hub/internal/identity"
)

var capabilityIDPattern = regexp.MustCompile(`^[a-z][a-z0-9_.-]{0,95}$`)

// CapabilityUse is reviewed with the immutable tool definition. Resource is a
// logical server-owned root/account, never a model-supplied host path or URL.
// PathArgument, when present, selects a top-level relative-path argument.
type CapabilityUse struct {
	Action       string `json:"action"`
	Resource     string `json:"resource"`
	PathArgument string `json:"path_argument,omitempty"`
}

type CapabilityLimits struct {
	OutputBytes    int `json:"output_bytes"`
	TimeoutSeconds int `json:"timeout_seconds"`
}

// A rule is one complete tuple. Action and resource cannot be borrowed from
// different rules. Prefixes constrain logical paths, not filesystem traversal.
type CapabilityRule struct {
	CapabilityID         string           `json:"capability_id"`
	ImplementationDigest string           `json:"implementation_digest"`
	Action               string           `json:"action"`
	Resource             string           `json:"resource"`
	ConnectionID         string           `json:"connection_id,omitempty"`
	PathPrefix           string           `json:"path_prefix,omitempty"`
	Limits               CapabilityLimits `json:"limits"`
	ExpiresAt            time.Time        `json:"expires_at,omitempty"`
}

// A group is an embedded grant snapshot, not a live name lookup. Updating its
// members requires a new policy/profile revision and cannot widen old grants.
type CapabilityGroup struct {
	GroupID  string           `json:"group_id"`
	Revision uint64           `json:"revision"`
	Members  []CapabilityRule `json:"members"`
}

// Confirmation is durable evidence that a human operator reviewed the exact
// record bytes. The digest binds the act to the canonical content and the
// confirmation issuer must equal the record issuer, so one operator identity
// cannot stamp for another. Records without a valid confirmation carry no
// authority: model and agent paths cannot mint one and host paths must opt in.
type Confirmation struct {
	Digest      string    `json:"digest"`
	IssuedBy    string    `json:"issued_by"`
	ConfirmedAt time.Time `json:"confirmed_at"`
}

type CapabilityPolicy struct {
	Schema        int               `json:"schema"`
	PolicyID      string            `json:"policy_id"`
	Organization  string            `json:"organization,omitempty"`
	Members       []string          `json:"members"`
	Revision      uint64            `json:"revision"`
	IssuedBy      string            `json:"issued_by"`
	IssuedAt      time.Time         `json:"issued_at"`
	Reason        string            `json:"reason"`
	Status        Status            `json:"status"`
	Ceiling       []CapabilityRule  `json:"ceiling"`
	Defaults      []CapabilityRule  `json:"defaults"`
	DefaultGroups []CapabilityGroup `json:"default_groups,omitempty"`
	Denies        []CapabilityRule  `json:"denies,omitempty"`
	Confirmation  *Confirmation     `json:"confirmation,omitempty"`
}

type CapabilitySelection struct {
	CapabilityID         string `json:"capability_id"`
	DefinitionID         string `json:"definition_id"`
	DefinitionVersion    string `json:"definition_version"`
	ImplementationDigest string `json:"implementation_digest"`
	ToolName             string `json:"tool_name"`
	Name                 string `json:"name"`
	ConnectionID         string `json:"connection_id,omitempty"`
}

// A profile is published by the host control path for an exact runtime
// generation. An absent profile or policy never enables legacy grants.
type CapabilityProfile struct {
	Schema         int                   `json:"schema"`
	ProfileID      string                `json:"profile_id"`
	PrincipalID    string                `json:"principal_id"`
	ContextID      string                `json:"context_id"`
	RuntimeID      string                `json:"runtime_id"`
	Environment    string                `json:"environment"`
	Generation     uint64                `json:"generation"`
	PolicyVersion  string                `json:"policy_version"`
	PolicyID       string                `json:"policy_id"`
	PolicyRevision uint64                `json:"policy_revision"`
	Revision       uint64                `json:"revision"`
	IssuedBy       string                `json:"issued_by"`
	IssuedAt       time.Time             `json:"issued_at"`
	Reason         string                `json:"reason"`
	Status         Status                `json:"status"`
	Selections     []CapabilitySelection `json:"selections"`
	Allows         []CapabilityRule      `json:"allows,omitempty"`
	AllowGroups    []CapabilityGroup     `json:"allow_groups,omitempty"`
	Denies         []CapabilityRule      `json:"denies,omitempty"`
	// ControlOperations is the reviewed allowlist of control-plane operations
	// the managed principal's runtime token may invoke. Names must come from
	// ControlOperations; absent means none, and legacy grants never leak in.
	ControlOperations []string `json:"control_operations,omitempty"`
	// SelfInstall admits the GitHub-source prepare path for this principal.
	// Installation stays a distinct grant (SPEC-0041 CP-05): the pipeline
	// still requires the single-use human confirmation and enable phases.
	SelfInstall  bool          `json:"self_install,omitempty"`
	Confirmation *Confirmation `json:"confirmation,omitempty"`
}

// Policy history is the persisted authority. Current maps are rebuilt from it,
// so a grant and its issuer/scope/revision record cannot commit separately.
// The registry's existing atomic snapshot and stale-writer fence cover both.
type capabilityChange struct {
	Policy  *CapabilityPolicy  `json:"policy,omitempty"`
	Profile *CapabilityProfile `json:"profile,omitempty"`
	Grant   *Grant             `json:"grant,omitempty"`
}

// Called with s.mu held, preserving the same lock order as saveLocked. The
// shared disk fence prevents another host process from committing a revoke
// between our snapshot check and admission. Projection refresh alone cannot.
func (s *Store) lockCapabilityAdmission() (func(), error) {
	if s.path == "" {
		return func() {}, nil
	}
	if err := safeStorePath(s.path); err != nil {
		return nil, err
	}
	if err := safeStorePath(s.path + ".lock"); err != nil {
		return nil, err
	}
	lock := flock.New(s.path + ".lock")
	if err := lock.RLock(); err != nil {
		return nil, err
	}
	body, err := os.ReadFile(s.path) // #nosec G304 -- protected registry path validated above.
	if err != nil || sha256.Sum256(body) != s.diskDigest {
		_ = lock.Unlock()
		if err != nil {
			return nil, err
		}
		return nil, fmt.Errorf("%w: capability snapshot changed before admission", ErrStale)
	}
	return func() { _ = lock.Unlock() }, nil
}

func DefinitionDigest(definition ToolDefinition) string {
	body, err := json.Marshal(definition)
	if err != nil {
		return ""
	}
	sum := sha256.Sum256(body)
	return "sha256:" + hex.EncodeToString(sum[:])
}

// ConfirmationDigest is the canonical digest the CLI prints for review and the
// store re-derives at admission. It covers the record with the confirmation
// field removed, so a stamped record digests identically to its reviewed form.
func ConfirmationDigest(record any) string {
	body, err := json.Marshal(record)
	if err != nil {
		return ""
	}
	var shadow map[string]json.RawMessage
	if err := json.Unmarshal(body, &shadow); err != nil {
		return ""
	}
	delete(shadow, "confirmation")
	canonical, err := json.Marshal(shadow)
	if err != nil {
		return ""
	}
	sum := sha256.Sum256(canonical)
	return "sha256:" + hex.EncodeToString(sum[:])
}

// Confirm stamps issuer's confirmation onto a policy, profile, or grant
// pointer. An empty issuer defaults to the record's own issuer, which is also
// what cross-field identity validation enforces at admission.
func Confirm(record any, issuer string, now time.Time) error {
	slot, recordIssuer := confirmationTarget(record)
	if slot == nil {
		return fmt.Errorf("%w: unsupported confirmation record", ErrInvalid)
	}
	if issuer == "" {
		issuer = recordIssuer
	}
	*slot = nil
	confirmation, err := newConfirmation(record, issuer, now)
	if err != nil {
		return err
	}
	*slot = &confirmation
	return nil
}

func confirmationTarget(record any) (**Confirmation, string) {
	switch r := record.(type) {
	case *CapabilityPolicy:
		return &r.Confirmation, r.IssuedBy
	case *CapabilityProfile:
		return &r.Confirmation, r.IssuedBy
	case *Grant:
		return &r.Confirmation, r.IssuedBy
	}
	return nil, ""
}

func newConfirmation(record any, issuer string, now time.Time) (Confirmation, error) {
	if !identity.ValidID(issuer) || issuer == "model" || issuer == "hermes" {
		return Confirmation{}, fmt.Errorf("%w: confirmation issuer", ErrUnauthorized)
	}
	digest := ConfirmationDigest(record)
	if digest == "" {
		return Confirmation{}, fmt.Errorf("%w: confirmation digest", ErrInvalid)
	}
	return Confirmation{Digest: digest, IssuedBy: issuer, ConfirmedAt: now.UTC()}, nil
}

func validConfirmation(confirmation *Confirmation, record any, issuer string) bool {
	return confirmation != nil && confirmation.IssuedBy == issuer &&
		confirmation.Digest != "" && confirmation.Digest == ConfirmationDigest(record) &&
		!confirmation.ConfirmedAt.IsZero() && confirmation.ConfirmedAt.Year() <= 9999
}

func validCapabilityPath(value string) bool {
	return value == "" || (len(value) <= 4096 && value != "." && !strings.HasPrefix(value, "/") &&
		!strings.ContainsAny(value, "\\:\x00\r\n") && path.Clean(value) == value &&
		value != ".." && !strings.HasPrefix(value, "../"))
}

func prefixContains(prefix, value string) bool {
	return prefix == "" || value == prefix || strings.HasPrefix(value, prefix+"/")
}

func validPolicyRecord(schema int, id string, revision uint64, issuer, reason string, issuedAt time.Time, status Status) bool {
	return schema == SchemaVersion && identity.ValidID(id) && revision > 0 && identity.ValidID(issuer) &&
		!issuedAt.IsZero() && issuedAt.Year() >= 1 && issuedAt.Year() <= 9999 &&
		issuer != "model" && issuer != "hermes" && strings.TrimSpace(reason) != "" && len(reason) <= 512 &&
		!strings.ContainsAny(reason, "\x00\r\n") && (status == ActiveStatus || status == DisabledStatus || status == RevokedStatus)
}

func validateCapabilityRules(rules []CapabilityRule, deny bool) error {
	if len(rules) > 4096 {
		return fmt.Errorf("%w: too many capability rules", ErrInvalid)
	}
	for _, rule := range rules {
		if !capabilityIDPattern.MatchString(rule.CapabilityID) || !digestPattern.MatchString(rule.ImplementationDigest) ||
			!identity.ValidID(rule.Action) || !identity.ValidID(rule.Resource) || !validCapabilityPath(rule.PathPrefix) ||
			(rule.ConnectionID != "" && !identity.ValidID(rule.ConnectionID)) || (!rule.ExpiresAt.IsZero() && (rule.ExpiresAt.Year() < 1 || rule.ExpiresAt.Year() > 9999)) {
			return fmt.Errorf("%w: capability rule tuple", ErrInvalid)
		}
		if deny {
			if rule.Limits != (CapabilityLimits{}) {
				return fmt.Errorf("%w: deny rule cannot carry execution limits", ErrInvalid)
			}
		} else if rule.Limits.OutputBytes <= 0 || rule.Limits.OutputBytes > MaxOutputBytes || rule.Limits.TimeoutSeconds <= 0 || rule.Limits.TimeoutSeconds > MaxExecutionTimeout {
			return fmt.Errorf("%w: capability rule limits", ErrInvalid)
		}
	}
	return nil
}

// ValidateCapabilityGroup is the host-side authoring check used by hubctl before
// a group is embedded into a policy/profile revision. Groups carry no
// confirmation of their own; the enclosing record's confirmation covers them.
func ValidateCapabilityGroup(group CapabilityGroup) error {
	return validateCapabilityGroups([]CapabilityGroup{group})
}

func validateCapabilityGroups(groups []CapabilityGroup) error {
	if len(groups) > 256 {
		return fmt.Errorf("%w: too many capability groups", ErrInvalid)
	}
	seen := map[string]bool{}
	for _, group := range groups {
		if !identity.ValidID(group.GroupID) || group.Revision == 0 || len(group.Members) == 0 || len(group.Members) > 256 || seen[group.GroupID] {
			return fmt.Errorf("%w: capability group identity or membership", ErrInvalid)
		}
		seen[group.GroupID] = true
		if err := validateCapabilityRules(group.Members, false); err != nil {
			return err
		}
		for i, member := range group.Members {
			for _, previous := range group.Members[:i] {
				if sameCapabilityTuple(member, previous) && member.PathPrefix == previous.PathPrefix {
					return fmt.Errorf("%w: duplicate capability group member", ErrInvalid)
				}
			}
		}
	}
	return nil
}

func checkGroupRevisions(previous, next []CapabilityGroup) error {
	for _, old := range previous {
		for _, current := range next {
			if old.GroupID == current.GroupID && (current.Revision < old.Revision ||
				(current.Revision == old.Revision && !slices.Equal(old.Members, current.Members))) {
				return fmt.Errorf("%w: capability group revision", ErrConflict)
			}
		}
	}
	return nil
}

func sameCapabilityTuple(a, b CapabilityRule) bool {
	return a.CapabilityID == b.CapabilityID && a.ImplementationDigest == b.ImplementationDigest &&
		a.Action == b.Action && a.Resource == b.Resource && a.ConnectionID == b.ConnectionID
}

func ruleInside(outer, inner CapabilityRule) bool {
	return sameCapabilityTuple(outer, inner) && prefixContains(outer.PathPrefix, inner.PathPrefix) &&
		inner.Limits.OutputBytes <= outer.Limits.OutputBytes && inner.Limits.TimeoutSeconds <= outer.Limits.TimeoutSeconds &&
		(outer.ExpiresAt.IsZero() || (!inner.ExpiresAt.IsZero() && !inner.ExpiresAt.After(outer.ExpiresAt)))
}

func (p CapabilityPolicy) Validate() error {
	if !validPolicyRecord(p.Schema, p.PolicyID, p.Revision, p.IssuedBy, p.Reason, p.IssuedAt, p.Status) ||
		(p.Organization != "" && !identity.ValidID(p.Organization)) || len(p.Members) == 0 || len(p.Members) > 4096 {
		return fmt.Errorf("%w: capability policy identity", ErrInvalid)
	}
	seen := map[string]bool{}
	for _, member := range p.Members {
		if !identity.ValidID(member) || seen[member] {
			return fmt.Errorf("%w: capability policy membership", ErrInvalid)
		}
		seen[member] = true
	}
	if p.Organization == "" && len(p.Members) != 1 {
		return fmt.Errorf("%w: personal policy has one member", ErrInvalid)
	}
	for _, rules := range [][]CapabilityRule{p.Ceiling, p.Defaults} {
		if err := validateCapabilityRules(rules, false); err != nil {
			return err
		}
	}
	if err := validateCapabilityRules(p.Denies, true); err != nil {
		return err
	}
	if err := validateCapabilityGroups(p.DefaultGroups); err != nil {
		return err
	}
	for _, defaults := range policyDefaultRuleSets(p) {
		for _, rule := range defaults {
			if !slices.ContainsFunc(p.Ceiling, func(ceiling CapabilityRule) bool { return ruleInside(ceiling, rule) }) {
				return fmt.Errorf("%w: organization default exceeds ceiling", ErrUnauthorized)
			}
		}
	}
	if !validConfirmation(p.Confirmation, p, p.IssuedBy) {
		return fmt.Errorf("%w: unconfirmed capability policy", ErrUnauthorized)
	}
	return nil
}

func (p CapabilityProfile) Validate() error {
	if err := p.validateStructure(); err != nil {
		return err
	}
	if !validConfirmation(p.Confirmation, p, p.IssuedBy) {
		return fmt.Errorf("%w: unconfirmed capability profile", ErrUnauthorized)
	}
	return nil
}

// validateStructure checks everything about the record except the human
// confirmation stamp. Preview applies it to operator drafts before --confirm
// exists; publication still requires the full Validate.
func (p CapabilityProfile) validateStructure() error {
	if !validPolicyRecord(p.Schema, p.ProfileID, p.Revision, p.IssuedBy, p.Reason, p.IssuedAt, p.Status) ||
		!identity.ValidID(p.PolicyID) || !identity.ValidID(p.PrincipalID) || !identity.ValidID(p.ContextID) ||
		!identity.ValidID(p.RuntimeID) || !identity.ValidID(p.PolicyVersion) || p.PolicyRevision == 0 || p.Generation == 0 ||
		(p.Environment != "dev" && p.Environment != "prod") || len(p.Selections) > 1024 {
		return fmt.Errorf("%w: capability profile identity", ErrInvalid)
	}
	if err := validateCapabilityRules(p.Allows, false); err != nil {
		return err
	}
	if err := validateCapabilityRules(p.Denies, true); err != nil {
		return err
	}
	if err := validateCapabilityGroups(p.AllowGroups); err != nil {
		return err
	}
	names, capabilities := map[string]bool{}, map[string]bool{}
	for _, selection := range p.Selections {
		key := selection.CapabilityID + "\x00" + selection.ConnectionID
		if !capabilityIDPattern.MatchString(selection.CapabilityID) || !identity.ValidID(selection.DefinitionID) ||
			!versionPattern.MatchString(selection.DefinitionVersion) || !digestPattern.MatchString(selection.ImplementationDigest) ||
			validateProjectedName(selection.Name) != nil || !mcpToolNamePattern.MatchString(selection.ToolName) ||
			(selection.ConnectionID != "" && !identity.ValidID(selection.ConnectionID)) || names[selection.Name] || capabilities[key] || validControlOperation(selection.Name) {
			return fmt.Errorf("%w: ambiguous capability selection", ErrInvalid)
		}
		names[selection.Name], capabilities[key] = true, true
	}
	if len(p.ControlOperations) > len(ControlOperations) {
		return fmt.Errorf("%w: too many control operations", ErrInvalid)
	}
	seenOps := map[string]bool{}
	for _, operation := range p.ControlOperations {
		if !slices.Contains(ControlOperations, operation) || seenOps[operation] {
			return fmt.Errorf("%w: reviewed control operation", ErrInvalid)
		}
		seenOps[operation] = true
	}
	return nil
}

func (s *Store) PutCapabilityPolicy(policy CapabilityPolicy) error {
	if err := policy.Validate(); err != nil {
		return err
	}
	// Own slices/maps: an operator's later in-memory edits cannot mutate a grant.
	body, _ := json.Marshal(policy)
	var owned CapabilityPolicy
	_ = json.Unmarshal(body, &owned)
	policy = owned
	s.mu.Lock()
	defer s.mu.Unlock()
	old, exists := s.capabilityPolicies[policy.PolicyID]
	// ponytail: publication scans bounded-by-store history; index group revisions
	// only if operator policy histories become large enough to measure a slowdown.
	for _, change := range s.capabilityChanges {
		if change.Policy != nil && change.Policy.PolicyID == policy.PolicyID {
			if err := checkGroupRevisions(change.Policy.DefaultGroups, policy.DefaultGroups); err != nil {
				return err
			}
		}
	}
	if exists && policy.Revision <= old.Revision {
		if recordsEqual(old, policy) {
			if s.path != "" {
				return s.saveLocked(s.path)
			}
			return nil
		}
		return fmt.Errorf("%w: capability policy revision", ErrConflict)
	}
	s.capabilityPolicies[policy.PolicyID] = policy
	s.capabilityChanges = append(s.capabilityChanges, capabilityChange{Policy: &policy})
	if s.path != "" {
		if err := s.saveLocked(s.path); err != nil {
			s.capabilityChanges = s.capabilityChanges[:len(s.capabilityChanges)-1]
			if exists {
				s.capabilityPolicies[policy.PolicyID] = old
			} else {
				delete(s.capabilityPolicies, policy.PolicyID)
			}
			return err
		}
	}
	return nil
}

// validateProfileAdmissionLocked runs the authority checks a profile
// publication must pass against the current store: the named policy exists at
// the pinned revision and lists the principal, every allow stays inside the
// ceiling, and every selection pins a reviewed definition revision by digest.
// Preview runs the identical checks so a previewed profile is exactly what
// apply would admit.
func (s *Store) validateProfileAdmissionLocked(profile CapabilityProfile) (CapabilityPolicy, error) {
	policy, exists := s.capabilityPolicies[profile.PolicyID]
	if !exists || policy.Revision != profile.PolicyRevision || !slices.Contains(policy.Members, profile.PrincipalID) {
		return CapabilityPolicy{}, fmt.Errorf("%w: profile policy or membership", ErrUnauthorized)
	}
	for _, rules := range append([][]CapabilityRule{profile.Allows}, groupRuleSets(profile.AllowGroups)...) {
		for _, rule := range rules {
			if !slices.ContainsFunc(policy.Ceiling, func(ceiling CapabilityRule) bool { return ruleInside(ceiling, rule) }) {
				return CapabilityPolicy{}, fmt.Errorf("%w: profile allow exceeds ceiling", ErrUnauthorized)
			}
		}
	}
	for _, selection := range profile.Selections {
		definition, exists := s.definitions[definitionKey(selection.DefinitionID, selection.DefinitionVersion)]
		if !exists || DefinitionDigest(definition) != selection.ImplementationDigest || !slices.ContainsFunc(definition.Tools, func(tool ToolSpec) bool {
			return tool.Name == selection.ToolName && tool.CapabilityID == selection.CapabilityID && len(tool.Uses) > 0
		}) {
			return CapabilityPolicy{}, fmt.Errorf("%w: unreviewed capability implementation", ErrUnauthorized)
		}
	}
	return policy, nil
}

func (s *Store) PutCapabilityProfile(profile CapabilityProfile) error {
	if err := profile.Validate(); err != nil {
		return err
	}
	body, _ := json.Marshal(profile)
	var owned CapabilityProfile
	_ = json.Unmarshal(body, &owned)
	profile = owned
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, err := s.validateProfileAdmissionLocked(profile); err != nil {
		return err
	}
	old, exists := s.capabilityProfiles[profile.ProfileID]
	for _, change := range s.capabilityChanges {
		if change.Profile != nil && change.Profile.ProfileID == profile.ProfileID {
			if err := checkGroupRevisions(change.Profile.AllowGroups, profile.AllowGroups); err != nil {
				return err
			}
		}
	}
	if exists && profile.Revision <= old.Revision {
		if recordsEqual(old, profile) {
			if s.path != "" {
				return s.saveLocked(s.path)
			}
			return nil
		}
		return fmt.Errorf("%w: capability profile revision", ErrConflict)
	}
	s.capabilityProfiles[profile.ProfileID] = profile
	s.capabilityChanges = append(s.capabilityChanges, capabilityChange{Profile: &profile})
	if s.path != "" {
		if err := s.saveLocked(s.path); err != nil {
			s.capabilityChanges = s.capabilityChanges[:len(s.capabilityChanges)-1]
			if exists {
				s.capabilityProfiles[profile.ProfileID] = old
			} else {
				delete(s.capabilityProfiles, profile.ProfileID)
			}
			return err
		}
	}
	return nil
}

func (s *Store) capabilityProfileLocked(auth identity.Envelope) (CapabilityProfile, CapabilityPolicy, error) {
	if err := auth.Validate(auth.PrincipalID, auth.ContextID, auth.RuntimeID, auth.PolicyVersion); err != nil {
		return CapabilityProfile{}, CapabilityPolicy{}, fmt.Errorf("%w: capability identity", ErrUnauthorized)
	}
	profile, exists := s.capabilityProfiles[auth.CapabilityProfile]
	if !exists || profile.Status != ActiveStatus || profile.PrincipalID != auth.PrincipalID || profile.ContextID != auth.ContextID ||
		profile.RuntimeID != auth.RuntimeID || profile.Environment != auth.Environment || profile.Generation != auth.Generation || profile.PolicyVersion != auth.PolicyVersion {
		return CapabilityProfile{}, CapabilityPolicy{}, fmt.Errorf("%w: capability runtime scope", ErrUnauthorized)
	}
	policy, exists := s.capabilityPolicies[profile.PolicyID]
	if !exists || policy.Status != ActiveStatus || policy.Revision != profile.PolicyRevision || !slices.Contains(policy.Members, auth.PrincipalID) {
		return CapabilityProfile{}, CapabilityPolicy{}, fmt.Errorf("%w: current capability policy", ErrUnauthorized)
	}
	return profile, policy, nil
}

func validateToolCapability(tool ToolSpec) error {
	if tool.CapabilityID == "" && len(tool.Uses) == 0 && len(tool.ArgumentEquals) == 0 {
		return nil
	}
	if !capabilityIDPattern.MatchString(tool.CapabilityID) || len(tool.Uses) == 0 || len(tool.Uses) > 16 || len(tool.ArgumentEquals) > 32 {
		return fmt.Errorf("%w: reviewed tool capability", ErrInvalid)
	}
	for _, use := range tool.Uses {
		if !identity.ValidID(use.Action) || !identity.ValidID(use.Resource) || (use.PathArgument != "" && !mcpToolNamePattern.MatchString(use.PathArgument)) {
			return fmt.Errorf("%w: reviewed capability action/resource", ErrInvalid)
		}
	}
	for key, value := range tool.ArgumentEquals {
		if !mcpToolNamePattern.MatchString(key) || len(value) > 1024 || strings.ContainsAny(value, "\x00\r\n") {
			return fmt.Errorf("%w: fixed capability argument", ErrInvalid)
		}
	}
	return nil
}

// CapabilityScope records one path-grant boundary an admitted call must honor:
// the resource domain, the argument carrying the path and the granted prefix.
// Executors translate it into an os.Root boundary so a handler bug cannot
// widen a grant into a traversal. It serializes only on the private executor
// channels (tools-exec, exec-pack); EffectiveBinding keeps it out of persisted
// projections via its own json:"-" field.
type CapabilityScope struct {
	Resource     string `json:"resource"`
	PathArgument string `json:"path_argument,omitempty"`
	PathPrefix   string `json:"path_prefix"`
}

func capabilityDecision(profile CapabilityProfile, policy CapabilityPolicy, selection CapabilitySelection, tool ToolSpec, arguments map[string]any, projection bool) (CapabilityLimits, []CapabilityScope, bool) {
	if !projection {
		for key, value := range tool.ArgumentEquals {
			if got, ok := arguments[key].(string); !ok || got != value {
				return CapabilityLimits{}, nil, false
			}
		}
	}
	limits := CapabilityLimits{MaxOutputBytes, MaxExecutionTimeout}
	var scopes []CapabilityScope
	for _, use := range tool.Uses {
		tuple := CapabilityRule{CapabilityID: selection.CapabilityID, ImplementationDigest: selection.ImplementationDigest,
			Action: use.Action, Resource: use.Resource, ConnectionID: selection.ConnectionID}
		var actual *string
		if !projection || use.PathArgument == "" {
			value := ""
			if use.PathArgument != "" {
				var ok bool
				value, ok = arguments[use.PathArgument].(string)
				if !ok || !validCapabilityPath(value) {
					return CapabilityLimits{}, nil, false
				}
			}
			actual = &value
		}
		bound, prefix, allowed := capabilityRuleDecision(profile, policy, tuple, actual, time.Now())
		if !allowed {
			return CapabilityLimits{}, nil, false
		}
		if use.PathArgument != "" {
			scopes = append(scopes, CapabilityScope{Resource: use.Resource, PathArgument: use.PathArgument, PathPrefix: prefix})
		}
		limits.OutputBytes = min(limits.OutputBytes, bound.OutputBytes)
		limits.TimeoutSeconds = min(limits.TimeoutSeconds, bound.TimeoutSeconds)
	}
	return limits, scopes, len(tool.Uses) > 0
}

func (s *Store) managedToolsLocked(auth identity.Envelope) ([]ProjectedTool, error) {
	profile, policy, err := s.capabilityProfileLocked(auth)
	if err != nil {
		return nil, err
	}
	result := []ProjectedTool{}
	for _, selection := range profile.Selections {
		projected, _, err := s.managedSelectionLocked(auth, profile, policy, selection, nil, true)
		if err == nil {
			result = append(result, projected)
		} else if err != ErrUnauthorized {
			return nil, err
		}
	}
	seen := make(map[string]bool, len(result))
	for _, tool := range result {
		seen[tool.Name] = true
	}
	for _, tool := range s.managedSelfInstallToolsLocked(auth, profile) {
		if seen[tool.Name] {
			continue
		}
		seen[tool.Name] = true
		result = append(result, tool)
	}
	slices.SortFunc(result, func(a, b ProjectedTool) int { return strings.Compare(a.Name, b.Name) })
	return result, nil
}

// managedSelfInstallsLocked returns the active bindings this principal
// materialized through the self-install pipeline under the current managed
// policy generation. Provenance is the onboarding record: it carries the
// authenticated confirmation the binding was enabled under, so bindings from
// before the managed era (different policy_version) never leak in.
func (s *Store) managedSelfInstallsLocked(auth identity.Envelope, profile CapabilityProfile) map[string]bool {
	if !profile.SelfInstall {
		return nil
	}
	admitted := map[string]bool{}
	for _, onboarding := range s.onboardings {
		if onboarding.Mode != OnboardingSelfInstall || onboarding.BindingID == "" ||
			onboarding.PrincipalID != auth.PrincipalID || onboarding.ContextID != auth.ContextID ||
			onboarding.RuntimeID != auth.RuntimeID || onboarding.PolicyVersion != auth.PolicyVersion {
			continue
		}
		if onboarding.Phase != PhaseConfirmed && onboarding.Phase != PhaseEnabled && onboarding.Phase != PhaseDisabled {
			continue
		}
		admitted[onboarding.BindingID] = true
	}
	return admitted
}

func (s *Store) managedSelfInstallToolsLocked(auth identity.Envelope, profile CapabilityProfile) []ProjectedTool {
	admitted := s.managedSelfInstallsLocked(auth, profile)
	if len(admitted) == 0 {
		return nil
	}
	result := []ProjectedTool{}
	for _, binding := range s.bindings {
		if !admitted[binding.ToolBindingID] || binding.Status != ActiveStatus {
			continue
		}
		effective, err := s.resolveLocked(auth, binding.ToolBindingID)
		if err != nil {
			continue
		}
		effective.CapabilityProfileID, effective.CapabilityProfileRevision = profile.ProfileID, profile.Revision
		effective.CapabilityPolicyID, effective.CapabilityPolicyRevision = profile.PolicyID, profile.PolicyRevision
		effective.ImplementationDigest = DefinitionDigest(effective.Definition)
		for _, tool := range effective.Definition.Tools {
			result = append(result, ProjectedTool{
				Name:         ProjectedToolName(effective.Definition.DefinitionID, effective.Definition.Version, tool.Name),
				BindingID:    effective.Binding.ToolBindingID,
				DefinitionID: effective.Definition.DefinitionID,
				Version:      effective.Definition.Version,
				Tool:         tool,
			})
		}
	}
	return result
}

// managedSelfInstallToolLocked is the dispatch side of
// managedSelfInstallToolsLocked: same provenance gate, exact projected name.
func (s *Store) managedSelfInstallToolLocked(auth identity.Envelope, profile CapabilityProfile, name string) (ProjectedTool, EffectiveBinding, error) {
	admitted := s.managedSelfInstallsLocked(auth, profile)
	for _, binding := range s.bindings {
		if !admitted[binding.ToolBindingID] || binding.Status != ActiveStatus {
			continue
		}
		effective, err := s.resolveLocked(auth, binding.ToolBindingID)
		if err != nil {
			continue
		}
		for _, tool := range effective.Definition.Tools {
			if ProjectedToolName(effective.Definition.DefinitionID, effective.Definition.Version, tool.Name) != name {
				continue
			}
			effective.CapabilityProfileID, effective.CapabilityProfileRevision = profile.ProfileID, profile.Revision
			effective.CapabilityPolicyID, effective.CapabilityPolicyRevision = profile.PolicyID, profile.PolicyRevision
			effective.ImplementationDigest = DefinitionDigest(effective.Definition)
			return ProjectedTool{Name: name, BindingID: binding.ToolBindingID, DefinitionID: effective.Definition.DefinitionID,
				Version: effective.Definition.Version, Tool: tool}, effective, nil
		}
	}
	return ProjectedTool{}, EffectiveBinding{}, fmt.Errorf("%w: projected tool", ErrNotFound)
}

// ProfilePreviewEntry is one selection's dispatch outcome in a preview: either
// the admitted projection surface (scopes and tightened limits) or the reason
// the same evaluator would deny it. Quarantined entries stay visible so an
// operator never publishes a draft believing a dead selection is live.
type ProfilePreviewEntry struct {
	Name           string            `json:"name"`
	CapabilityID   string            `json:"capability_id"`
	Admitted       bool              `json:"admitted"`
	Reason         string            `json:"reason,omitempty"`
	Scopes         []CapabilityScope `json:"scopes,omitempty"`
	OutputBytes    int               `json:"output_bytes,omitempty"`
	TimeoutSeconds int               `json:"timeout_seconds,omitempty"`
}

// ProfilePreview is the human-reviewable old-to-new capability diff CP-10
// requires before a migration or profile change is applied.
type ProfilePreview struct {
	ProfileID         string                `json:"profile_id"`
	CurrentRevision   uint64                `json:"current_revision"`
	CandidateRevision uint64                `json:"candidate_revision"`
	Current           []ProfilePreviewEntry `json:"current"`
	Candidate         []ProfilePreviewEntry `json:"candidate"`
	Added             []string              `json:"added"`
	Removed           []string              `json:"removed"`
	Changed           []string              `json:"changed"`
}

// PreviewCapabilityProfile diffs a candidate profile against the stored one
// using the exact evaluator and admission checks apply and dispatch run, so a
// previewed grant can never diverge from what publish+dispatch would do. The
// draft needs only structural validity; confirmation remains the apply gate.
func (s *Store) PreviewCapabilityProfile(candidate CapabilityProfile) (ProfilePreview, error) {
	if err := candidate.validateStructure(); err != nil {
		return ProfilePreview{}, err
	}
	body, _ := json.Marshal(candidate)
	var owned CapabilityProfile
	_ = json.Unmarshal(body, &owned)
	candidate = owned
	s.mu.RLock()
	defer s.mu.RUnlock()
	preview := ProfilePreview{ProfileID: candidate.ProfileID, CandidateRevision: candidate.Revision,
		Current: []ProfilePreviewEntry{}, Candidate: []ProfilePreviewEntry{},
		Added: []string{}, Removed: []string{}, Changed: []string{}}
	candidatePolicy, err := s.validateProfileAdmissionLocked(candidate)
	if err != nil {
		return preview, err
	}
	if current, ok := s.capabilityProfiles[candidate.ProfileID]; ok {
		if candidate.Revision <= current.Revision {
			return preview, fmt.Errorf("%w: capability profile revision", ErrConflict)
		}
		preview.CurrentRevision = current.Revision
		if current.Status == ActiveStatus {
			// The old side may pin a superseded policy revision (e.g. the
			// operator already published the new policy). Evaluate it under
			// its pinned revision from history so the diff shows what the
			// old profile actually admitted, not an empty set.
			if currentPolicy, ok := s.policyAtRevisionLocked(current.PolicyID, current.PolicyRevision); ok {
				preview.Current = s.previewSelectionsLocked(current, currentPolicy)
			}
		}
	}
	preview.Candidate = s.previewSelectionsLocked(candidate, candidatePolicy)
	currentByName := map[string]ProfilePreviewEntry{}
	for _, entry := range preview.Current {
		if entry.Admitted {
			currentByName[entry.Name] = entry
		}
	}
	candidateByName := map[string]ProfilePreviewEntry{}
	for _, entry := range preview.Candidate {
		if entry.Admitted {
			candidateByName[entry.Name] = entry
		}
	}
	for name, next := range candidateByName {
		prev, ok := currentByName[name]
		if !ok {
			preview.Added = append(preview.Added, name)
		} else if !previewEntriesEqual(prev, next) {
			preview.Changed = append(preview.Changed, name)
		}
		delete(currentByName, name)
	}
	for name := range currentByName {
		preview.Removed = append(preview.Removed, name)
	}
	slices.Sort(preview.Added)
	slices.Sort(preview.Removed)
	slices.Sort(preview.Changed)
	return preview, nil
}

// previewSelectionsLocked evaluates every selection exactly as projection
// does: same synthesized identity, same per-selection resolution, same
// deny-reason surfacing.
func (s *Store) previewSelectionsLocked(profile CapabilityProfile, policy CapabilityPolicy) []ProfilePreviewEntry {
	auth := identity.Envelope{Schema: identity.Schema, PrincipalID: profile.PrincipalID, ContextID: profile.ContextID, RuntimeID: profile.RuntimeID,
		ExternalIdentityID: profile.ContextID, ConversationID: profile.ContextID, DeliveryTargetID: profile.ContextID,
		PolicyVersion: profile.PolicyVersion, CapabilityProfile: profile.ProfileID, Environment: profile.Environment, Generation: profile.Generation}
	entries := []ProfilePreviewEntry{}
	for _, selection := range profile.Selections {
		entry := ProfilePreviewEntry{Name: selection.Name, CapabilityID: selection.CapabilityID}
		_, effective, err := s.managedSelectionLocked(auth, profile, policy, selection, nil, true)
		if err != nil {
			entry.Reason = "unauthorized"
			if errors.Is(err, ErrConflict) {
				entry.Reason = "ambiguous binding"
			}
		} else {
			entry.Admitted = true
			entry.Scopes = effective.CapabilityScopes
			entry.OutputBytes = effective.Definition.Execution.OutputBytes
			entry.TimeoutSeconds = effective.Definition.Execution.TimeoutSeconds
		}
		entries = append(entries, entry)
	}
	return entries
}

// policyAtRevisionLocked resolves a policy at an exact revision: the live
// record when it still matches, else the pinned historical revision replayed
// out of capability history. Preview uses it for the old side of a diff.
func (s *Store) policyAtRevisionLocked(policyID string, revision uint64) (CapabilityPolicy, bool) {
	if policy, ok := s.capabilityPolicies[policyID]; ok && policy.Revision == revision {
		return policy, true
	}
	for _, change := range s.capabilityChanges {
		if change.Policy != nil && change.Policy.PolicyID == policyID && change.Policy.Revision == revision {
			return *change.Policy, true
		}
	}
	return CapabilityPolicy{}, false
}

func previewEntriesEqual(a, b ProfilePreviewEntry) bool {
	return a.OutputBytes == b.OutputBytes && a.TimeoutSeconds == b.TimeoutSeconds && slices.EqualFunc(a.Scopes, b.Scopes, func(x, y CapabilityScope) bool { return x == y })
}

func (s *Store) managedSelectionLocked(auth identity.Envelope, profile CapabilityProfile, policy CapabilityPolicy, selection CapabilitySelection, arguments map[string]any, projection bool) (ProjectedTool, EffectiveBinding, error) {
	var found *EffectiveBinding
	for _, binding := range s.bindings {
		if binding.DefinitionID != selection.DefinitionID || binding.DefinitionVersion != selection.DefinitionVersion || binding.ConnectionID != selection.ConnectionID {
			continue
		}
		effective, err := s.resolveLocked(auth, binding.ToolBindingID)
		if err != nil {
			continue
		}
		if found != nil {
			return ProjectedTool{}, EffectiveBinding{}, fmt.Errorf("%w: ambiguous capability binding", ErrConflict)
		}
		found = &effective
	}
	if found == nil || DefinitionDigest(found.Definition) != selection.ImplementationDigest {
		return ProjectedTool{}, EffectiveBinding{}, ErrUnauthorized
	}
	for _, tool := range found.Definition.Tools {
		if tool.Name != selection.ToolName || tool.CapabilityID != selection.CapabilityID {
			continue
		}
		limits, scopes, allowed := capabilityDecision(profile, policy, selection, tool, arguments, projection)
		if !allowed {
			break
		}
		found.Definition.Execution.OutputBytes = min(found.Definition.Execution.OutputBytes, limits.OutputBytes)
		found.Definition.Execution.TimeoutSeconds = min(found.Definition.Execution.TimeoutSeconds, limits.TimeoutSeconds)
		found.CapabilityID, found.CapabilityPolicyRevision, found.CapabilityProfileRevision = selection.CapabilityID, policy.Revision, profile.Revision
		found.CapabilityPolicyID, found.CapabilityProfileID = policy.PolicyID, profile.ProfileID
		found.ImplementationDigest = selection.ImplementationDigest
		found.CapabilityScopes = scopes
		if projection {
			// Allowed-only description: the model sees the admitted boundary,
			// never the full catalog surface this tool could serve elsewhere.
			for _, scope := range scopes {
				if scope.PathPrefix == "" {
					continue
				}
				tool.Description = strings.TrimSpace(tool.Description + " Scoped to " + scope.Resource + " under " + scope.PathPrefix + ".")
			}
		}
		return ProjectedTool{Name: selection.Name, BindingID: found.Binding.ToolBindingID, DefinitionID: selection.DefinitionID, Version: selection.DefinitionVersion, Tool: tool}, *found, nil
	}
	return ProjectedTool{}, EffectiveBinding{}, ErrUnauthorized
}

// ReverifyEffective rechecks the authority a call was admitted under. Export
// and apply paths run this so a mid-flight revocation, binding update or
// policy/profile revision change stops the write instead of landing on
// stale authority.
func (s *Store) ReverifyEffective(effective EffectiveBinding) error {
	s.mu.RLock()
	defer s.mu.RUnlock()
	binding, ok := s.bindings[effective.Binding.ToolBindingID]
	if !ok || binding.Status != ActiveStatus || binding.Revision != effective.Binding.Revision || binding.ProjectionRevision != effective.Binding.ProjectionRevision {
		return fmt.Errorf("%w: binding revoked or changed", ErrUnauthorized)
	}
	profile, ok := s.capabilityProfiles[effective.CapabilityProfileID]
	if !ok || profile.Status != ActiveStatus || profile.Revision != effective.CapabilityProfileRevision {
		return fmt.Errorf("%w: capability profile changed", ErrUnauthorized)
	}
	policy, ok := s.capabilityPolicies[profile.PolicyID]
	if !ok || policy.Status != ActiveStatus || policy.Revision != effective.CapabilityPolicyRevision {
		return fmt.Errorf("%w: capability policy changed", ErrUnauthorized)
	}
	return nil
}

func (s *Store) managedToolLocked(auth identity.Envelope, name string, arguments map[string]any, projection bool) (ProjectedTool, EffectiveBinding, error) {
	profile, policy, err := s.capabilityProfileLocked(auth)
	if err != nil {
		return ProjectedTool{}, EffectiveBinding{}, err
	}
	for _, selection := range profile.Selections {
		if selection.Name == name {
			tool, effective, err := s.managedSelectionLocked(auth, profile, policy, selection, arguments, projection)
			if err != ErrUnauthorized {
				return tool, effective, err
			}
			break
		}
	}
	return s.managedSelfInstallToolLocked(auth, profile, name)
}

// capabilityRuleDecision returns one allow/ceiling intersection. Never merge
// unrelated rules' actions, resources, connections, expiry or execution limits.
// Projection asks whether a permitted path exists; dispatch checks the actual path.
func policyDefaultRuleSets(policy CapabilityPolicy) [][]CapabilityRule {
	return append([][]CapabilityRule{policy.Defaults}, groupRuleSets(policy.DefaultGroups)...)
}

func groupRuleSets(groups []CapabilityGroup) [][]CapabilityRule {
	sets := make([][]CapabilityRule, 0, len(groups))
	for _, group := range groups {
		sets = append(sets, group.Members)
	}
	return sets
}

func allowedRuleSets(profile CapabilityProfile, policy CapabilityPolicy) [][]CapabilityRule {
	sets := policyDefaultRuleSets(policy)
	sets = append(sets, profile.Allows)
	sets = append(sets, groupRuleSets(profile.AllowGroups)...)
	return sets
}

func capabilityRuleDecision(profile CapabilityProfile, policy CapabilityPolicy, tuple CapabilityRule, actualPath *string, now time.Time) (CapabilityLimits, string, bool) {
	active := func(rule CapabilityRule) bool { return rule.ExpiresAt.IsZero() || now.Before(rule.ExpiresAt) }
	for _, allows := range allowedRuleSets(profile, policy) {
		for _, allow := range allows {
			if !active(allow) || !sameCapabilityTuple(allow, tuple) {
				continue
			}
			for _, ceiling := range policy.Ceiling {
				if !active(ceiling) || !sameCapabilityTuple(allow, ceiling) {
					continue
				}
				prefix := allow.PathPrefix
				if prefixContains(prefix, ceiling.PathPrefix) {
					prefix = ceiling.PathPrefix
				} else if !prefixContains(ceiling.PathPrefix, prefix) {
					continue
				}
				candidate := prefix
				if actualPath != nil {
					candidate = *actualPath
					if !prefixContains(prefix, candidate) {
						continue
					}
				}
				denied := false
				for _, denies := range [][]CapabilityRule{policy.Denies, profile.Denies} {
					for _, deny := range denies {
						if active(deny) && sameCapabilityTuple(deny, tuple) && prefixContains(deny.PathPrefix, candidate) {
							denied = true
						}
					}
				}
				if !denied {
					return CapabilityLimits{min(allow.Limits.OutputBytes, ceiling.Limits.OutputBytes), min(allow.Limits.TimeoutSeconds, ceiling.Limits.TimeoutSeconds)}, prefix, true
				}
			}
		}
	}
	return CapabilityLimits{}, "", false
}
