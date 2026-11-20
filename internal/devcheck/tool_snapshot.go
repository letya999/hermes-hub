package devcheck

import (
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"

	"github.com/letya999/hermes-hub/internal/agenttools"
	"github.com/letya999/hermes-hub/internal/toolhub/api"
)

const toolSnapshotPath = "internal/devcheck/testdata/tool-catalog.snapshot"

func toolCatalogSnapshot() ([]byte, error) {
	definition := agenttools.HubToolsDefinition()
	lines := []string{"# " + definition.DefinitionID + "@" + definition.Version, "# name schema_sha256 effect required_rights"}
	tools := slices.Clone(definition.Tools)
	slices.SortFunc(tools, func(a, b api.ToolSpec) int { return strings.Compare(a.Name, b.Name) })
	for _, tool := range tools {
		var schema any = api.CliInputSchema(tool)
		if len(tool.InputSchema) > 0 {
			if err := json.Unmarshal(tool.InputSchema, &schema); err != nil {
				return nil, fmt.Errorf("schema %s: %w", tool.Name, err)
			}
		}
		canonical, err := json.Marshal(schema)
		if err != nil {
			return nil, err
		}
		rights := slices.Clone(tool.Requires)
		slices.Sort(rights)
		lines = append(lines, fmt.Sprintf("%s %x %s %s", tool.Name, sha256.Sum256(canonical), tool.Effect, strings.Join(rights, ",")))
	}
	return []byte(strings.Join(lines, "\n") + "\n"), nil
}

// WriteToolCatalogSnapshot refuses to replace an existing contract at the
// same catalog version. A tool-contract change must bump the immutable catalog.
func WriteToolCatalogSnapshot(root string) error {
	current, err := toolCatalogSnapshot()
	if err != nil {
		return err
	}
	path := filepath.Join(root, filepath.FromSlash(toolSnapshotPath))
	previous, err := os.ReadFile(path)
	if err != nil && !os.IsNotExist(err) {
		return err
	}
	if err == nil && string(previous) != string(current) && snapshotVersion(previous) == snapshotVersion(current) {
		return fmt.Errorf("tool snapshot changed without a hub-agent-tools version bump")
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	return os.WriteFile(path, current, 0o600)
}

func snapshotVersion(body []byte) string { line, _, _ := strings.Cut(string(body), "\n"); return line }
