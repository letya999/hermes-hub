package passport_test

import (
	"strings"
	"testing"

	"github.com/letya999/hermes-hub/internal/passport"
	"gopkg.in/yaml.v3"
)

// typedWorkspace is the normative platform layout (SPEC-0092 R5.1):
// inbox/service-artifacts nodes are mode ro, parametrized subtrees carry a
// placeholder, human notes annotate the map.
func typedWorkspace() map[string]any {
	return map[string]any{
		"writable": []any{
			"/workspace/work", "/workspace/projects",
			"/workspace/artifacts/documents", "/workspace/artifacts/images",
			"/workspace/artifacts/videos", "/workspace/artifacts/archives",
			"/workspace/outbox", "/vault/library", "/vault/attachments",
		},
		"read_only": []any{"/config", "/skills"},
		"layout": []any{
			map[string]any{"path": "/workspace/inbox", "note": "incoming from outside", "mode": "ro"},
			map[string]any{"path": "/workspace/inbox/people", "placeholder": "<channel>/<date>/<attachment-id>"},
			map[string]any{"path": "/workspace/inbox/services", "placeholder": "<service>/<date>/<attachment-id>"},
			map[string]any{"path": "/workspace/work", "note": "working files"},
			map[string]any{"path": "/workspace/projects", "note": "projects incl. nested Git repos"},
			map[string]any{"path": "/workspace/artifacts", "note": "agent/service output"},
			"/workspace/artifacts/documents",
			"/workspace/artifacts/images",
			"/workspace/artifacts/videos",
			"/workspace/artifacts/archives",
			map[string]any{"path": "/workspace/artifacts/services", "mode": "ro", "placeholder": "<service>"},
			map[string]any{"path": "/workspace/outbox", "note": "prepared for sending"},
		},
		"mounts": map[string]any{
			"/shared/acme-docs": map[string]any{"source": "org/acme/docs", "mode": "ro"},
		},
	}
}

// treeWorkspaceCompile replaces the fixture's ceiling workspace doc with ws
// so tree assertions run against the real Compile path.
func treeWorkspaceCompile(t *testing.T, ws map[string]any) (passport.Revision, error) {
	t.Helper()
	in := fixtureInputs(t)
	in.PlatformCeiling.Docs["workspace"] = ws
	for _, l := range []*passport.Layer{&in.PlatformTemplate, &in.OrgCeiling, &in.OrgTemplate, &in.User} {
		delete(l.Docs, "workspace")
	}
	return passport.Compile(in)
}

func workspaceDoc(t *testing.T, rev passport.Revision) map[string]any {
	t.Helper()
	var out map[string]any
	if err := yaml.Unmarshal(rev.Docs["workspace"], &out); err != nil {
		t.Fatalf("unmarshal workspace: %v", err)
	}
	return out
}

func TestWorkspaceTreeASCIIMatchesNormative(t *testing.T) {
	rev, err := treeWorkspaceCompile(t, typedWorkspace())
	if err != nil {
		t.Fatalf("Compile: %v", err)
	}
	doc := workspaceDoc(t, rev)
	tree, _ := doc["tree"].(string)
	for _, want := range []string{
		"/\n",
		"├── config/",
		"├── workspace/",
		"│   ├── inbox/",
		"│   │   ├── people/<channel>/<date>/<attachment-id>/",
		"│   ├── work/",
		"│   ├── artifacts/",
		"│   │   ├── documents/",
		"│   │   └── services/<service>/",
		"│   └── outbox/",
		"├── vault/",
		"│   ├── memory/",
		"│   ├── library/",
		"│   └── attachments/",
		"├── skills/",
		"└── shared/",
		"    └── acme-docs/",
		"working files",
		"incoming from outside",
	} {
		if !strings.Contains(tree, want) {
			t.Errorf("tree missing %q:\n%s", want, tree)
		}
	}
	if !strings.Contains(tree, "[read-only]") {
		t.Errorf("tree carries no read-only marker:\n%s", tree)
	}
	if strings.Contains(tree, "work/   [read-only]") || strings.Contains(tree, "outbox/   [read-only]") {
		t.Errorf("writable subtree marked read-only:\n%s", tree)
	}
	// The typed layout itself is a compile source — not emitted.
	if _, ok := doc["layout"]; ok {
		t.Error("workspace.yaml still emits the layout source field")
	}
	// Invariant ro paths land in the emitted read_only even when the source
	// lists only /config and /skills.
	ro, _ := doc["read_only"].([]any)
	set := map[string]bool{}
	for _, p := range ro {
		s, _ := p.(string)
		set[s] = true
	}
	for _, inv := range []string{"/workspace/inbox", "/workspace/artifacts/services", "/vault/memory", "/shared/acme-docs"} {
		if !set[inv] {
			t.Errorf("emitted read_only misses invariant/ro path %s: %v", inv, ro)
		}
	}
}
