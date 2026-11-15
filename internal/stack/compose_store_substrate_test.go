package stack

import (
	"os"
	"path/filepath"
	"testing"
)

// The rendered ToolHub store path follows the activated substrate: while
// store.json still exists the service keeps the JSON backend; once the
// operator retires it to .legacy with store.db verified in place, the
// compose env switches to the SQLite path.
func TestComposeToolHubStoreSubstrateSwitch(t *testing.T) {
	s := Settings{Schema: 1, User: "alice", Environment: "dev", Timezone: "UTC", OAuthPort: 8000, BrowserPort: 6080}
	storeDir := func(dir string) string { return filepath.Join(dir, "runtime", "toolhub") }
	storePath := func(dir string) string {
		return Compose(s, t.TempDir(), dir)["services"].(M)["toolhub"].(M)["environment"].(M)["HUB_TOOLHUB_STORE"].(string)
	}

	legacy := t.TempDir()
	if got := storePath(legacy); got != "/state/toolhub/store.json" {
		t.Fatalf("fresh space must default to the JSON backend: %s", got)
	}

	migrated := t.TempDir()
	if err := os.MkdirAll(storeDir(migrated), 0700); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"store.json", "store.db"} {
		if err := os.WriteFile(filepath.Join(storeDir(migrated), name), []byte("{}"), 0600); err != nil {
			t.Fatal(err)
		}
	}
	if got := storePath(migrated); got != "/state/toolhub/store.json" {
		t.Fatalf("unactivated space keeps the JSON backend: %s", got)
	}

	if err := os.Rename(filepath.Join(storeDir(migrated), "store.json"), filepath.Join(storeDir(migrated), "store.json.legacy")); err != nil {
		t.Fatal(err)
	}
	if got := storePath(migrated); got != "/state/toolhub/store.db" {
		t.Fatalf("activated space must switch to the SQLite backend: %s", got)
	}
}
