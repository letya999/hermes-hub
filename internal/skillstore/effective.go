package skillstore

import (
	"fmt"
	"os"
	"path/filepath"
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
	Name        string `json:"name"`
	Description string `json:"description"`
	Source      string `json:"source"`
	Revision    string `json:"revision"`
	Pinned      bool   `json:"pinned,omitempty"`
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
			entry := Entry{Name: name, Description: m.Description, Source: src.Name, Revision: Revision(tree)}
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
