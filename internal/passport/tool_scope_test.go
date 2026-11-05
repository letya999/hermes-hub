package passport

import (
	"strings"
	"testing"

	"github.com/letya999/hermes-hub/internal/catalog"
)

func TestCompileRejectsUnenforceableToolScopes(t *testing.T) {
	docs, err := catalog.Generate()
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		name  string
		entry map[string]any
	}{
		{"host", map[string]any{"via": "toolhub", "tools": map[string]any{"include": []any{"file_read"}}, "scope": map[string]any{"hosts": []any{"example.test"}}}},
		{"url", map[string]any{"via": "toolhub", "tools": map[string]any{"include": []any{"file_read"}}, "scope": map[string]any{"urls": []any{"https://example.test"}}}},
		{"non-file executor", map[string]any{"via": "toolhub", "tools": map[string]any{"include": []any{"project_create"}}, "scope": map[string]any{"paths": []any{"/workspace/docs"}}}},
		{"traversal", map[string]any{"via": "toolhub", "tools": map[string]any{"include": []any{"file_read"}}, "scope": map[string]any{"paths": []any{"/workspace/docs/../private"}}}},
		{"ignored rule", map[string]any{"via": "toolhub", "tools": map[string]any{"include": []any{"file_read"}}, "rules": map[string]any{"file_read": map[string]any{"confirm": true}}}},
		{"excluded executor rule", map[string]any{"via": "toolhub", "tools": map[string]any{"include": []any{"file_read"}}, "rules": map[string]any{"project_create": map[string]any{"scope": map[string]any{"paths": []any{"/workspace/docs"}}}}}},
		{"disjoint member", map[string]any{"via": "toolhub", "tools": map[string]any{"include": []any{"file_read"}}, "scope": map[string]any{"paths": []any{"/workspace/docs"}}, "rules": map[string]any{"file_read": map[string]any{"scope": map[string]any{"paths": []any{"/workspace/private"}}}}}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			in := v2FixtureInputs(t)
			in.Catalog = docs
			in.User.Docs["tools"] = map[string]any{}
			in.PlatformCeiling.Docs["tools"] = map[string]any{"extended": map[string]any{"files": tc.entry}}
			if _, err := Compile(in); err == nil || !strings.Contains(err.Error(), "passport compile") {
				t.Fatalf("unenforceable scope compiled: %v", err)
			}
		})
	}
}

func TestCompileReadOnlyFileScopeSkipsWriteExecutors(t *testing.T) {
	in := v2FixtureInputs(t)
	in.User.Docs["tools"] = map[string]any{}
	in.PlatformCeiling.Docs["tools"] = map[string]any{"extended": map[string]any{"files": map[string]any{
		"via": "toolhub", "access": "read", "scope": map[string]any{"paths": []any{"/workspace/docs"}},
	}}}
	if _, err := Compile(in); err != nil {
		t.Fatalf("read-only file scope rejected because of unselected writes: %v", err)
	}
}
