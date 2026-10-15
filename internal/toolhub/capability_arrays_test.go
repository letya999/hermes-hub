package toolhub

import (
	"github.com/letya999/hermes-hub/internal/toolhub/api"
	"testing"
)

func TestCapabilityArrayChecksEveryPath(t *testing.T) {
	_, _, policy, profile := managedStore(t)
	selection := profile.Selections[0]
	tool := api.ToolSpec{CapabilityID: selection.CapabilityID, Uses: []api.CapabilityUse{{Action: "read", Resource: "workspace", PathArgument: "paths"}}}
	args := map[string]any{"paths": []any{"docs/a.txt", "docs/b.txt"}}
	_, scopes, ok := api.CapabilityDecision(profile, policy, selection, tool, args, false)
	if !ok || len(scopes) != 2 || scopes[0].PathPrefix != "docs" || scopes[1].PathPrefix != "docs" {
		t.Fatalf("valid array denied: %v %v", ok, scopes)
	}
	for _, bad := range []any{[]any{"docs/a.txt", "secret.txt"}, []any{"docs/a.txt", 1}, []string{}, []string{"/etc/passwd"}, []string{"docs/../secret.txt"}, nil} {
		args["paths"] = bad
		if _, _, ok := api.CapabilityDecision(profile, policy, selection, tool, args, false); ok {
			t.Fatalf("unadmitted item accepted: %v", bad)
		}
	}
}
