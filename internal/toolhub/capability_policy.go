package toolhub

import (
	"fmt"
	"github.com/letya999/hermes-hub/internal/identity"
	"path"
	"regexp"
	"slices"
	"strings"
	"time"
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
		values := []string{""}
		if !projection && use.PathArgument != "" {
			switch value := arguments[use.PathArgument].(type) {
			case nil:
				if _, present := arguments[use.PathArgument]; present {
					return CapabilityLimits{}, nil, false
				}
			case string:
				values = []string{value}
			case []any:
				values = nil
				for _, item := range value {
					v, ok := item.(string)
					if !ok {
						return CapabilityLimits{}, nil, false
					}
					values = append(values, v)
				}
			case []string:
				values = value
			default:
				return CapabilityLimits{}, nil, false
			}
			if len(values) == 0 || len(values) > 8192 {
				return CapabilityLimits{}, nil, false
			}
		}
		for _, value := range values {
			if value == "." {
				value = ""
			}
			var actual *string
			if !projection || use.PathArgument == "" {
				actual = &value
			}
			if actual != nil && !validCapabilityPath(value) {
				return CapabilityLimits{}, nil, false
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
	}
	return limits, scopes, len(tool.Uses) > 0
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

func previewEntriesEqual(a, b ProfilePreviewEntry) bool {
	return a.OutputBytes == b.OutputBytes && a.TimeoutSeconds == b.TimeoutSeconds && slices.EqualFunc(a.Scopes, b.Scopes, func(x, y CapabilityScope) bool { return x == y })
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
