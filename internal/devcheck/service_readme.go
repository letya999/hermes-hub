package devcheck

import (
	"bytes"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

const serviceReadmeTemplate = "services/README.template.md"

// serviceReadmeExempt lists services whose README predates the v1 template,
// each with the milestone that brings it to the template. The list is
// shrink-only: an exempt README that already carries every heading fails the
// gate until its entry is removed.
var serviceReadmeExempt = map[string]string{
	// Copied MIT module with its own Russian documentation pipeline
	// (docs_scripts/check.py) and frontmatter rules; M40.13 makes it the only
	// secret owner and rewrites its README from the template.
	"credential-broker": "M40.13",
}

// serviceReadmes enforces that every services/<svc>/README.md contains each
// "## " heading of services/README.template.md as its own line (M40.1,
// docs/engineering.md). The template is the single source of the headings.
func serviceReadmes(root string) []string {
	entries, err := os.ReadDir(filepath.Join(root, "services"))
	if err != nil {
		return nil
	}
	var services []string
	for _, e := range entries {
		if e.IsDir() {
			services = append(services, e.Name())
		}
	}
	if len(services) == 0 {
		return nil
	}
	sort.Strings(services)
	body, err := os.ReadFile(filepath.Join(root, filepath.FromSlash(serviceReadmeTemplate)))
	if err != nil {
		return []string{"missing " + serviceReadmeTemplate}
	}
	var headings []string
	for _, line := range markdownLines(body) {
		if strings.HasPrefix(line, "## ") {
			headings = append(headings, line)
		}
	}
	if len(headings) == 0 {
		return []string{serviceReadmeTemplate + " has no ## headings"}
	}
	var problems []string
	for _, svc := range services {
		rel := "services/" + svc + "/README.md"
		readme, err := os.ReadFile(filepath.Join(root, "services", svc, "README.md")) // #nosec G304 -- service directory inside the project tree
		if err != nil {
			if _, exempt := serviceReadmeExempt[svc]; !exempt {
				problems = append(problems, "missing "+rel)
			}
			continue
		}
		present := map[string]bool{}
		sectionHasContent := map[string]bool{}
		lines := markdownLines(readme)
		current := ""
		for _, line := range lines {
			if strings.HasPrefix(line, "## ") {
				current = line
				continue
			}
			if current != "" && strings.TrimSpace(line) != "" {
				sectionHasContent[current] = true
			}
		}
		for _, line := range lines {
			present[line] = true
		}
		var missing []string
		for _, h := range headings {
			if !present[h] {
				missing = append(missing, h)
			} else if !sectionHasContent[h] {
				problems = append(problems, rel+" has empty section \""+h+"\" — write content or \"None.\"")
			}
		}
		if milestone, exempt := serviceReadmeExempt[svc]; exempt {
			if len(missing) == 0 {
				problems = append(problems, "stale README exemption: "+svc+" follows the template; remove it from serviceReadmeExempt ("+milestone+")")
			}
			continue
		}
		for _, h := range missing {
			problems = append(problems, rel+" lacks heading \""+h+"\" from "+serviceReadmeTemplate)
		}
	}
	return problems
}

// markdownLines splits a file into lines without CR or trailing blanks, so a
// CRLF checkout on Windows compares equal to the LF template.
func markdownLines(body []byte) []string {
	raw := bytes.Split(bytes.ReplaceAll(body, []byte("\r\n"), []byte("\n")), []byte("\n"))
	lines := make([]string, 0, len(raw))
	for _, l := range raw {
		lines = append(lines, strings.TrimRight(string(l), " \t"))
	}
	return lines
}
