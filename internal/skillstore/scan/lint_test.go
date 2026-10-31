package scan

import (
	"strings"
	"testing"
)

func lintRules(r []Finding) map[string]int {
	out := map[string]int{}
	for _, f := range r {
		out[f.RuleID]++
	}
	return out
}

// R12.1: a clean skill produces no advisory findings.
func TestLintCleanSkill(t *testing.T) {
	got := Lint("pdf-tools", LintInput{
		Name:        "pdf-tools",
		Description: "Use when extracting, merging or shrinking PDF files.",
		Body:        "steps\n",
		Files:       2,
	})
	if len(got) != 0 {
		t.Fatalf("clean skill linted: %+v", got)
	}
}

// R12.1: the "This skill" prefix and non-imperative openers are flagged.
func TestLintDescriptionShape(t *testing.T) {
	got := Lint("pdf-tools", LintInput{
		Name:        "pdf-tools",
		Description: "This skill is a helper for PDF work.",
		Body:        "x",
		Files:       1,
	})
	rules := lintRules(got)
	if rules["desc.prefix"] != 1 {
		t.Fatalf("prefix %v", rules)
	}
	// A description that starts with "This" is not imperative.
	if rules["desc.imperative"] != 1 {
		t.Fatalf("imperative %v", rules)
	}
}

// R12.1: participle and noun openers ("Bounded …", "Helps …", "A tool …")
// cannot head an imperative "Use when …" description.
func TestLintNonImperativeOpeners(t *testing.T) {
	for _, desc := range []string{
		"Bounded multi-round deep web research: scope a brief",
		"Helps with PDF extraction",
		"A tool for reading PDFs",
		"The assistant's memory",
	} {
		got := Lint("x", LintInput{Name: "x", Description: desc, Body: "x", Files: 1})
		if lintRules(got)["desc.imperative"] != 1 {
			t.Fatalf("non-imperative %q not flagged", desc)
		}
	}
	for _, desc := range []string{
		"Use when extracting PDFs",
		"Route requests to install an MCP",
		"Explain Hermes Hub concepts",
	} {
		got := Lint("x", LintInput{Name: "x", Description: desc, Body: "x", Files: 1})
		if lintRules(got)["desc.imperative"] != 0 {
			t.Fatalf("imperative %q flagged: %+v", desc, got)
		}
	}
}

// R12.1: description length — hard bound at 1024, advice at 250.
func TestLintDescriptionLength(t *testing.T) {
	got := Lint("pdf-tools", LintInput{
		Name:        "pdf-tools",
		Description: "Use when " + strings.Repeat("x", 300),
		Body:        "x",
		Files:       1,
	})
	rules := lintRules(got)
	if rules["desc.advise"] != 1 || rules["desc.length"] != 0 {
		t.Fatalf("advise %v", rules)
	}
	got = Lint("pdf-tools", LintInput{
		Name:        "pdf-tools",
		Description: "Use when " + strings.Repeat("x", 1100),
		Body:        "x",
		Files:       1,
	})
	if lintRules(got)["desc.length"] != 1 {
		t.Fatalf("length %+v", got)
	}
}

// R12.1: name must equal the directory and use the agentskills charset.
func TestLintNameChecks(t *testing.T) {
	got := Lint("pdf-tools", LintInput{Name: "pdf_tools", Description: "Use when x", Body: "x", Files: 1})
	rules := lintRules(got)
	if rules["name.charset"] != 1 || rules["name.mismatch"] != 1 {
		t.Fatalf("name rules %v", rules)
	}
}

// R12.1: oversized bodies and trees past the file bound are flagged.
func TestLintBodyBounds(t *testing.T) {
	got := Lint("pdf-tools", LintInput{
		Name: "pdf-tools", Description: "Use when x",
		Body:  strings.Repeat("line\n", 501),
		Files: 61,
	})
	rules := lintRules(got)
	if rules["body.lines"] != 1 || rules["tree.files"] != 1 {
		t.Fatalf("bounds %v", rules)
	}
}

// R12.1: a run log posing as a skill is an incident-log shape, not guidance.
func TestLintIncidentLogShape(t *testing.T) {
	var body strings.Builder
	for i := 0; i < 10; i++ {
		body.WriteString("- 2026-01-0" + string(rune('0'+i)) + " run attempt failed\n")
	}
	got := Lint("pdf-tools", LintInput{
		Name: "pdf-tools", Description: "Use when x",
		Body:  body.String(),
		Files: 1,
	})
	if lintRules(got)["shape.incident_log"] != 1 {
		t.Fatalf("incident log %+v", got)
	}
}

// An absent description must not panic the imperative check.
func TestLintEmptyDescription(t *testing.T) {
	got := Lint("x", LintInput{Name: "x", Body: "x", Files: 1})
	if lintRules(got)["desc.imperative"] != 1 {
		t.Fatalf("empty description %+v", got)
	}
}

// R12.1: links into references/ must point at files in the tree and stay
// one level deep — a dead or nested path is an advisory finding.
func TestLintReferencesExistOneLevelDeep(t *testing.T) {
	paths := []string{"SKILL.md", "references/guide.md"}
	got := Lint("pdf", LintInput{
		Name: "pdf", Description: "Use when extracting PDFs",
		Body:  "see [guide](references/guide.md) and [deep](references/a/b.md) and [lost](references/missing.md)",
		Files: 2, Paths: paths,
	})
	rules := lintRules(got)
	if rules["refs.missing"] != 1 || rules["refs.depth"] != 1 {
		t.Fatalf("refs findings %+v", got)
	}
	got = Lint("pdf", LintInput{
		Name: "pdf", Description: "Use when extracting PDFs",
		Body:  "see [guide](references/guide.md)",
		Files: 2, Paths: paths,
	})
	if len(got) != 0 {
		t.Fatalf("existing shallow reference must be clean: %+v", got)
	}
}
