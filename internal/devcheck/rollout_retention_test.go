package devcheck

import (
	"os"
	"path/filepath"
	"testing"
)

func TestCleanupRetainsRolloutPins(t *testing.T) {
	root := t.TempDir()
	dir := filepath.Join(root, "spaces", "me", "generated")
	if err := os.MkdirAll(dir, 0700); err != nil {
		t.Fatal(err)
	}
	body := `{"envs":{"prod":{"current":{"image":"hermes-hub:current"},"previous":{"image":"hermes-hub:previous"},"canary":{"image":"hermes-hub:canary","context_id":"me"}}}}`
	if err := os.WriteFile(filepath.Join(dir, "image-pins.json"), []byte(body), 0600); err != nil {
		t.Fatal(err)
	}
	keep, err := activeImageTags(root)
	if err != nil {
		t.Fatal(err)
	}
	for _, tag := range []string{"current", "previous", "canary"} {
		if !keep[tag] {
			t.Errorf("cleanup would delete %s", tag)
		}
	}
	if err := os.WriteFile(filepath.Join(dir, "image-pins.json"), []byte("{"), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := activeImageTags(root); err == nil {
		t.Fatal("cleanup must refuse unreadable retention state")
	}
}
