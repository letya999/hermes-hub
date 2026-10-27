package skillstore

import (
	"fmt"
	"slices"
	"strings"

	"github.com/letya999/hermes-hub/internal/skillstore/scan"
)

// maxReportedFindings bounds the findings list stored on drafts and shown
// in errors (R4.3: reports are bounded, content-free).
const maxReportedFindings = 100

// scanTree converts a skill tree into scan inputs and runs the scanner.
func scanTree(tree Tree) scan.Report {
	paths := make([]string, 0, len(tree))
	for p := range tree {
		paths = append(paths, p)
	}
	slices.Sort(paths)
	inputs := make([]scan.Input, len(paths))
	for i, p := range paths {
		inputs[i] = scan.Input{Path: p, Data: tree[p].Data, Exec: tree[p].Exec}
	}
	return scan.Scan(inputs)
}

// boundFindings caps a report for storage in metadata.
func boundFindings(r scan.Report) []scan.Finding {
	if len(r.Findings) > maxReportedFindings {
		return append([]scan.Finding{}, r.Findings[:maxReportedFindings]...)
	}
	return r.Findings
}

// scanError renders a critical report as a refusing error. Only file, line
// and rule ids are included — never content.
func scanError(r scan.Report) error {
	parts := make([]string, 0, 5)
	for _, f := range r.Findings {
		if f.Severity != scan.Critical {
			continue
		}
		parts = append(parts, fmt.Sprintf("%s:%d %s", f.File, f.Line, f.RuleID))
		if len(parts) == 5 {
			break
		}
	}
	return fmt.Errorf("%w: %s", ErrScanCritical, strings.Join(parts, "; "))
}

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
