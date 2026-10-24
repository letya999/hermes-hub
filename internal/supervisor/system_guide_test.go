package supervisor

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestSpawnRefreshesSystemGuideContractForExistingContexts(t *testing.T) {
	t.Setenv("HUB_ENV", "prod")
	for _, managed := range []bool{false, true} {
		m, root := testManager(t, func(context.Context, ...string) ([]byte, error) { return nil, nil }, nil)
		if managed {
			writeManagedFixtures(t, root)
		}
		persona := "# Owner persona\nRemain concise."
		if err := os.WriteFile(filepath.Join(root, "SOUL.md"), []byte(persona), 0600); err != nil {
			t.Fatal(err)
		}
		if err := os.MkdirAll(filepath.Join(root, "generated"), 0755); err != nil {
			t.Fatal(err)
		}
		effective := filepath.Join(root, "generated", "soul-effective.prod.md")
		if err := os.WriteFile(effective, []byte("old generated contract"), 0644); err != nil {
			t.Fatal(err)
		}
		args, err := m.runArgs(binding(root), "guide-fixture", 19000)
		if err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(strings.Join(args, " "), "src="+effective+",dst=/state/hermes/SOUL.md,readonly") {
			t.Fatal("generated contract not mounted")
		}
		body, err := os.ReadFile(effective)
		if err != nil || !strings.Contains(string(body), "hermes-hub-system-guide") || !strings.HasPrefix(string(body), persona) {
			t.Fatalf("managed=%v: existing context retained stale contract", managed)
		}
		body, err = os.ReadFile(filepath.Join(root, "SOUL.md"))
		if err != nil || string(body) != persona {
			t.Fatal("spawn changed owner persona")
		}
	}
}
