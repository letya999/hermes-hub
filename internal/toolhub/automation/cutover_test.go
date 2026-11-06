package automation

import (
	"os"
	"path/filepath"
	"testing"
)

func TestValidateLegacySpoolKeepsTransportAndRefusesAutomation(t *testing.T) {
	root := t.TempDir()
	transport := filepath.Join(root, "pending")
	if err := os.Mkdir(transport, 0o700); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(transport, "ordinary.json")
	ordinary := `{"id":"message-job","trigger":"message"}`
	if err := os.WriteFile(path, []byte(ordinary), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := ValidateLegacySpool(root); err != nil {
		t.Fatalf("transport-only spool rejected: %v", err)
	}
	for _, class := range []string{"pending", "running"} {
		t.Run(class+"-cron", func(t *testing.T) {
			dir := filepath.Join(root, class)
			if err := os.MkdirAll(dir, 0o700); err != nil {
				t.Fatal(err)
			}
			record := filepath.Join(dir, "old-cron.json")
			if err := os.WriteFile(record, []byte(`{"id":"old-cron","trigger":"cron"}`), 0o600); err != nil {
				t.Fatal(err)
			}
			if err := ValidateLegacySpool(root); err == nil {
				t.Fatal("pending old cron execution passed cutover preflight")
			}
			if body, err := os.ReadFile(record); err != nil || string(body) != `{"id":"old-cron","trigger":"cron"}` {
				t.Fatal("preflight modified pending old job")
			}
			if err := os.Remove(record); err != nil {
				t.Fatal(err)
			}
		})
	}
	for _, class := range []string{"schedules", "occurrences", "assignments", "watchers", "digests", "event-state"} {
		t.Run(class, func(t *testing.T) {
			if err := os.Mkdir(filepath.Join(root, class), 0o700); err != nil {
				t.Fatal(err)
			}
			record := filepath.Join(root, class, "existing.json")
			if err := os.WriteFile(record, []byte("existing record"), 0o600); err != nil {
				t.Fatal(err)
			}
			if err := ValidateLegacySpool(root); err == nil {
				t.Fatal("populated shared legacy class was accepted")
			}
			if body, err := os.ReadFile(record); err != nil || string(body) != "existing record" {
				t.Fatalf("legacy record changed: %q, %v", body, err)
			}
			if err := os.Remove(record); err != nil {
				t.Fatal(err)
			}
			if err := os.Remove(filepath.Join(root, class)); err != nil {
				t.Fatal(err)
			}
		})
	}
	marker := filepath.Join(root, "records.activated")
	if err := os.WriteFile(marker, []byte("activated"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := ValidateLegacySpool(root); err == nil {
		t.Fatal("activated shared spool was accepted")
	}
	if body, err := os.ReadFile(path); err != nil || string(body) != ordinary {
		t.Fatalf("transport record changed: %q, %v", body, err)
	}
}
