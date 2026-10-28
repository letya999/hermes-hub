package skillstore

import (
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"time"
)

// Source is one precedence tier feeding the effective view, ordered
// highest first: platform, org, user, then catalog sources like hermes.
type Source struct {
	Name string `json:"name"`
	Dir  string `json:"-"`
}

// Entry is one effective skill as seen by the agent.
type Entry struct {
	Name        string   `json:"name"`
	Description string   `json:"description"`
	Source      string   `json:"source"`
	Revision    string   `json:"revision"`
	Pinned      bool     `json:"pinned,omitempty"`
	Tags        []string `json:"tags,omitempty"`
	Requires    Requires `json:"requires,omitempty"`
	Platforms   []string `json:"platforms,omitempty"`
	// Hidden means the skill is absent from implicit catalog surfaces but
	// still invocable explicitly (disable-model-invocation or
	// allow_implicit_invocation:false).
	Hidden bool `json:"hidden,omitempty"`
	// Eligible/Missing are stamped by ApplyReadiness from the space's
	// granted capabilities — ineligible entries are hidden everywhere and
	// explicit invocation reports Missing.
	Eligible bool     `json:"eligible"`
	Missing  []string `json:"missing,omitempty"`
}

// Capabilities is the space's granted capability surface as the renderer
// knows it: native toolsets, connector names, target platform.
type Capabilities struct {
	Toolsets   map[string]bool
	Connectors map[string]bool
	Platform   string
}

// ApplyReadiness stamps Eligible/Missing on every entry (R6.1): declared
// connectors and platforms are checked against the space's granted
// capability surface. Env/bins/deps are readiness signals for the executor
// surfaces, not eligibility — they cannot be verified at render.
func (idx *EffectiveIndex) ApplyReadiness(caps Capabilities) {
	for i := range idx.Skills {
		e := &idx.Skills[i]
		e.Missing = nil
		for _, ts := range e.Requires.Toolsets {
			if !caps.Toolsets[ts] {
				e.Missing = append(e.Missing, "toolset:"+ts)
			}
		}
		for _, c := range e.Requires.Connectors {
			if !caps.Connectors[c] {
				e.Missing = append(e.Missing, "connector:"+c)
			}
		}
		if len(e.Platforms) > 0 && !slices.Contains(e.Platforms, caps.Platform) {
			e.Missing = append(e.Missing, "platform:"+strings.Join(e.Platforms, "|"))
		}
		e.Eligible = len(e.Missing) == 0
	}
	// The session pin tracks what the catalog actually shows: a grant flip
	// changes visibility without touching any skill body (R6.3).
	rev := Tree{}
	for _, e := range idx.Skills {
		rev[e.Name+"/SKILL.md"] = File{Data: []byte(fmt.Sprintf(
			"%s|eligible=%v|hidden=%v|missing=%s",
			e.Revision, e.Eligible, e.Hidden, strings.Join(e.Missing, ",")))}
	}
	idx.Revision = Revision(rev)
}

// Notice reports a visibility event surfaced to an owner: a skill hidden
// by a higher-precedence source or a reserved-name shadow attempt.
type Notice struct {
	Kind           string `json:"kind"` // collision|reserved
	Name           string `json:"name"`
	HiddenBySource string `json:"hidden_by_source"`
	At             string `json:"at"`
}

// EffectiveIndex is materialized as index.json beside the view and pinned
// per session (R6.5).
type EffectiveIndex struct {
	Schema   int      `json:"schema"`
	Revision string   `json:"revision"`
	Built    string   `json:"built"`
	Skills   []Entry  `json:"skills"`
	Notices  []Notice `json:"notices,omitempty"`
}

// Build composes the effective view under dest: only winning skills are
// copied, plus index.json. Dot-entries (hub-private .drafts/.revisions/
// .archive/.state/.sources) are invisible to loadTree, so they cannot
// leak into the view. The mount is what enforces read-only at runtime.
func Build(dest string, sources []Source) (*EffectiveIndex, error) {
	if err := os.MkdirAll(dest, 0755); err != nil {
		return nil, err
	}
	idx := &EffectiveIndex{
		Schema: 1, Built: time.Now().UTC().Format(time.RFC3339Nano),
		Skills: []Entry{},
	}
	won := map[string]bool{}
	rev := Tree{}
	for _, src := range sources {
		st, err := Open(src.Dir, ScopeCatalog) // read-only scan
		if err != nil {
			return nil, fmt.Errorf("source %s: %w", src.Name, err)
		}
		names, err := st.Names()
		if err != nil {
			return nil, fmt.Errorf("source %s: %w", src.Name, err)
		}
		for _, name := range names {
			if src.Name != "platform" && ReservedName(name) {
				idx.Notices = append(idx.Notices, Notice{
					Kind: "reserved", Name: name, HiddenBySource: src.Name,
					At: idx.Built,
				})
				continue
			}
			if won[name] {
				idx.Notices = append(idx.Notices, Notice{
					Kind: "collision", Name: name, HiddenBySource: idx.skillsSource(name),
					At: idx.Built,
				})
				continue
			}
			tree, err := st.Tree(name)
			if err != nil {
				return nil, fmt.Errorf("source %s skill %s: %w", src.Name, name, err)
			}
			m, _, err := ParseSkillMD(name, tree[SkillFile].Data, true)
			if err != nil {
				return nil, fmt.Errorf("source %s skill %s: %w", src.Name, name, err)
			}
			won[name] = true
			entry := Entry{
				Name: name, Description: m.Description, Source: src.Name,
				Revision: Revision(tree), Tags: m.Tags, Requires: m.Requires,
				Platforms: m.Platforms, Eligible: true,
				Hidden: m.DisableModelInvocation ||
					(m.AllowImplicitInvocation != nil && !*m.AllowImplicitInvocation),
			}
			idx.Skills = append(idx.Skills, entry)
			if err := writeTree(filepath.Join(dest, name), tree); err != nil {
				return nil, err
			}
			rev[name+"/SKILL.md"] = File{Data: []byte(entry.Revision)}
		}
	}
	idx.Revision = Revision(rev)
	return idx, writeJSONFile(filepath.Join(dest, "index.json"), mustJSON(idx))
}

// WriteIndex persists index.json beside a built effective view — used by
// render after ApplyReadiness stamps space-specific eligibility.
func WriteIndex(dest string, idx *EffectiveIndex) error {
	return writeJSONFile(filepath.Join(dest, "index.json"), mustJSON(idx))
}

func (idx *EffectiveIndex) skillsSource(name string) string {
	for _, e := range idx.Skills {
		if e.Name == name {
			return e.Source
		}
	}
	return ""
}

// HigherTierHas reports whether a skill named name exists in a tier higher
// than tier (sources are ordered highest first). Same-scope collisions are
// rejected at write time by the caller using this check.
func HigherTierHas(sources []Source, name string, tier int) (string, bool) {
	for i := 0; i < tier && i < len(sources); i++ {
		st, err := Open(sources[i].Dir, ScopeCatalog)
		if err != nil {
			continue
		}
		if names, err := st.Names(); err == nil {
			for _, n := range names {
				if n == name {
					return sources[i].Name, true
				}
			}
		}
	}
	return "", false
}
