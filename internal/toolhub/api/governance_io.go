package api

import (
	"encoding/json"
	"errors"
	"fmt"
	"github.com/gofrs/flock"
	"github.com/letya999/hermes-hub/internal/identity"
	"os"
	"path/filepath"
	"strings"
	"time"
)

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
		hub("hub:hosting", EffectExec),
		hub("hub:exposure", EffectExec), // public endpoints: restrictive default, like ssh
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

// MigrationSeed returns a seeded document plus one user-scope allow per
// listed section — the explicit record that preserves a pre-governance
// space's current emit set. Each note carries the migration reason so audits
// can tell preserved state from deliberate grants.
func MigrationSeed(principal string, preserve []string, now time.Time) *Governance {
	g := NewGovernance()
	g.History = append(g.History, GovChange{At: now.UTC(), By: "migration", Kind: "seed", ID: "inventory", Revision: 1})
	for _, section := range preserve {
		rule := PolicyRule{
			RuleID:    DeterministicID("govrule", ScopeUser, principal, section, "migration"),
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

// Section returns the inventoried section or false for unknown ids.
func (g *Governance) Section(id string) (ToolSection, bool) {
	for _, s := range g.Inventory {
		if s.ID == id {
			return s, true
		}
	}
	return ToolSection{}, false
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
// lock (GovernanceStore.Update) must use SaveGovernanceLocked — a second
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
	return SaveGovernanceLocked(path, g)
}

// SaveGovernanceLocked performs the validate + atomic write; the caller owns
// exclusion (the store's flock or a single-threaded context).
func SaveGovernanceLocked(path string, g *Governance) error {
	if err := g.Validate(); err != nil {
		return err
	}
	if err := SafeStorePath(path); err != nil {
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
