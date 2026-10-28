package toolhub

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/letya999/hermes-hub/internal/toolhub/api"
)

// The managed projection injects the eligible-names enum into skill.name from
// the mounted effective index (SPEC-0083 R5.1); the executor re-validates.
func TestProjectedSkillToolCarriesNameEnum(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "index.json"), []byte(`{"skills":[{"name":"demo-skill","description":"d","source":"user","revision":"r"}]}`), 0o644); err != nil {
		t.Fatal(err)
	}
	t.Setenv("HUB_SKILLS_EFFECTIVE", dir)
	g := &Gateway{}
	raw := g.projectedToolSchema(api.ToolSpec{Name: "skill", Arguments: []api.CLIArgument{{Name: "name", Type: "string", Required: true}, {Name: "args", Type: "string"}}})
	schema, _ := raw.(map[string]any)
	enum, _ := schema["properties"].(map[string]any)["name"].(map[string]any)["enum"].([]any)
	if len(enum) != 1 || enum[0] != "demo-skill" {
		t.Fatalf("skill.name enum must come from the effective index: %v", enum)
	}
}
