package skillstore

import (
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

	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		name := e.Name()
		if platform != nil {
			if pt, err := platform.Tree(name); err == nil {
				if lt, lerr := loadTree(filepath.Join(legacyDir, name)); lerr == nil && Revision(lt) == Revision(pt) {
					rep.Skipped = append(rep.Skipped, name) // bundled copy, not user content
					continue
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
			tree, err := loadTree(filepath.Join(legacyDir, name))
			if err != nil {
				return nil, err
			}
			if _, err := st.Publish(name, tree, IndexMeta{Author: "operator", Source: "legacy"}); err != nil {
				return nil, err
			}
			rep.Published = append(rep.Published, name)
		default:
			tree, err := loadTree(filepath.Join(legacyDir, name))
			if err != nil {
				return nil, err
			}
			if _, err := st.CreateDraft(name, tree, DraftMeta{Author: "import", Reason: "legacy migration"}); err != nil {
				return nil, err
			}
			rep.Drafted = append(rep.Drafted, name)
		}
	}
	return rep, nil
}
