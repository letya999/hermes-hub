package api

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
	RightCeiling  []RightRule       `json:"right_ceiling,omitempty"`
	Defaults      []CapabilityRule  `json:"defaults"`
	DefaultGroups []CapabilityGroup `json:"default_groups,omitempty"`
	Denies        []CapabilityRule  `json:"denies,omitempty"`
	Confirmation  *Confirmation     `json:"confirmation,omitempty"`
}

type CapabilitySelection struct {
	CapabilityID         string   `json:"capability_id"`
	DefinitionID         string   `json:"definition_id"`
	DefinitionVersion    string   `json:"definition_version"`
	ImplementationDigest string   `json:"implementation_digest"`
	ToolName             string   `json:"tool_name"`
	Name                 string   `json:"name"`
	ConnectionID         string   `json:"connection_id,omitempty"`
	PathScopes           []string `json:"path_scopes,omitempty"`
	// Toolset is the authored tools.yaml set the selection came from
	// (`browser`, `github`, …) — the operator vocabulary, not a capability
	// axis. Empty for selections appended outside a passport apply.
	Toolset string `json:"toolset,omitempty"`
}

// ProfileToolset is one authored tools.yaml set carried on the published
// profile so the inventory can answer "which toolsets are selected" with
// live admitted counts instead of inferred name-prefix groups.
type ProfileToolset struct {
	Name      string `json:"name"`
	Placement string `json:"placement"` // "core" | "extended"
	Via       string `json:"via"`
	// Access/Include/Exclude carry the authored connector filter so a pending
	// (unbound) selection can be materialized when a binding activates —
	// without re-reading the passport. Empty means unfiltered.
	Access  string   `json:"access,omitempty"`
	Include []string `json:"include,omitempty"`
	Exclude []string `json:"exclude,omitempty"`
}

// A profile is published by the host control path for an exact runtime
// generation. An absent profile or policy never enables legacy grants.
type CapabilityProfile struct {
	Schema           int                   `json:"schema"`
	ProfileID        string                `json:"profile_id"`
	PrincipalID      string                `json:"principal_id"`
	ContextID        string                `json:"context_id"`
	RuntimeID        string                `json:"runtime_id"`
	Environment      string                `json:"environment"`
	Generation       uint64                `json:"generation"`
	PolicyVersion    string                `json:"policy_version"`
	PassportRevision string                `json:"passport_revision,omitempty"`
	PolicyID         string                `json:"policy_id"`
	PolicyRevision   uint64                `json:"policy_revision"`
	Revision         uint64                `json:"revision"`
	IssuedBy         string                `json:"issued_by"`
	IssuedAt         time.Time             `json:"issued_at"`
	Reason           string                `json:"reason"`
	Status           Status                `json:"status"`
	Selections       []CapabilitySelection `json:"selections"`
	Allows           []CapabilityRule      `json:"allows,omitempty"`
	AllowGroups      []CapabilityGroup     `json:"allow_groups,omitempty"`
	Denies           []CapabilityRule      `json:"denies,omitempty"`
	// ControlOperations is the reviewed allowlist of control-plane operations
	// the managed principal's runtime token may invoke. Names must come from
	// ControlOperations; absent means none, and legacy grants never leak in.
	ControlOperations []string `json:"control_operations,omitempty"`
	// SelfInstall admits the GitHub-source prepare path for this principal.
	// Installation stays a distinct grant (SPEC-0041 CP-05): the pipeline
	// still requires the single-use human confirmation and enable phases.
	SelfInstall bool `json:"self_install,omitempty"`
	// Connectors admits the unified connector_* tool surface: "" keeps the
	// legacy per-operation grants projection, "none" hides connector control
	// entirely, "personal" projects the nine connector_* tools over the
	// principal's own connections, "personal+team" additionally allows
	// lifecycle on team connections where the caller is a member.
	Connectors string `json:"connectors,omitempty"`
	// Rights is the compiled grants.yaml allow flattened to
	// capability.operation ceiling rows (SPEC-0092 R2): the PDP refuses any
	// right the published revision never carried, and live ledger grants can
	// only narrow inside it.
	Rights []RightRule `json:"rights,omitempty"`
	// CoreTools is the compiled revision's direct-schema allowlist (ADR-0092
	// D6, R6.1): only these selected names get a projected MCP schema; the
	// rest of the admitted surface stays callable through tools_list/invoke.
	// Empty keeps the legacy all-projected posture for unmanaged bindings.
	CoreBound bool     `json:"core_bound,omitempty"`
	CoreTools []string `json:"core_tools,omitempty"`
	// Toolsets is the authored selection the published revision compiled:
	// placement and via per set, membership resolved through
	// Selection.Toolset. Recomputed per call by the inventory.
	Toolsets     []ProfileToolset `json:"toolsets,omitempty"`
	Confirmation *Confirmation    `json:"confirmation,omitempty"`
}

// Connector capability bundle values for CapabilityProfile.Connectors.
const (
	ConnectorsNone         = "none"
	ConnectorsPersonal     = "personal"
	ConnectorsPersonalTeam = "personal+team"
)

// ConnectorTools is the complete nine-tool connector_* surface, in the order
// the issue defines it.
var ConnectorTools = []string{
	"connector_search", "connector_install", "connector_list",
	"connector_status", "connector_enable", "connector_disable",
	"connector_allow_writes", "connector_reauth", "connector_remove",
}

// Policy history is the persisted authority. Current maps are rebuilt from it,
// so a grant and its issuer/scope/revision record cannot commit separately.
// The registry's existing atomic snapshot and stale-writer fence cover both.
type CapabilityChange struct {
	Policy  *CapabilityPolicy  `json:"policy,omitempty"`
	Profile *CapabilityProfile `json:"profile,omitempty"`
	Grant   *Grant             `json:"grant,omitempty"`
}

func validConfirmation(confirmation *Confirmation, record any, issuer string) bool {
	return confirmation != nil && confirmation.IssuedBy == issuer &&
		confirmation.Digest != "" && confirmation.Digest == ConfirmationDigest(record) &&
		!confirmation.ConfirmedAt.IsZero() && confirmation.ConfirmedAt.Year() <= 9999
}

func ValidCapabilityPath(value string) bool {
	return value == "" || (len(value) <= 4096 && value != "." && !strings.HasPrefix(value, "/") &&
		!strings.ContainsAny(value, "\\:\x00\r\n") && path.Clean(value) == value &&
		value != ".." && !strings.HasPrefix(value, "../"))
}

func ValidSelectionPath(value string) bool {
	return len(value) <= 4096 && strings.HasPrefix(value, "/") && value != "/" &&
		!strings.ContainsAny(value, "\\:\x00\r\n") && path.Clean(value) == value
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
		if !capabilityIDPattern.MatchString(rule.CapabilityID) || !DigestPattern.MatchString(rule.ImplementationDigest) ||
			!identity.ValidID(rule.Action) || !identity.ValidID(rule.Resource) || !ValidCapabilityPath(rule.PathPrefix) ||
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

func CheckGroupRevisions(previous, next []CapabilityGroup) error {
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

func RuleInside(outer, inner CapabilityRule) bool {
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
	if len(p.RightCeiling) > 4096 {
		return fmt.Errorf("%w: too many rights in policy ceiling", ErrInvalid)
	}
	rights := map[RightRule]bool{}
	for _, right := range p.RightCeiling {
		if !rightNamePattern.MatchString(right.Capability) || !rightNamePattern.MatchString(right.Operation) || rights[right] {
			return fmt.Errorf("%w: policy right ceiling", ErrInvalid)
		}
		rights[right] = true
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
			if !slices.ContainsFunc(p.Ceiling, func(ceiling CapabilityRule) bool { return RuleInside(ceiling, rule) }) {
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
	if err := p.ValidateStructure(); err != nil {
		return err
	}
	if !validConfirmation(p.Confirmation, p, p.IssuedBy) {
		return fmt.Errorf("%w: unconfirmed capability profile", ErrUnauthorized)
	}
	return nil
}

// ValidateStructure checks everything about the record except the human
// confirmation stamp. Preview applies it to operator drafts before --confirm
// exists; publication still requires the full Validate.
func (p CapabilityProfile) ValidateStructure() error {
	if !validPolicyRecord(p.Schema, p.ProfileID, p.Revision, p.IssuedBy, p.Reason, p.IssuedAt, p.Status) ||
		!identity.ValidID(p.PolicyID) || !identity.ValidID(p.PrincipalID) || !identity.ValidID(p.ContextID) ||
		!identity.ValidID(p.RuntimeID) || !identity.ValidID(p.PolicyVersion) || p.PolicyRevision == 0 || p.Generation == 0 ||
		(p.Environment != "dev" && p.Environment != "prod") || len(p.Selections) > 1024 {
		return fmt.Errorf("%w: capability profile identity", ErrInvalid)
	}
	if p.PassportRevision != "" && !identity.ValidID(p.PassportRevision) {
		return fmt.Errorf("%w: capability passport revision", ErrInvalid)
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
			!VersionPattern.MatchString(selection.DefinitionVersion) || !DigestPattern.MatchString(selection.ImplementationDigest) ||
			ValidateProjectedName(selection.Name) != nil || !McpToolNamePattern.MatchString(selection.ToolName) ||
			(selection.ConnectionID != "" && !identity.ValidID(selection.ConnectionID)) || names[selection.Name] || capabilities[key] || ValidControlOperation(selection.Name) {
			return fmt.Errorf("%w: ambiguous capability selection", ErrInvalid)
		}
		for _, scope := range selection.PathScopes {
			if !ValidSelectionPath(scope) {
				return fmt.Errorf("%w: invalid selected path scope", ErrInvalid)
			}
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
	if len(p.CoreTools) > 20 {
		return fmt.Errorf("%w: core projection over the tool budget", ErrInvalid)
	}
	coreSeen := map[string]bool{}
	for _, name := range p.CoreTools {
		if !names[name] || coreSeen[name] {
			return fmt.Errorf("%w: core tool outside the admitted selections", ErrInvalid)
		}
		coreSeen[name] = true
	}
	switch p.Connectors {
	case "", ConnectorsNone, ConnectorsPersonal, ConnectorsPersonalTeam:
	default:
		return fmt.Errorf("%w: connectors bundle", ErrInvalid)
	}
	return nil
}

// RemoteExecutor reports whether the executor runs the call behind the PEP
// outside the in-runtime handler channel: connector:<name>, flow:<name> or
// runtime:<api> (ADR-0094 D2). Remote tools carry no path scopes; their
// admission is the selection plus the declared `requires` rights.
func RemoteExecutor(executor Executor) bool {
	return strings.HasPrefix(executor, "connector:") ||
		strings.HasPrefix(executor, "flow:") ||
		strings.HasPrefix(executor, "runtime:")
}

func validateToolCapability(tool ToolSpec) error {
	if tool.CapabilityID == "" && len(tool.Uses) == 0 && len(tool.ArgumentEquals) == 0 {
		return nil
	}
	// Remote executors (connector:, flow:, runtime:) carry no capability-use
	// path scopes: their calls never reach an os.Root boundary, so a zero
	// Uses list is legal; handler: tools keep requiring ≥1 use.
	remote := RemoteExecutor(tool.Executor)
	if !capabilityIDPattern.MatchString(tool.CapabilityID) || (!remote && len(tool.Uses) == 0) || len(tool.Uses) > 16 || len(tool.ArgumentEquals) > 32 {
		return fmt.Errorf("%w: reviewed tool capability", ErrInvalid)
	}
	for _, use := range tool.Uses {
		if !identity.ValidID(use.Action) || !identity.ValidID(use.Resource) || (use.PathArgument != "" && !McpToolNamePattern.MatchString(use.PathArgument)) {
			return fmt.Errorf("%w: reviewed capability action/resource", ErrInvalid)
		}
	}
	for key, value := range tool.ArgumentEquals {
		if !McpToolNamePattern.MatchString(key) || len(value) > 1024 || strings.ContainsAny(value, "\x00\r\n") {
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

func CapabilityDecision(profile CapabilityProfile, policy CapabilityPolicy, selection CapabilitySelection, tool ToolSpec, arguments map[string]any, projection bool) (CapabilityLimits, []CapabilityScope, bool) {
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
			if actual != nil && !ValidCapabilityPath(value) {
				return CapabilityLimits{}, nil, false
			}
			bound, prefix, allowed := CapabilityRuleDecision(profile, policy, tuple, actual, time.Now())
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
	return limits, scopes, len(tool.Uses) > 0 || RemoteExecutor(tool.Executor)
}

// NarrowSelectionScopes intersects authored paths with the admitted policy
// prefixes before dispatch. The resulting prefix also reaches the executor's
// os.Root guard, so a symlink cannot escape a lexical argument check.
func NarrowSelectionScopes(selection CapabilitySelection, tool ToolSpec, arguments map[string]any, admitted []CapabilityScope, projection bool) ([]CapabilityScope, bool) {
	if len(selection.PathScopes) == 0 {
		return admitted, true
	}
	if tool.Executor != "handler:files" || len(tool.Uses) == 0 {
		return nil, false
	}
	for _, use := range tool.Uses {
		if use.PathArgument == "" {
			return nil, false
		}
	}
	if len(admitted) == 0 {
		return nil, false
	}
	if projection {
		return admitted, true
	}
	root := "workspace"
	if value, present := arguments["root"]; present {
		name, ok := value.(string)
		if !ok {
			return nil, false
		}
		if name != "" {
			root = name
		}
	}
	switch root {
	case "workspace", "archive", "organization":
	default:
		return nil, false
	}
	out := slices.Clone(admitted)
	byArgument := map[string]string{}
	for i, scope := range out {
		var values []string
		switch value := arguments[scope.PathArgument].(type) {
		case nil:
			values = []string{""}
		case string:
			values = []string{value}
		case []string:
			values = value
		case []any:
			for _, item := range value {
				v, ok := item.(string)
				if !ok {
					return nil, false
				}
				values = append(values, v)
			}
		default:
			return nil, false
		}
		if len(values) == 0 {
			return nil, false
		}
		chosen := ""
		selected := false
		for _, value := range values {
			if value == "." {
				value = ""
			}
			if !ValidCapabilityPath(value) {
				return nil, false
			}
			matched := false
			valuePrefix := ""
			for _, allowed := range selection.PathScopes {
				base := "/" + root
				if allowed != base && !strings.HasPrefix(allowed, base+"/") {
					continue
				}
				prefix := strings.TrimPrefix(strings.TrimPrefix(allowed, base), "/")
				if !prefixContains(prefix, value) {
					continue
				}
				if prefixContains(scope.PathPrefix, prefix) {
					// The authored prefix is narrower than the policy grant.
				} else if prefixContains(prefix, scope.PathPrefix) {
					prefix = scope.PathPrefix
				} else {
					continue
				}
				if !matched || len(prefix) > len(valuePrefix) {
					valuePrefix = prefix
				}
				matched = true
			}
			if !matched {
				return nil, false
			}
			if selected && chosen != valuePrefix {
				return nil, false
			}
			chosen, selected = valuePrefix, true
		}
		if prior, set := byArgument[scope.PathArgument]; set && prior != chosen {
			return nil, false
		}
		byArgument[scope.PathArgument] = chosen
		out[i].PathPrefix = chosen
	}
	return out, true
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

func PreviewEntriesEqual(a, b ProfilePreviewEntry) bool {
	return a.OutputBytes == b.OutputBytes && a.TimeoutSeconds == b.TimeoutSeconds && slices.EqualFunc(a.Scopes, b.Scopes, func(x, y CapabilityScope) bool { return x == y })
}

// CapabilityRuleDecision returns one allow/ceiling intersection. Never merge
// unrelated rules' actions, resources, connections, expiry or execution limits.
// Projection asks whether a permitted path exists; dispatch checks the actual path.
func policyDefaultRuleSets(policy CapabilityPolicy) [][]CapabilityRule {
	return append([][]CapabilityRule{policy.Defaults}, GroupRuleSets(policy.DefaultGroups)...)
}

func GroupRuleSets(groups []CapabilityGroup) [][]CapabilityRule {
	sets := make([][]CapabilityRule, 0, len(groups))
	for _, group := range groups {
		sets = append(sets, group.Members)
	}
	return sets
}

func allowedRuleSets(profile CapabilityProfile, policy CapabilityPolicy) [][]CapabilityRule {
	sets := policyDefaultRuleSets(policy)
	sets = append(sets, profile.Allows)
	sets = append(sets, GroupRuleSets(profile.AllowGroups)...)
	return sets
}

func ruleActive(rule CapabilityRule, now time.Time) bool {
	return rule.ExpiresAt.IsZero() || now.Before(rule.ExpiresAt)
}

// scanCapabilityRules returns the first active allow∩ceiling pair admitting
// the tuple. admit decides whether the candidate prefix is acceptable:
// CapabilityRuleDecision passes the deny check, coverage passes true. Never
// merge unrelated rules' actions, resources, connections, expiry or
// execution limits.
func scanCapabilityRules(profile CapabilityProfile, policy CapabilityPolicy, tuple CapabilityRule, actualPath *string, now time.Time, admit func(candidate string) bool) (CapabilityLimits, string, bool) {
	for _, allows := range allowedRuleSets(profile, policy) {
		for _, allow := range allows {
			if !ruleActive(allow, now) || !sameCapabilityTuple(allow, tuple) {
				continue
			}
			for _, ceiling := range policy.Ceiling {
				if !ruleActive(ceiling, now) || !sameCapabilityTuple(allow, ceiling) {
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
				if admit(candidate) {
					return CapabilityLimits{min(allow.Limits.OutputBytes, ceiling.Limits.OutputBytes), min(allow.Limits.TimeoutSeconds, ceiling.Limits.TimeoutSeconds)}, prefix, true
				}
			}
		}
	}
	return CapabilityLimits{}, "", false
}

func CapabilityRuleDecision(profile CapabilityProfile, policy CapabilityPolicy, tuple CapabilityRule, actualPath *string, now time.Time) (CapabilityLimits, string, bool) {
	return scanCapabilityRules(profile, policy, tuple, actualPath, now, func(candidate string) bool {
		for _, denies := range [][]CapabilityRule{policy.Denies, profile.Denies} {
			for _, deny := range denies {
				if ruleActive(deny, now) && sameCapabilityTuple(deny, tuple) && prefixContains(deny.PathPrefix, candidate) {
					return false
				}
			}
		}
		return true
	})
}

// CapabilityCoverage reports whether a selection's uses are backed by active
// allow∩ceiling authority, ignoring deny rules. Coverage is the profile's
// positive authority: a selection the rules cannot admit is suppressed under
// projection — a gap the denial surface and diagnostics report, whatever its
// cause (revocation, never-authorized intent, or the digest-split bug).
// The path binding mirrors projection: a use without a path argument checks
// the empty path, a path-scoped use asks whether any permitted path exists.
func CapabilityCoverage(profile CapabilityProfile, policy CapabilityPolicy, selection CapabilitySelection, tool ToolSpec, now time.Time) bool {
	for _, use := range tool.Uses {
		tuple := CapabilityRule{CapabilityID: selection.CapabilityID, ImplementationDigest: selection.ImplementationDigest,
			Action: use.Action, Resource: use.Resource, ConnectionID: selection.ConnectionID}
		var actual *string
		if use.PathArgument == "" {
			empty := ""
			actual = &empty
		}
		if _, _, covered := scanCapabilityRules(profile, policy, tuple, actual, now, func(string) bool { return true }); !covered {
			return false
		}
	}
	return len(tool.Uses) > 0 || RemoteExecutor(tool.Executor)
}

// CapabilityRepinConflict is the digest-split signature: a use tuple the
// selection pins at its implementation digest is governed by an active rule
// at a different digest. The publish bug abandoned the rules' digest when it
// repinned selections; revocation or never-authorized intent does not match
// (no rule at another digest governs the tuple) — those are legitimate
// profile states the denial surface reports, while a repin that leaves the
// rules behind is a write-time bug the admission gate refuses. Denies count
// as governing: a tuple the rulebook knows only at another generation was
// still repinned past its records.
func CapabilityRepinConflict(profile CapabilityProfile, policy CapabilityPolicy, selection CapabilitySelection, tool ToolSpec, now time.Time) bool {
	if len(tool.Uses) == 0 {
		return false
	}
	sets := append(allowedRuleSets(profile, policy), policy.Ceiling, policy.Denies, profile.Denies)
	for _, use := range tool.Uses {
		for _, rules := range sets {
			for _, rule := range rules {
				if !ruleActive(rule, now) || rule.ImplementationDigest == "" || rule.ImplementationDigest == selection.ImplementationDigest {
					continue
				}
				if rule.CapabilityID == selection.CapabilityID && rule.Action == use.Action && rule.Resource == use.Resource && rule.ConnectionID == selection.ConnectionID {
					return true
				}
			}
		}
	}
	return false
}
