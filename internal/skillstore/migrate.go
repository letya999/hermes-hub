package skillstore

import (
	"errors"
	"os"
	"path/filepath"
	"slices"
)

// MigrateReport summarizes a legacy hermes/skills migration.
type MigrateReport struct {
	Published []string `json:"published,omitempty"`
	Drafted   []string `json:"drafted,omitempty"`
	Skipped   []string `json:"skipped,omitempty"`
	Warnings  []string `json:"warnings,omitempty"`
}

// legacyRegistry mirrors the pre-M24 internal/skills registry shape:
// <space>/skills/registry.json = {schema, user, skills: [...]} while the
// skill dirs live at <space>/hermes/skills/<name>.
type legacyRegistry struct {
	Schema int `json:"schema"`
	Skills []struct {
		Name    string `json:"name"`
		Consent bool   `json:"consent"`
		Revoked bool   `json:"revoked"`
	} `json:"skills"`
}

// MigrateLegacySpace moves pre-M24 skills from <space>/hermes/skills into
// the unified store at <space>/skills. Consented registry skills publish;
// unrecorded dirs arrive as drafts; revoked skills are skipped. apply=false
// only reports. The legacy tree is never deleted.
func MigrateLegacySpace(spaceDir string, apply bool) (*MigrateReport, error) {
	return migrateLegacy(spaceDir, apply, false, nil)
}

// MigrateLegacyOrg migrates an organization's legacy <org>/hermes/skills.
// Org skills were already live for every member via the /org mount, so valid
// content publishes instead of stalling behind drafts.
func MigrateLegacyOrg(orgDir string, apply bool) (*MigrateReport, error) {
	return migrateLegacy(orgDir, apply, true, nil)
}

// MigrateLegacySpaceWithPlatform migrates a user space and additionally
// drops legacy copies byte-identical to a platform skill — those are stale
// hub-shipped duplicates, not user content.
func MigrateLegacySpaceWithPlatform(spaceDir, platformDir string, apply bool) (*MigrateReport, error) {
	var platform *Store
	if platformDir != "" {
		st, err := Open(platformDir, ScopeCatalog)
		if err != nil {
			return nil, err
		}
		platform = st
	}
	return migrateLegacy(spaceDir, apply, false, platform)
}

func migrateLegacy(dir string, apply, consentAll bool, platform *Store) (*MigrateReport, error) {
	rep := &MigrateReport{}
	legacyDir := filepath.Join(dir, "hermes", "skills")
	entries, err := os.ReadDir(legacyDir)
	if err != nil {
		return nil, err
	}
	var reg legacyRegistry
	if body, err := os.ReadFile(filepath.Join(dir, "skills", "registry.json")); err == nil {
		if err := jsonUnmarshal(body, &reg); err != nil {
			return nil, err
		}
	}
	consented := map[string]bool{}
	revoked := map[string]bool{}
	for _, r := range reg.Skills {
		if r.Revoked {
			revoked[r.Name] = true
		} else if r.Consent {
			consented[r.Name] = true
		}
	}

	st, err := Open(filepath.Join(dir, "skills"), ScopeUser)
	if err != nil {
		return nil, err
	}
	existing, err := st.Names()
	if err != nil {
		return nil, err
	}
	drafts, err := st.Drafts()
	if err != nil {
		return nil, err
	}
	drafted := map[string]bool{}
	for _, d := range drafts {
		drafted[d.Name] = true
	}

	migrateOne := func(skillDir, name string) error {
		if platform != nil {
			if pt, err := platform.Tree(name); err == nil {
				if lt, lerr := loadTree(skillDir); lerr == nil && Revision(lt) == Revision(pt) {
					rep.Skipped = append(rep.Skipped, name) // bundled copy, not user content
					return nil
				}
			}
		}
		switch {
		case revoked[name]:
			rep.Skipped = append(rep.Skipped, name)
		case slices.Contains(existing, name) || drafted[name]:
			// already migrated — idempotent
		case !apply:
			if consented[name] {
				rep.Published = append(rep.Published, name)
			} else {
				rep.Drafted = append(rep.Drafted, name)
			}
		case consented[name] || consentAll:
			tree, err := loadTree(skillDir)
			if err != nil {
				return err
			}
			if _, err := st.Publish(name, tree, IndexMeta{Author: "operator", Source: "legacy"}); err != nil {
				if !errors.Is(err, ErrScanCritical) {
					return err
				}
				// consent predates the pipeline; unsafe content degrades to draft (R4.4)
				if _, derr := st.CreateDraft(name, tree, DraftMeta{Author: "import", Reason: "legacy migration degraded: " + err.Error()}); derr != nil {
					return derr
				}
				rep.Drafted = append(rep.Drafted, name)
				return nil
			}
			rep.Published = append(rep.Published, name)
		default:
			tree, err := loadTree(skillDir)
			if err != nil {
				return err
			}
			if _, err := st.createDraft(name, tree, DraftMeta{Author: "import", Reason: "legacy migration"}, false); err != nil {
				if errors.Is(err, ErrInvalid) {
					// Malformed legacy content (e.g. upstream bundled skills
					// written before the stricter validation rules) must not
					// block render; the tree stays on disk for the operator.
					rep.Skipped = append(rep.Skipped, name+": "+err.Error())
					return nil
				}
				return err
			}
			rep.Drafted = append(rep.Drafted, name)
		}
		return nil
	}

	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		name := e.Name()
		// Upstream seeded bundled skills under category dirs (hermes/skills/
		// <category>/<skill>); a dir without SKILL.md is a category, so each
		// nested dir is handled as its own candidate.
		if _, err := os.Stat(filepath.Join(legacyDir, name, SkillFile)); err != nil {
			sub, serr := os.ReadDir(filepath.Join(legacyDir, name))
			if serr != nil {
				return nil, serr
			}
			for _, se := range sub {
				if !se.IsDir() {
					continue
				}
				if _, err := os.Stat(filepath.Join(legacyDir, name, se.Name(), SkillFile)); err != nil {
					continue
				}
				if err := migrateOne(filepath.Join(legacyDir, name, se.Name()), se.Name()); err != nil {
					return nil, err
				}
			}
			continue
		}
		if err := migrateOne(filepath.Join(legacyDir, name), name); err != nil {
			return nil, err
		}
	}
	return rep, nil
}
