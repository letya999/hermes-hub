package toolhub

import (
	"encoding/json"
	"github.com/google/jsonschema-go/jsonschema"
	"testing"
)

func TestCLIInstallSchemaPreservesConditionalSelectors(t *testing.T) {
	_, contract := controlToolContract("prepare_source")
	var check func(any)
	check = func(node any) {
		switch node := node.(type) {
		case map[string]any:
			if required, ok := node["required"].([]string); ok {
				properties, _ := node["properties"].(map[string]any)
				for _, field := range required {
					if properties[field] == nil {
						t.Fatalf("Hermes would prune conditional selector %s", field)
					}
				}
			}
			for _, value := range node {
				check(value)
			}
		case []any:
			for _, value := range node {
				check(value)
			}
		}
	}
	check(contract)
	data, err := json.Marshal(contract)
	if err != nil {
		t.Fatal(err)
	}
	var schema jsonschema.Schema
	if err := json.Unmarshal(data, &schema); err != nil {
		t.Fatal(err)
	}
	resolved, err := schema.Resolve(nil)
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		spec  map[string]any
		valid bool
	}{
		{map[string]any{"command": "bash"}, true},
		{map[string]any{"source": "github-release:jqlang/jq@jq-1.8.1", "binary": "/usr/local/bin/jq"}, true},
		{map[string]any{}, false},
		{map[string]any{"source": "github-release:jqlang/jq@jq-1.8.1"}, false},
		{map[string]any{"command": "bash", "source": "x", "binary": "/x"}, false},
		{map[string]any{"command": "bash", "binary": "/x"}, false},
		{map[string]any{"command": "bash", "unexpected": "x"}, false},
	} {
		tc.spec["name"] = "cli-e2e-contract"
		tc.spec["tools"] = []any{map[string]any{"name": "version", "effect": "read"}}
		err := resolved.Validate(map[string]any{"cli": tc.spec})
		if (err == nil) != tc.valid {
			t.Fatalf("spec=%v valid=%v error=%v", tc.spec, tc.valid, err)
		}
	}
}
