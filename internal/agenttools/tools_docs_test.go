package agenttools

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

// P0 tool-contract discipline: a cataloged tool must have a docs entry in
// docs/tools.md — a tool shipped without documentation fails the suite.
// Wildcard doc rows (`ssh_shell_*`) cover every tool sharing the prefix.
func TestCatalogToolsDocumented(t *testing.T) {
	body, err := os.ReadFile(filepath.Join("..", "..", "docs", "tools.md"))
	if err != nil {
		t.Fatal(err)
	}
	tokens := map[string]bool{}
	for _, m := range regexp.MustCompile("`([a-z_]+\\*?)`").FindAllStringSubmatch(string(body), -1) {
		tokens[m[1]] = true
	}
	for _, spec := range HubToolsDefinition().Tools {
		if tokens[spec.Name] {
			continue
		}
		covered := false
		for token := range tokens {
			if strings.HasSuffix(token, "*") && strings.HasPrefix(spec.Name, strings.TrimSuffix(token, "*")) {
				covered = true
				break
			}
		}
		if !covered {
			t.Errorf("%s missing from docs/tools.md", spec.Name)
		}
	}
}
