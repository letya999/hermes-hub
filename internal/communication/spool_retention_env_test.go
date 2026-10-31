package communication

import (
	"testing"
	"time"
)

// Retention and bound knobs land in the config from env on both load paths,
// so a deployment cannot silently run unbounded.
func TestSpoolLimitsFromEnv(t *testing.T) {
	t.Setenv("HUB_USER_ID", "me")
	t.Setenv("HUB_ORGANIZATION_ID", "acme")
	t.Setenv("TELEGRAM_ALLOWED_USERS", "42")
	t.Setenv("HUB_SPOOL_MAX_FILES", "300")
	t.Setenv("HUB_SPOOL_MAX_ARCHIVE_BYTES", "1048576")
	t.Setenv("HUB_SPOOL_MAX_USAGE_ENTRIES", "4096")
	t.Setenv("HUB_SPOOL_RETENTION", "")
	cfg, err := ConfigFromEnv()
	if err != nil {
		t.Fatal(err)
	}
	if cfg.SpoolMaxFiles != 300 || cfg.SpoolMaxArchiveBytes != 1048576 || cfg.SpoolMaxUsageEntries != 4096 {
		t.Fatalf("limits not parsed: %+v", cfg)
	}
	if cfg.SpoolRetention != 72*time.Hour {
		t.Fatalf("retention default not 72h: %v", cfg.SpoolRetention)
	}
}
