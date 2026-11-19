package stack

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/letya999/hermes-hub/internal/passportview"
)

func TestPreparedComposeKeepsCurrentPrincipalVaultMount(t *testing.T) {
	root := filepath.Join(t.TempDir(), "spaces")
	dir := filepath.Join(root, "infra")
	docs := managedDocs("infra")
	docs["config"] += "memory: {enabled: true}\n"
	publishManagedDocs(t, dir, "infra", "r-0123456789abcdef", docs)
	if err := os.MkdirAll(filepath.Join(dir, "vault"), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "generated", "passports", "current"), []byte("pending\n"), 0600); err != nil {
		t.Fatal(err)
	}
	enabled := true
	s := Settings{Schema: 3, User: "infra", Environment: "dev", PassportRevision: "r-candidate", PassportConfig: &passportview.Config{Memory: passportview.Memory{Enabled: &enabled}}}
	if got := RuntimeService(s, dir, dir)["environment"].(M)["HUB_PASSPORT_REVISION"]; got != s.PassportRevision {
		t.Fatalf("runtime revision pin = %v", got)
	}
	toolhub := Compose(s, dir, dir)["services"].(M)["toolhub"].(M)
	for _, raw := range toolhub["volumes"].([]any) {
		if raw.(M)["target"] == "/vaults-by/infra" {
			return
		}
	}
	t.Fatal("prepared compose lost the current principal's vault mount while current was pending")
}
