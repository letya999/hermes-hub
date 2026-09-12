package migration

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestNativeCronExportIsBoundedPinnedReadOnlyAndSecretFree(t *testing.T) {
	dir := t.TempDir()
	if err := os.Mkdir(filepath.Join(dir, "hermes"), 0700); err != nil {
		t.Fatal(err)
	}
	for _, scenario := range []string{"empty", "active", "wrong-pin", "corrupt", "negative", "failure", "oversized"} {
		t.Run(scenario, func(t *testing.T) {
			export, err := InspectNativeCron(context.Background(), dir, "pinned:test", func(_ context.Context, args ...string) ([]byte, error) {
				joined := strings.Join(args, " ")
				for _, want := range []string{"--network none", "--read-only", "dst=/source,readonly", "/state:size=64m", "list_jobs(include_disabled=False)", "pinned:test"} {
					if !strings.Contains(joined, want) {
						t.Fatalf("unsafe native export: missing %s", want)
					}
				}
				if strings.Contains(joined, "runtime.auth") || strings.Contains(joined, "--env-file") || strings.Contains(joined, "cron pause") {
					t.Fatal("export touches credentials or mutates schedules")
				}
				if scenario == "failure" {
					return nil, errors.New("unavailable")
				}
				if scenario == "corrupt" {
					return []byte("{broken"), nil
				}
				if scenario == "oversized" {
					return make([]byte, 16*1024+1), nil
				}
				value := NativeCronSnapshot{HermesPin: HermesMigrationPin}
				if scenario == "active" {
					value.ActiveJobs = 1
				}
				if scenario == "negative" {
					value.ActiveJobs = -1
				}
				if scenario == "wrong-pin" {
					value.HermesPin = "unverified"
				}
				return json.Marshal(value)
			})
			if scenario == "empty" || scenario == "active" {
				if err != nil {
					t.Fatal(err)
				}
				if scenario == "active" && export.ActiveJobs != 1 {
					t.Fatal("active clock ignored")
				}
			} else if err == nil {
				t.Fatal("unverified export accepted")
			}
		})
	}
}
