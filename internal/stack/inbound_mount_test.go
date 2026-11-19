package stack

import (
	"os"
	"path/filepath"
	"testing"
)

func TestComposeMountsOwnerInboxesForControlFetch(t *testing.T) {
	spaces := filepath.Join(t.TempDir(), "spaces")
	for _, owner := range []string{"alice", "bob"} {
		if err := os.MkdirAll(filepath.Join(spaces, owner, "managed", "dev", "workspace", "inbox"), 0700); err != nil {
			t.Fatal(err)
		}
	}
	dir := filepath.Join(spaces, "alice")
	s := Settings{Schema: 1, Environment: "dev", User: "alice", Timezone: "UTC"}
	services := Compose(s, dir, dir)["services"].(M)
	toolhub := services["toolhub"].(M)
	if toolhub["environment"].(M)["HUB_INBOX_BY_PRINCIPAL"] != "/inboxes-by" {
		t.Fatal("ToolHub lacks owner inbox root")
	}
	for _, owner := range []string{"alice", "bob"} {
		found := false
		for _, raw := range toolhub["volumes"].([]any) {
			volume := raw.(M)
			if volume["target"] == "/inboxes-by/"+owner {
				if volume["read_only"] == true || volume["source"] != filepath.ToSlash(filepath.Join(spaces, owner, "managed", "dev", "workspace", "inbox")) {
					t.Fatalf("wrong owner inbox mount: %v", volume)
				}
				found = true
			}
		}
		if !found {
			t.Fatalf("missing owner inbox mount for %s", owner)
		}
	}
	if got := RuntimeService(s, dir, dir)["environment"].(M)["HUB_WORKSPACE_RO"]; got == "" {
		t.Fatal("runtime inbox lost read-only boundary")
	}
}
