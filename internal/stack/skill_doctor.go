package stack

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"

	"github.com/letya999/hermes-hub/internal/skillstore"
)

// SkillDoctor reports effective-view readiness for the operator: skills the
// agent catalog deliberately hides (ineligible, implicit-hidden), missing
// requirements, source collisions and pending drafts. Read-only — it only
// renders what the last render already decided.
func SkillDoctor(dir string) ([]string, error) {
	raw, err := os.ReadFile(filepath.Join(GeneratedEffectiveSkillsDir(dir), "index.json"))
	if err != nil {
		return []string{"Skills: effective view not rendered; run render or up first"}, nil
	}
	var idx skillstore.EffectiveIndex
	if err := json.Unmarshal(raw, &idx); err != nil {
		return nil, fmt.Errorf("skills index %s: %w", GeneratedEffectiveSkillsDir(dir), err)
	}
	perSource := map[string]int{}
	for _, e := range idx.Skills {
		perSource[e.Source]++
	}
	sources := make([]string, 0, len(perSource))
	for name := range perSource {
		sources = append(sources, name)
	}
	slices.Sort(sources)
	parts := make([]string, 0, len(sources))
	for _, name := range sources {
		parts = append(parts, fmt.Sprintf("%s=%d", name, perSource[name]))
	}
	lines := []string{fmt.Sprintf("Skills: %d effective (%s), catalog revision %s",
		len(idx.Skills), strings.Join(parts, " "), shortRev(idx.Revision))}
	bad := 0
	for _, e := range idx.Skills {
		switch {
		case !e.Eligible:
			bad++
			lines = append(lines, fmt.Sprintf("  %s [%s] INELIGIBLE, missing: %s",
				e.Name, e.Source, strings.Join(e.Missing, ", ")))
		case e.Hidden:
			lines = append(lines, fmt.Sprintf("  %s [%s] hidden from implicit catalog (explicit call only)", e.Name, e.Source))
		}
		if len(e.CredsMissing) > 0 {
			lines = append(lines, fmt.Sprintf("  %s [%s] setup needed, credentials: %s",
				e.Name, e.Source, strings.Join(e.CredsMissing, ", ")))
		}
		if len(e.DepsMissing) > 0 {
			lines = append(lines, fmt.Sprintf("  %s [%s] deps not resolvable here: %s",
				e.Name, e.Source, strings.Join(e.DepsMissing, ", ")))
		}
		if e.InstructionsOnly {
			lines = append(lines, fmt.Sprintf("  %s [%s] has scripts but no exec grant — instructions only", e.Name, e.Source))
		}
	}
	for _, n := range idx.Notices {
		lines = append(lines, fmt.Sprintf("  notice %s: %q from %s hidden by %s", n.Kind, n.Name, n.HiddenBySource, noticeWinner(idx, n.Name)))
	}
	if entries, derr := os.ReadDir(filepath.Join(dir, "skills", ".drafts")); derr == nil && len(entries) > 0 {
		lines = append(lines, fmt.Sprintf("  %d pending draft(s) under skills/.drafts — hubctl skill drafts", len(entries)))
	}
	if bad == 0 {
		lines = append(lines, "Skills: all listed entries eligible.")
	}
	return lines, nil
}

func shortRev(rev string) string {
	if len(rev) > 12 {
		return rev[:12]
	}
	if rev == "" {
		return "none"
	}
	return rev
}

// noticeWinner resolves which source won a collision: the entry that kept the
// name in the effective view.
func noticeWinner(idx skillstore.EffectiveIndex, name string) string {
	for _, e := range idx.Skills {
		if e.Name == name {
			return e.Source
		}
	}
	return "?"
}
