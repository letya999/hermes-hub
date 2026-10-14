package toolhub

import (
	"fmt"
	"github.com/letya999/hermes-hub/internal/identity"
	"regexp"
	"slices"
	"strings"
	"time"
)

// Tool governance (issue 139): a hub-owned, durable inventory of every tool
// section the platform can emit plus the scoped policy and grants that admit
// them. The document lives beside the ToolHub registry (governance.json) —
// same atomic-write and lock discipline, never agent-writable and never
// mounted into an agent runtime.

// Owners classify who authored a tool section.
const (
	OwnerHermes   = "hermes"   // upstream toolsets pinned/reviewed with Hermes
	OwnerHub      = "hub"      // hubctl tools, browser servers, hub connectors
	OwnerOrg      = "org"      // prepared organization bundle/catalog entries
	OwnerUser     = "user"     // user-owned definitions and settings surfaces
	OwnerExternal = "external" // arbitrary registry installs, unreviewed sources
)

// Section effects are the maximal effect class a section can produce.
const (
	EffectRead       = "read"
	EffectMutate     = "mutate"
	EffectExec       = "exec"
	EffectCredential = "credential"
)

// Policy scopes, most to least specific. `default` holds the shipped posture
// floor; real scopes can widen it, deny rules at user/org/global cannot be
// overridden by anything.
const (
	ScopeGlobal  = "global"
	ScopeOrg     = "org"
	ScopeUser    = "user"
	ScopeDefault = "default"
)

const (
	RuleAllow   = "allow"
	RuleDeny    = "deny"
	RuleCapRead = "cap:read" // only read-effect tools of the section stay reachable
)

// Well-known section ids.
const (
	SectionUserMCP    = "user_mcp"    // settings mcp:/mcp_servers declarations
	SectionOrgMCP     = "org_mcp"     // organization-provided MCP catalog
	SectionToolHub    = "toolhub"     // dynamic projected tools (attribute match)
	SectionHubTools   = "hub:tools"   // hubctl tools executor surface
	SectionHubBrowser = "hub:browser" // persistent-profile Playwright server
)

// Grant lifecycle. "expired" is derived from ExpiresAt, never stored.
const (
	GrantPending = "pending"
	GrantActive  = "active"
	GrantRevoked = "revoked"
)

var (
	sectionIDPattern = regexp.MustCompile(`^[a-z][a-z0-9_]*(:[a-z][a-z0-9_-]{0,39}){0,2}$`)
	attrKeyPattern   = regexp.MustCompile(`^[a-z][a-z0-9_]{0,39}$`)
	attrValuePattern = regexp.MustCompile(`^[a-zA-Z0-9_.:*-]{1,128}$`)
)

// ToolSection is one inventoried capability surface. Axes are deterministic
// facts, not free text; trust is derived from them.
type ToolSection struct {
	ID       string `json:"id"`
	Owner    string `json:"owner"`
	Official bool   `json:"official"`
	Dynamic  bool   `json:"dynamic"`
	Prepared bool   `json:"prepared"`
	Effect   string `json:"effect"`
	// Default is the section floor when no rule or grant matches:
	// "deny" for user_mcp and the restrictive-posture toolsets.
	Default string `json:"default"`
}

// Trust derives the review tier from the axes, never from a stored label.
func (t ToolSection) Trust() string {
	switch {
	case t.Owner == OwnerHermes && t.Official && !t.Dynamic:
		return "platform"
	case t.Owner == OwnerHub && t.Official:
		return "hub"
	case t.Owner == OwnerOrg && t.Prepared:
		return "prepared"
	case t.Owner == OwnerUser:
		return "user"
	default:
		return "external"
	}
}

// PolicyRule is one scoped statement. Section names an exact inventoried
// section; Match instead constrains dynamic tools by attribute
// (owner, prepared, dynamic, effect, definition_id, transport). Exactly one
// of Section/Match is required.
type PolicyRule struct {
	RuleID    string            `json:"rule_id"`
	Scope     string            `json:"scope"`
	Subject   string            `json:"subject,omitempty"` // org id (org) or principal id (user); empty at global/default
	Section   string            `json:"section,omitempty"`
	Match     map[string]string `json:"match,omitempty"`
	Effect    string            `json:"effect"`
	Reason    string            `json:"reason"`
	GrantedBy string            `json:"granted_by"`
	GrantID   string            `json:"grant_id,omitempty"`
	ExpiresAt time.Time         `json:"expires_at,omitempty"`
	Status    Status            `json:"status"`
	Revision  uint64            `json:"revision"`
}

// ToolGrant is the only path that widens one principal beyond the org floor:
// host-created, expiring, reason-bearing and revocable. Pending grants carry
// no authority; the in-conversation request + nonce acknowledge pair activates
// them and leaves the audit record.
type ToolGrant struct {
	Schema      int               `json:"schema"`
	GrantID     string            `json:"grant_id"`
	PrincipalID string            `json:"principal_id"`
	Section     string            `json:"section"`
	Match       map[string]string `json:"match,omitempty"`
	Reason      string            `json:"reason"`
	GrantedBy   string            `json:"granted_by"`
	ExpiresAt   time.Time         `json:"expires_at"`
	Status      string            `json:"status"`
	Revision    uint64            `json:"revision"`
	// Request* is the fresh in-conversation ask; the nonce binds the
	// acknowledge step to it and persists so a ToolHub restart cannot lose
	// or replay an in-flight activation.
	RequestNonce   string        `json:"request_nonce,omitempty"`
	RequestExpires time.Time     `json:"request_expires,omitempty"`
	RequestedAt    time.Time     `json:"requested_at,omitempty"`
	AckedAt        time.Time     `json:"acked_at,omitempty"`
	CreatedAt      time.Time     `json:"created_at"`
	Confirmation   *Confirmation `json:"confirmation,omitempty"`
}

// Governance is the whole policy document: inventory, rules, grants and a
// bounded revision history so every host mutation stays attributable.
type Governance struct {
	Schema    int           `json:"schema"`
	Revision  uint64        `json:"revision"`
	Inventory []ToolSection `json:"inventory"`
	Rules     []PolicyRule  `json:"rules"`
	Grants    []ToolGrant   `json:"grants"`
	History   []GovChange   `json:"history,omitempty"`
}

// GovChange is one append-only history entry recording what changed, who
// authorized it and why. Bounded to the newest entries.
type GovChange struct {
	At       time.Time `json:"at"`
	By       string    `json:"by"`
	Kind     string    `json:"kind"` // seed | migrate | rule | grant | revoke | request | acknowledge
	ID       string    `json:"id"`
	Revision uint64    `json:"revision"`
	Reason   string    `json:"reason,omitempty"`
}

// PolicyDecision is the evaluation outcome for one (principal, section).
type PolicyDecision struct {
	Effect string `json:"effect"` // allow | deny | cap:read
	Via    string `json:"via"`    // deny | grant | user | org | global | default | section | unknown
	Reason string `json:"reason,omitempty"`
}

func (d PolicyDecision) Allowed() bool { return d.Effect == RuleAllow || d.Effect == RuleCapRead }

// AllowsToolEffect narrows a decision to one concrete tool effect:
// cap:read admits only read-effect tools; deny admits none.
func (d PolicyDecision) AllowsToolEffect(effect string) bool {
	switch d.Effect {
	case RuleAllow:
		return true
	case RuleCapRead:
		return effect == "" || effect == EffectRead || effect == string(ReadEffect)
	default:
		return false
	}
}

// NewGovernance returns the seeded document (inventory + default rules).
func NewGovernance() *Governance {
	return &Governance{
		Schema:    SchemaVersion,
		Revision:  1,
		Inventory: SeedInventory(),
		Rules:     DefaultRules(),
	}
}

func (s ToolSection) validate() error {
	if !sectionIDPattern.MatchString(s.ID) || len(s.ID) > 96 {
		return fmt.Errorf("%w: section id", ErrInvalid)
	}
	switch s.Owner {
	case OwnerHermes, OwnerHub, OwnerOrg, OwnerUser, OwnerExternal:
	default:
		return fmt.Errorf("%w: section owner", ErrInvalid)
	}
	switch s.Effect {
	case EffectRead, EffectMutate, EffectExec, EffectCredential:
	default:
		return fmt.Errorf("%w: section effect", ErrInvalid)
	}
	if s.Default != RuleAllow && s.Default != RuleDeny {
		return fmt.Errorf("%w: section default", ErrInvalid)
	}
	return nil
}

func validRuleTarget(r PolicyRule) error {
	if (r.Section == "") == (len(r.Match) == 0) {
		return fmt.Errorf("%w: rule selects exactly one of section or match", ErrInvalid)
	}
	if r.Section != "" && !sectionIDPattern.MatchString(r.Section) {
		return fmt.Errorf("%w: rule section", ErrInvalid)
	}
	if len(r.Match) > 8 {
		return fmt.Errorf("%w: rule match breadth", ErrInvalid)
	}
	for key, value := range r.Match {
		if !attrKeyPattern.MatchString(key) || !attrValuePattern.MatchString(value) {
			return fmt.Errorf("%w: rule match pair", ErrInvalid)
		}
	}
	return nil
}

func (r PolicyRule) validate() error {
	if !identity.ValidID(r.RuleID) || r.Revision == 0 {
		return fmt.Errorf("%w: rule identity", ErrInvalid)
	}
	switch r.Scope {
	case ScopeGlobal, ScopeDefault:
		if r.Subject != "" {
			return fmt.Errorf("%w: %s rule carries a subject", ErrInvalid, r.Scope)
		}
	case ScopeOrg, ScopeUser:
		if !identity.ValidID(r.Subject) {
			return fmt.Errorf("%w: %s rule needs a subject", ErrInvalid, r.Scope)
		}
	default:
		return fmt.Errorf("%w: rule scope", ErrInvalid)
	}
	if err := validRuleTarget(r); err != nil {
		return err
	}
	switch r.Effect {
	case RuleAllow, RuleCapRead:
		// A user-scope allow is a host-authored per-principal record (the
		// migration note or an org decision narrowed to one user); the agent
		// surface has no rule write path at all.
	case RuleDeny:
		if r.Scope == ScopeDefault && r.Section == SectionUserMCP {
			return fmt.Errorf("%w: user_mcp deny must stay a section default so grants can unlock it", ErrInvalid)
		}
	default:
		return fmt.Errorf("%w: rule effect", ErrInvalid)
	}
	if !identity.ValidID(r.GrantedBy) || r.GrantedBy == "model" || r.GrantedBy == "hermes" {
		return fmt.Errorf("%w: rule issuer", ErrUnauthorized)
	}
	if strings.TrimSpace(r.Reason) == "" || len(r.Reason) > 512 || strings.ContainsAny(r.Reason, "\x00\r\n") {
		return fmt.Errorf("%w: rule reason", ErrInvalid)
	}
	if !r.ExpiresAt.IsZero() && (r.ExpiresAt.Year() < 1 || r.ExpiresAt.Year() > 9999) {
		return fmt.Errorf("%w: rule expiry", ErrInvalid)
	}
	if r.Status != ActiveStatus && r.Status != DisabledStatus && r.Status != RevokedStatus {
		return fmt.Errorf("%w: rule status", ErrInvalid)
	}
	if r.GrantID != "" && !identity.ValidID(r.GrantID) {
		return fmt.Errorf("%w: rule grant link", ErrInvalid)
	}
	return nil
}

func (r PolicyRule) matches(section string, attrs map[string]string) bool {
	if r.Section != "" {
		return r.Section == section
	}
	for key, want := range r.Match {
		if attrs[key] != want {
			return false
		}
	}
	return true
}

// Validate enforces the whole document so a corrupt or hand-edited store
// fails closed rather than silently widening authority.
func (g *Governance) Validate() error {
	if g == nil || g.Schema != SchemaVersion || g.Revision == 0 {
		return fmt.Errorf("%w: governance identity", ErrInvalid)
	}
	if len(g.Inventory) == 0 || len(g.Inventory) > 4096 || len(g.Rules) > 4096 || len(g.Grants) > 4096 {
		return fmt.Errorf("%w: governance breadth", ErrInvalid)
	}
	sections := map[string]bool{}
	for _, s := range g.Inventory {
		if err := s.validate(); err != nil {
			return err
		}
		if sections[s.ID] {
			return fmt.Errorf("%w: duplicate section %s", ErrInvalid, s.ID)
		}
		sections[s.ID] = true
	}
	for _, id := range []string{SectionUserMCP, SectionOrgMCP, SectionToolHub, SectionHubTools, SectionHubBrowser} {
		if !sections[id] {
			return fmt.Errorf("%w: governance inventory is missing %s", ErrInvalid, id)
		}
	}
	rules := map[string]bool{}
	for _, r := range g.Rules {
		if err := r.validate(); err != nil {
			return err
		}
		if rules[r.RuleID] {
			return fmt.Errorf("%w: duplicate rule %s", ErrInvalid, r.RuleID)
		}
		rules[r.RuleID] = true
	}
	grants := map[string]bool{}
	for _, grant := range g.Grants {
		if err := grant.validate(); err != nil {
			return err
		}
		if grants[grant.GrantID] {
			return fmt.Errorf("%w: duplicate grant %s", ErrInvalid, grant.GrantID)
		}
		grants[grant.GrantID] = true
	}
	if len(g.History) > 8192 {
		return fmt.Errorf("%w: governance history bound", ErrInvalid)
	}
	return nil
}

// ruleApplies at a scope: exact-section rules outrank attribute rules.
func ruleApplies(rule PolicyRule, scope, subject, section string, attrs map[string]string, now time.Time) bool {
	if rule.Scope != scope || rule.Status != ActiveStatus {
		return false
	}
	if (scope == ScopeUser || scope == ScopeOrg) && rule.Subject != subject {
		return false
	}
	if !rule.ExpiresAt.IsZero() && !now.Before(rule.ExpiresAt) {
		return false
	}
	return rule.matches(section, attrs)
}

// Evaluate is the single fixed order: any active deny at user/org/global wins
// outright; then the most specific scope — grant, user, org, global, default —
// supplies the effect; the section's inventory default is the last resort and
// an unknown section denies.
func (g *Governance) Evaluate(principal, org, section string, attrs map[string]string, now time.Time) PolicyDecision {
	if g == nil {
		return PolicyDecision{Effect: RuleDeny, Via: "unknown", Reason: "no governance document"}
	}
	now = now.UTC()
	subjects := map[string]string{ScopeUser: principal, ScopeOrg: org}
	// Phase 1: deny at any real scope is absolute.
	for _, scope := range []string{ScopeUser, ScopeOrg, ScopeGlobal} {
		for _, rule := range g.Rules {
			if rule.Effect == RuleDeny && ruleApplies(rule, scope, subjects[scope], section, attrs, now) {
				return PolicyDecision{Effect: RuleDeny, Via: "deny", Reason: rule.Reason}
			}
		}
	}
	// Phase 2: an active unexpired grant is the only widening path.
	for _, grant := range g.Grants {
		if grant.active(now) && grant.matches(principal, section, attrs) {
			return PolicyDecision{Effect: RuleAllow, Via: "grant", Reason: grant.Reason}
		}
	}
	// Phase 3: scoped rules by precedence.
	for _, scope := range []string{ScopeUser, ScopeOrg, ScopeGlobal, ScopeDefault} {
		sectionRules, attrRules := []PolicyRule{}, []PolicyRule{}
		for _, rule := range g.Rules {
			if ruleApplies(rule, scope, subjects[scope], section, attrs, now) {
				if rule.Section != "" {
					sectionRules = append(sectionRules, rule)
				} else {
					attrRules = append(attrRules, rule)
				}
			}
		}
		for _, candidate := range [][]PolicyRule{sectionRules, attrRules} {
			if len(candidate) == 0 {
				continue
			}
			// Same-scope same-kind conflicts resolve deterministically: deny
			// first (phase 1 only covered real scopes; a default deny is just
			// the floor and still sorts ahead of a same-scope allow).
			slices.SortFunc(candidate, func(a, b PolicyRule) int {
				if a.Effect != b.Effect {
					if a.Effect == RuleDeny {
						return -1
					}
					if b.Effect == RuleDeny {
						return 1
					}
					if a.Effect == RuleCapRead {
						return -1
					}
				}
				return strings.Compare(a.RuleID, b.RuleID)
			})
			rule := candidate[0]
			return PolicyDecision{Effect: rule.Effect, Via: scope, Reason: rule.Reason}
		}
	}
	// Phase 4: inventoried section default; unknown sections deny.
	for _, s := range g.Inventory {
		if s.ID == section {
			return PolicyDecision{Effect: s.Default, Via: "section", Reason: "inventory default"}
		}
	}
	return PolicyDecision{Effect: RuleDeny, Via: "unknown", Reason: "section not in the tool inventory"}
}

// record appends one bounded history entry (newest 8192 kept — validation
// bound) and bumps the document revision.
func (g *Governance) record(kind, id, by, reason string, now time.Time) {
	g.Revision++
	g.History = append(g.History, GovChange{At: now.UTC(), By: by, Kind: kind, ID: id, Revision: g.Revision, Reason: reason})
	if len(g.History) > 8192 {
		g.History = append([]GovChange(nil), g.History[len(g.History)-8192:]...)
	}
}

// PutRule upserts a policy rule. Host code only; there is no agent write path.
func (g *Governance) PutRule(rule PolicyRule, now time.Time) error {
	if err := rule.validate(); err != nil {
		return err
	}
	for i, existing := range g.Rules {
		if existing.RuleID == rule.RuleID {
			if rule.Revision <= existing.Revision {
				return fmt.Errorf("%w: rule revision must advance", ErrConflict)
			}
			g.Rules[i] = rule
			g.record("rule", rule.RuleID, rule.GrantedBy, rule.Reason, now)
			return nil
		}
	}
	g.Rules = append(g.Rules, rule)
	g.record("rule", rule.RuleID, rule.GrantedBy, rule.Reason, now)
	return nil
}

// ToolPolicySnapshot is the immutable per-render extract handed to the
// hub-owned executors through /config/tool-policy.json. It carries
// capability-family ids — never rules, reasons or other principals' data —
// so the executor can deny without ever parsing the governance document.
type ToolPolicySnapshot struct {
	Schema    int      `json:"schema"`
	Revision  uint64   `json:"revision"`
	Principal string   `json:"principal"`
	DenyAll   bool     `json:"deny_all,omitempty"`
	Deny      []string `json:"deny,omitempty"`
	ReadOnly  []string `json:"read_only,omitempty"`
}

// Denied reports whether the capability family is cut for this principal.
func (p *ToolPolicySnapshot) Denied(family string) bool {
	return p != nil && (p.DenyAll || slices.Contains(p.Deny, family))
}

// CappedReadOnly reports whether the family renders read-only.
func (p *ToolPolicySnapshot) CappedReadOnly(family string) bool {
	return p != nil && !p.DenyAll && slices.Contains(p.ReadOnly, family)
}
