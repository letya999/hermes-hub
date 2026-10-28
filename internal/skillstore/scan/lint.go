package scan

import (
	"regexp"
	"strings"
)

// LintInput carries the parsed manifest fields plus tree shape a lint pass
// needs (R12.1). Findings are advisory — they never block publication.
type LintInput struct {
	Name        string
	Description string
	Body        string
	Files       int
	Paths       []string
}

// Lint returns advisory findings for one skill tree (R12.1). Everything
// here is guidance — Low/Info severities never fail the scan verdict.
func Lint(dir string, in LintInput) []Finding {
	var out []Finding
	desc := strings.TrimSpace(in.Description)
	if strings.HasPrefix(strings.ToLower(desc), "this skill") {
		out = append(out, Finding{RuleID: "desc.prefix", Severity: Low, File: "SKILL.md", Line: 2})
	}
	if !imperativeOpener(desc) {
		out = append(out, Finding{RuleID: "desc.imperative", Severity: Low, File: "SKILL.md", Line: 2})
	}
	if len(desc) > 1024 {
		out = append(out, Finding{RuleID: "desc.length", Severity: Medium, File: "SKILL.md", Line: 2})
	} else if len(desc) > 250 {
		out = append(out, Finding{RuleID: "desc.advise", Severity: Info, File: "SKILL.md", Line: 2})
	}
	if !nameCharset.MatchString(in.Name) {
		out = append(out, Finding{RuleID: "name.charset", Severity: Medium, File: "SKILL.md", Line: 2})
	}
	if in.Name != dir {
		out = append(out, Finding{RuleID: "name.mismatch", Severity: Medium, File: "SKILL.md", Line: 2})
	}
	if strings.Count(in.Body, "\n") > 500 {
		out = append(out, Finding{RuleID: "body.lines", Severity: Low, File: "SKILL.md"})
	}
	if in.Files > 60 {
		out = append(out, Finding{RuleID: "tree.files", Severity: Low, File: "SKILL.md"})
	}
	stamped := 0
	for _, line := range strings.Split(in.Body, "\n") {
		if incidentLogLine.MatchString(line) {
			stamped++
		}
	}
	if stamped >= 5 {
		out = append(out, Finding{RuleID: "shape.incident_log", Severity: Medium, File: "SKILL.md"})
	}
	present := map[string]bool{}
	for _, p := range in.Paths {
		present[p] = true
	}
	var missing, deep bool
	for _, m := range refLink.FindAllStringSubmatch(in.Body, -1) {
		if strings.Count(m[1], "/") > 1 {
			deep = true
		} else if !present[m[1]] {
			missing = true
		}
	}
	if missing {
		out = append(out, Finding{RuleID: "refs.missing", Severity: Low, File: "SKILL.md"})
	}
	if deep {
		out = append(out, Finding{RuleID: "refs.depth", Severity: Low, File: "SKILL.md"})
	}
	return out
}

// refLink catches inline markdown links into references/.
var refLink = regexp.MustCompile(`\]\((references/[^)\s]+)\)`)

// incidentLogLine matches run-log entries: bulleted timestamped lines.
var incidentLogLine = regexp.MustCompile(`^\s*[-*]\s*20\d\d-\d\d-\d\d\b`)

// nameCharset mirrors the store's agentskills.io name pattern (scan is a
// leaf package and cannot import skillstore back).
var nameCharset = regexp.MustCompile(`^[a-z0-9]+(-[a-z0-9]+)*$`)

// nonImperativeOpeners are first words that cannot head an imperative
// ("Use when …") description: determiners, pronouns and noun openers.
var nonImperativeOpeners = map[string]bool{
	"this": true, "the": true, "a": true, "an": true, "helps": true,
}

// imperativeOpener reports whether the description plausibly opens with an
// imperative verb: base-form verbs never end in -ed and are never
// determiner/noun openers.
func imperativeOpener(desc string) bool {
	fields := strings.Fields(desc)
	if len(fields) == 0 {
		return false
	}
	first := strings.ToLower(fields[0])
	return !nonImperativeOpeners[first] && !strings.HasSuffix(first, "ed")
}
