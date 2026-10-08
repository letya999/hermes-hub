package toolhub

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"time"

	"github.com/gofrs/flock"
	"github.com/letya999/hermes-hub/internal/identity"
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

// SeedInventory is the built-in catalog of sections the platform can emit.
// Anything rendered that is not inventoried lands as unknown → deny.
func SeedInventory() []ToolSection {
	native := func(name, effect, def string) ToolSection {
		return ToolSection{ID: "native:" + name, Owner: OwnerHermes, Official: true, Effect: effect, Default: def}
	}
	hub := func(id, effect string) ToolSection {
		return ToolSection{ID: id, Owner: OwnerHub, Official: true, Prepared: true, Effect: effect, Default: RuleAllow}
	}
	sections := []ToolSection{
		// Native toolsets — the unmanaged emit list, the managed carve-out
		// vocabulary and the pseudo-toolsets rendered as native config.
		native("terminal", EffectExec, RuleDeny), // restrictive default (issue 139)
		native("code_execution", EffectExec, RuleDeny),
		native("file", EffectMutate, RuleAllow),
		native("web", EffectRead, RuleAllow),
		native("search", EffectRead, RuleAllow),
		native("x_search", EffectRead, RuleAllow),
		native("skills", EffectRead, RuleAllow),
		native("todo", EffectMutate, RuleAllow),
		native("cronjob", EffectMutate, RuleAllow),
		native("messaging", EffectMutate, RuleAllow),
		native("memory", EffectMutate, RuleAllow),
		native("session_search", EffectRead, RuleAllow),
		native("browser", EffectMutate, RuleAllow),
		native("image_gen", EffectMutate, RuleAllow),
		native("video", EffectRead, RuleAllow),
		native("video_gen", EffectMutate, RuleAllow),
		native("vision", EffectRead, RuleAllow),
		native("kanban", EffectMutate, RuleAllow),
		native("spotify", EffectMutate, RuleAllow),
		native("homeassistant", EffectMutate, RuleAllow),
		native("tts", EffectRead, RuleAllow),
		native("feishu_doc", EffectMutate, RuleAllow),
		native("feishu_drive", EffectMutate, RuleAllow),
		native("clarify", EffectRead, RuleAllow),
		native("debugging", EffectRead, RuleAllow),
		native("project", EffectMutate, RuleAllow),
		native("safe", EffectRead, RuleAllow),
		// Pseudo entries rendered through the native config.
		native("meet", EffectMutate, RuleAllow),
		native("transcription", EffectRead, RuleAllow),
		native("deep_research", EffectRead, RuleAllow),
		// Hub-owned MCP surfaces rendered into the effective config.
		hub(SectionHubTools, EffectMutate),
		hub(SectionHubBrowser, EffectMutate),
		hub("hub:browser_guest", EffectMutate),
		hub("hub:github", EffectMutate),
		hub("hub:gitlab", EffectMutate),
		hub("hub:google", EffectMutate),
		hub("hub:slack", EffectMutate),
		hub("hub:atlassian", EffectMutate),
		hub("hub:telegram_user", EffectMutate),
		hub("hub:desktop", EffectMutate),
		hub("hub:drafts", EffectMutate),
		// Executor capability families (`tools: <name>: toolhub`) — ids are
		// the capability ids after alias normalization (file→files,
		// docs→documents, code_exec→terminal).
		hub("hub:files", EffectMutate),
		hub("hub:documents", EffectMutate),
		hub("hub:images", EffectMutate),
		hub("hub:artifacts", EffectMutate),
		hub("hub:routines", EffectMutate),
		hub("hub:services", EffectMutate),
		hub("hub:settings", EffectMutate),
		hub("hub:image_gen", EffectMutate),
		hub("hub:hh", EffectMutate),
		hub("hub:ssh", EffectExec),
		hub("hub:terminal", EffectExec), // restrictive default, like native:terminal
		hub("hub:web", EffectMutate),
		// The two mutable per-principal surfaces.
		{ID: SectionUserMCP, Owner: OwnerUser, Effect: EffectCredential, Default: RuleDeny},
		{ID: SectionOrgMCP, Owner: OwnerOrg, Official: true, Prepared: true, Effect: EffectMutate, Default: RuleAllow},
		// Dynamic ToolHub projections; rules match attributes, per-tool
		// effects govern caps.
		{ID: SectionToolHub, Owner: OwnerOrg, Official: true, Dynamic: true, Prepared: true, Effect: EffectMutate, Default: RuleAllow},
	}
	return sections
}

// DefaultRules is the shipped posture floor: external (arbitrary registry /
// unreviewed) installs are denied at the lowest precedence so a grant can
// still admit one deliberately.
func DefaultRules() []PolicyRule {
	return []PolicyRule{{
		RuleID:    "default-external-deny",
		Scope:     ScopeDefault,
		Match:     map[string]string{"owner": OwnerExternal},
		Effect:    RuleDeny,
		Reason:    "shipped default: external/unprepared installs require a grant",
		GrantedBy: "operator",
		Status:    ActiveStatus,
		Revision:  1,
	}}
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

// MigrationSeed returns a seeded document plus one user-scope allow per
// listed section — the explicit record that preserves a pre-governance
// space's current emit set. Each note carries the migration reason so audits
// can tell preserved state from deliberate grants.
func MigrationSeed(principal string, preserve []string, now time.Time) *Governance {
	g := NewGovernance()
	g.History = append(g.History, GovChange{At: now.UTC(), By: "migration", Kind: "seed", ID: "inventory", Revision: 1})
	for _, section := range preserve {
		rule := PolicyRule{
			RuleID:    deterministicID("govrule", ScopeUser, principal, section, "migration"),
			Scope:     ScopeUser,
			Subject:   principal,
			Section:   section,
			Effect:    RuleAllow,
			Reason:    "migration: section was reachable before governance existed",
			GrantedBy: "migration",
			Status:    ActiveStatus,
			Revision:  1,
		}
		g.Rules = append(g.Rules, rule)
		g.History = append(g.History, GovChange{At: now.UTC(), By: "migration", Kind: "migrate", ID: rule.RuleID, Revision: 1, Reason: section})
	}
	return g
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

func (t ToolGrant) validate() error {
	if t.Schema != SchemaVersion || !identity.ValidID(t.GrantID) || !identity.ValidID(t.PrincipalID) ||
		!sectionIDPattern.MatchString(t.Section) || !identity.ValidID(t.GrantedBy) || t.Revision == 0 {
		return fmt.Errorf("%w: grant identity", ErrInvalid)
	}
	if t.GrantedBy == "model" || t.GrantedBy == "hermes" {
		return fmt.Errorf("%w: model cannot grant", ErrUnauthorized)
	}
	if t.ExpiresAt.IsZero() || t.ExpiresAt.Year() < 1 || t.ExpiresAt.Year() > 9999 {
		return fmt.Errorf("%w: grant needs an expiry", ErrInvalid)
	}
	if strings.TrimSpace(t.Reason) == "" || len(t.Reason) > 512 || strings.ContainsAny(t.Reason, "\x00\r\n") {
		return fmt.Errorf("%w: grant reason", ErrInvalid)
	}
	switch t.Status {
	case GrantPending, GrantActive, GrantRevoked:
	default:
		return fmt.Errorf("%w: grant status", ErrInvalid)
	}
	for key, value := range t.Match {
		if !attrKeyPattern.MatchString(key) || len(value) > 128 || strings.ContainsAny(value, "\x00\r\n") {
			return fmt.Errorf("%w: grant match pair", ErrInvalid)
		}
	}
	if len(t.Match) > 8 {
		return fmt.Errorf("%w: grant match breadth", ErrInvalid)
	}
	return nil
}

func (t ToolGrant) matches(principal, section string, attrs map[string]string) bool {
	if t.PrincipalID != principal || t.Section != section {
		return false
	}
	for key, want := range t.Match {
		if attrs[key] != want {
			return false
		}
	}
	return true
}

func (t ToolGrant) active(now time.Time) bool {
	return t.Status == GrantActive && now.Before(t.ExpiresAt)
}

// grantAwaitingAck reports a pending grant the in-conversation request already
// stamped a live nonce onto.
func (t ToolGrant) grantAwaitingAck(now time.Time) bool {
	return t.Status == GrantPending && t.RequestNonce != "" && now.Before(t.RequestExpires) && now.Before(t.ExpiresAt)
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

// NewToolGrant builds a pending host grant for one principal and section.
// The Confirmation must be stamped by the caller (hubctl --confirm) before
// PutToolGrant accepts the record.
func NewToolGrant(grantID, principal, section, reason, grantedBy string, expiresAt time.Time, match map[string]string) ToolGrant {
	return ToolGrant{
		Schema: SchemaVersion, GrantID: grantID, PrincipalID: principal, Section: section,
		Match: match, Reason: reason, GrantedBy: grantedBy,
		ExpiresAt: expiresAt.UTC(), Status: GrantPending, Revision: 1, CreatedAt: time.Now().UTC(),
	}
}

// PutToolGrant registers a confirmed pending grant. Re-registration of the
// same id requires a higher revision.
func (g *Governance) PutToolGrant(grant ToolGrant, now time.Time) error {
	if grant.Status != GrantPending && grant.Status != GrantRevoked {
		return fmt.Errorf("%w: a grant enters pending; the in-conversation request and acknowledge activate it", ErrInvalid)
	}
	if err := grant.validate(); err != nil {
		return err
	}
	if grant.Status == GrantPending && !validConfirmation(grant.Confirmation, grant, grant.GrantedBy) {
		return fmt.Errorf("%w: unconfirmed tool grant", ErrUnauthorized)
	}
	for i, existing := range g.Grants {
		if existing.GrantID == grant.GrantID {
			if grant.Revision <= existing.Revision {
				return fmt.Errorf("%w: grant revision must advance", ErrConflict)
			}
			g.Grants[i] = grant
			g.record("grant", grant.GrantID, grant.GrantedBy, grant.Reason, now)
			return nil
		}
	}
	g.Grants = append(g.Grants, grant)
	g.record("grant", grant.GrantID, grant.GrantedBy, grant.Reason, now)
	return nil
}

// PendingToolGrants lists the principal's pending grants, newest first.
func (g *Governance) PendingToolGrants(principal, section string, now time.Time) []ToolGrant {
	out := []ToolGrant{}
	for _, grant := range g.Grants {
		if grant.Status == GrantPending && grant.PrincipalID == principal &&
			(section == "" || grant.Section == section) && now.Before(grant.ExpiresAt) {
			out = append(out, grant)
		}
	}
	slices.SortFunc(out, func(a, b ToolGrant) int { return b.CreatedAt.Compare(a.CreatedAt) })
	return out
}

// RequestToolGrant stamps the in-conversation request onto a pending grant:
// a fresh single-use nonce with a short TTL. Repeated requests rotate the
// nonce so a stale acknowledge can never activate.
func (g *Governance) RequestToolGrant(principal, grantID string, now time.Time) (ToolGrant, error) {
	for i, grant := range g.Grants {
		if grant.GrantID != grantID || grant.PrincipalID != principal {
			continue
		}
		if grant.Status != GrantPending || !now.Before(grant.ExpiresAt) {
			return ToolGrant{}, fmt.Errorf("%w: grant is not pending", ErrInvalid)
		}
		nonce := make([]byte, 16)
		if _, err := rand.Read(nonce); err != nil {
			return ToolGrant{}, err
		}
		grant.RequestNonce = hex.EncodeToString(nonce)
		grant.RequestExpires = now.Add(15 * time.Minute).UTC()
		grant.RequestedAt = now.UTC()
		grant.Revision++
		g.Grants[i] = grant
		g.record("request", grant.GrantID, principal, grant.Section, now)
		return grant, nil
	}
	return ToolGrant{}, fmt.Errorf("%w: pending grant", ErrNotFound)
}

// AcknowledgeToolGrant activates a pending grant when the nonce from its
// in-conversation request matches. The audit entry is written into History
// before the status flips so an activation never exists unrecorded.
func (g *Governance) AcknowledgeToolGrant(principal, grantID, nonce string, now time.Time) (ToolGrant, error) {
	for i, grant := range g.Grants {
		if grant.GrantID != grantID || grant.PrincipalID != principal {
			continue
		}
		if !grant.grantAwaitingAck(now) {
			return ToolGrant{}, fmt.Errorf("%w: grant has no live request", ErrInvalid)
		}
		if nonce == "" || nonce != grant.RequestNonce {
			return ToolGrant{}, fmt.Errorf("%w: acknowledge nonce", ErrUnauthorized)
		}
		grant.Status = GrantActive
		grant.AckedAt = now.UTC()
		grant.RequestNonce = ""
		grant.Revision++
		g.Grants[i] = grant
		g.record("acknowledge", grant.GrantID, principal, "grant activated: "+grant.Section, now)
		return grant, nil
	}
	return ToolGrant{}, fmt.Errorf("%w: pending grant", ErrNotFound)
}

// RevokeToolGrant cuts a grant immediately; evaluation denies on the next
// call without a restart.
func (g *Governance) RevokeToolGrant(grantID, by, reason string, now time.Time) error {
	if !identity.ValidID(by) || by == "model" || by == "hermes" {
		return fmt.Errorf("%w: revoke issuer", ErrUnauthorized)
	}
	for i, grant := range g.Grants {
		if grant.GrantID != grantID {
			continue
		}
		grant.Status = GrantRevoked
		grant.RequestNonce = ""
		grant.Revision++
		g.Grants[i] = grant
		g.record("revoke", grant.GrantID, by, reason, now)
		return nil
	}
	return fmt.Errorf("%w: grant", ErrNotFound)
}

// Section returns the inventoried section or false for unknown ids.
func (g *Governance) Section(id string) (ToolSection, bool) {
	for _, s := range g.Inventory {
		if s.ID == id {
			return s, true
		}
	}
	return ToolSection{}, false
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

// LoadToolPolicy reads the rendered snapshot. A set path with a missing or
// corrupt file is a hard error — the executor fails closed rather than
// guessing at authority.
func LoadToolPolicy(path string) (*ToolPolicySnapshot, error) {
	body, err := os.ReadFile(path) // #nosec G304 -- rendered host-owned path
	if err != nil {
		return nil, err
	}
	if len(body) > 64*1024 {
		return nil, fmt.Errorf("%w: tool policy snapshot too large", ErrInvalid)
	}
	var p ToolPolicySnapshot
	decoder := json.NewDecoder(strings.NewReader(string(body)))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&p); err != nil {
		return nil, fmt.Errorf("%w: tool policy snapshot: %v", ErrInvalid, err)
	}
	if p.Schema != 1 || !identity.ValidID(p.Principal) || len(p.Deny) > 512 || len(p.ReadOnly) > 512 {
		return nil, fmt.Errorf("%w: tool policy snapshot identity", ErrInvalid)
	}
	return &p, nil
}

const maxGovernanceBytes = 4 << 20

// LoadGovernance reads and fully validates the document. A missing file is
// os.ErrNotExist; a corrupt or schema-invalid file is ErrInvalid — callers
// fail closed on it.
func LoadGovernance(path string) (*Governance, error) {
	body, err := os.ReadFile(path) // #nosec G304 -- host-owned policy path
	if err != nil {
		return nil, err
	}
	if len(body) > maxGovernanceBytes {
		return nil, fmt.Errorf("%w: governance document too large", ErrInvalid)
	}
	var g Governance
	decoder := json.NewDecoder(strings.NewReader(string(body)))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&g); err != nil {
		return nil, fmt.Errorf("%w: governance document: %v", ErrInvalid, err)
	}
	if err := g.Validate(); err != nil {
		return nil, err
	}
	return &g, nil
}

// SaveGovernance writes the document under a flock + atomic rename, the same
// durability discipline as the ToolHub registry. Callers already holding the
// lock (GovernanceStore.Update) must use saveGovernanceLocked — a second
// flock on the same file deadlocks.
func SaveGovernance(path string, g *Governance) error {
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		return err
	}
	lock := flock.New(path + ".lock")
	if err := lock.Lock(); err != nil {
		return err
	}
	defer lock.Unlock()
	return saveGovernanceLocked(path, g)
}

// saveGovernanceLocked performs the validate + atomic write; the caller owns
// exclusion (the store's flock or a single-threaded context).
func saveGovernanceLocked(path string, g *Governance) error {
	if err := g.Validate(); err != nil {
		return err
	}
	if err := safeStorePath(path); err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		return err
	}
	body, err := json.MarshalIndent(g, "", "  ")
	if err != nil {
		return err
	}
	tmp, err := os.CreateTemp(filepath.Dir(path), ".governance-*")
	if err != nil {
		return err
	}
	name := tmp.Name()
	defer os.Remove(name)
	if err = tmp.Chmod(0600); err == nil {
		_, err = tmp.Write(body)
	}
	if err == nil {
		err = tmp.Sync()
	}
	if closeErr := tmp.Close(); err == nil {
		err = closeErr
	}
	if err != nil {
		return err
	}
	return os.Rename(name, path)
}

// LoadOrSeedGovernance reads the document, or seeds + persists it when the
// file is absent. preserve lists deny-default sections the calling space
// already emits; each becomes an explicit recorded allow (the migration
// note). A corrupt existing file fails closed.
func LoadOrSeedGovernance(path, principal string, preserve []string, now time.Time) (*Governance, error) {
	g, err := LoadGovernance(path)
	if err == nil {
		return g, nil
	}
	if !errors.Is(err, os.ErrNotExist) {
		return nil, err
	}
	seed := MigrationSeed(principal, preserve, now)
	if saveErr := SaveGovernance(path, seed); saveErr != nil {
		// A read-only store path still evaluates correctly in memory; only a
		// corrupt on-disk document is a hard failure.
		return seed, nil
	}
	return seed, nil
}

// SectionAttrs exposes a section's axes as the attribute map rules match on,
// so `match: {owner: external}` works on static sections too.
func (s ToolSection) SectionAttrs() map[string]string {
	return map[string]string{
		"owner": s.Owner, "official": fmt.Sprint(s.Official),
		"dynamic": fmt.Sprint(s.Dynamic), "prepared": fmt.Sprint(s.Prepared),
		"effect": s.Effect,
	}
}
