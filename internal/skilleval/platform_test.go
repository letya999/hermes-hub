package skilleval

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/letya999/hermes-hub/internal/skillstore"
	"github.com/letya999/hermes-hub/internal/skillstore/scan"
)

// R12.6: the platform skills must lint clean — findings are advisory but
// the shipped catalog should set the example.
func TestPlatformSkillsPassLint(t *testing.T) {
	for _, dir := range []string{
		filepath.Join("..", "..", "config", "skills", "deep-research-embedded"),
		filepath.Join("..", "..", "config", "skills", "hermes-hub-mcp-onboarding"),
		filepath.Join("..", "..", "internal", "systemguide"),
	} {
		name := filepath.Base(dir)
		body, err := os.ReadFile(filepath.Join(dir, "SKILL.md"))
		if err != nil {
			t.Fatal(err)
		}
		m, _, err := skillstore.ParseSkillMD(name, body, true)
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		paths := []string{}
		_ = filepath.Walk(dir, func(p string, info os.FileInfo, err error) error {
			if err == nil && !info.IsDir() {
				if rel, err := filepath.Rel(dir, p); err == nil {
					paths = append(paths, filepath.ToSlash(rel))
				}
			}
			return err
		})
		// In the published store the dir is the skill name; the source
		// checkout's folder name is not the store dir.
		findings := scan.Lint(m.Name, scan.LintInput{
			Name:        m.Name,
			Description: m.Description,
			Body:        string(body),
			Files:       len(paths),
			Paths:       paths,
		})
		if len(findings) != 0 {
			ids := []string{}
			for _, f := range findings {
				ids = append(ids, f.RuleID)
			}
			t.Fatalf("%s has lint findings: %s", name, strings.Join(ids, ", "))
		}
	}
}

// R12.2/R12.4: every platform skill ships a valid trigger suite — ~20
// queries, a 60/40 split, ≥1 run and a sane threshold — so `hubctl skill
// eval` can gate release on it.
func TestPlatformSkillsShipTriggerFixtures(t *testing.T) {
	for _, dir := range []string{
		filepath.Join("..", "..", "config", "skills", "deep-research-embedded"),
		filepath.Join("..", "..", "config", "skills", "hermes-hub-mcp-onboarding"),
		filepath.Join("..", "..", "internal", "systemguide"),
	} {
		suite, err := LoadTriggers(dir)
		if err != nil {
			t.Fatalf("%s: %v", dir, err)
		}
		if len(suite.Queries) < 18 || len(suite.Queries) > 30 {
			t.Fatalf("%s: %d queries, want ~20", dir, len(suite.Queries))
		}
		if suite.Split <= 0 || suite.Split >= 1 || suite.Runs < 1 || suite.Threshold <= 0 || suite.Threshold > 1 {
			t.Fatalf("%s: bad suite params %+v", dir, suite)
		}
		var pos, neg int
		for _, q := range suite.Queries {
			if q.Expect {
				pos++
			} else {
				neg++
			}
		}
		if pos == 0 || neg == 0 {
			t.Fatalf("%s: suite needs positive and negative queries", dir)
		}
	}
}
