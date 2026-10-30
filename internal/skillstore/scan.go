package skillstore

import (
	"slices"
	"strings"

	"github.com/letya999/hermes-hub/internal/skillstore/scan"
)

// maxReportedFindings bounds the findings list stored on drafts and shown
// in errors (R4.3: reports are bounded, content-free).
const maxReportedFindings = 100

// scanTree converts a skill tree into scan inputs, runs the scanner and
// appends advisory lint findings (R12.1) under the skill's dir name.
func scanTree(name string, tree Tree) scan.Report {
	paths := make([]string, 0, len(tree))
	for p := range tree {
		paths = append(paths, p)
	}
	slices.Sort(paths)
	inputs := make([]scan.Input, len(paths))
	for i, p := range paths {
		inputs[i] = scan.Input{Path: p, Data: tree[p].Data, Exec: tree[p].Exec}
	}
	r := scan.Scan(inputs)
	if m, _, err := ParseSkillMD(name, tree[SkillFile].Data, true); err == nil {
		r.Findings = append(r.Findings, scan.Lint(name, scan.LintInput{
			Name:        m.Name,
			Description: m.Description,
			Body:        string(tree[SkillFile].Data),
			Files:       len(tree),
			Paths:       paths,
		})...)
	}
	return r
}

// boundFindings caps a report for storage in metadata.
func boundFindings(r scan.Report) []scan.Finding {
	if len(r.Findings) > maxReportedFindings {
		return append([]scan.Finding{}, r.Findings[:maxReportedFindings]...)
	}
	return r.Findings
}

// scanError rendered a critical report as a refusing error; it returns
// when the publish-time critical block does (R4.4 suspended for MVP —
// findings stay advisory on the draft card until scanner rules are tuned).

// hasScripts reports whether the tree carries executable script files —
// such skills may only publish through the draft+approve path (R4.4).
func hasScripts(tree Tree) bool {
	for p := range tree {
		if strings.HasPrefix(p, "scripts/") {
			return true
		}
	}
	return false
}
