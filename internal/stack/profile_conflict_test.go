package stack

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestProfilesRejectConflictingGroups(t *testing.T) {
	for _, pair := range []struct{ field, first, second string }{
		{"model", "first", "second"},
		{"model_url", "https://first.invalid/v1", "https://second.invalid/v1"},
		{"cliproxy", "shared", "dedicated"},
		{"cliproxy_credentials", "shared", "private"},
	} {
		t.Run(pair.field, func(t *testing.T) {
			dir := filepath.Join(t.TempDir(), "spaces", "alice")
			if err := os.MkdirAll(dir, 0700); err != nil {
				t.Fatal(err)
			}
			saveYAML(t, filepath.Join(filepath.Dir(dir), "profiles.yaml"), map[string]any{"profiles": map[string]any{
				"a": map[string]any{"owners": []string{"alice"}, pair.field: pair.first},
				"z": map[string]any{"owners": []string{"alice"}, pair.field: pair.second},
			}})
			for _, own := range []string{"", "owner-model"} {
				s := Settings{User: "alice", Model: own}
				err := applyProfiles(&s, dir)
				if err == nil || !strings.Contains(err.Error(), "conflicting") {
					t.Fatalf("ambiguous group assignment accepted with owner override %q: %v", own, err)
				}
			}
			s := Settings{User: "bob"}
			if err := applyProfiles(&s, dir); err != nil {
				t.Fatalf("unrelated owner rejected: %v", err)
			}
		})
	}
}
