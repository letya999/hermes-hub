package log_test

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"

	"github.com/letya999/hermes-hub/shared/log"
)

func TestLoggerWritesServiceJSONAtConfiguredLevel(t *testing.T) {
	t.Setenv(log.LevelEnv, "warn")
	var out bytes.Buffer
	logger := log.New(&out, "task-engine")
	logger.Info("hidden")
	logger.Warn("shown", "job", "j1")
	lines := strings.Split(strings.TrimSpace(out.String()), "\n")
	if len(lines) != 1 {
		t.Fatalf("level filter: %q", out.String())
	}
	var record map[string]any
	if err := json.Unmarshal([]byte(lines[0]), &record); err != nil {
		t.Fatal(err)
	}
	if record["service"] != "task-engine" || record["msg"] != "shown" || record["level"] != "WARN" || record["job"] != "j1" {
		t.Fatalf("record: %v", record)
	}
	t.Setenv(log.LevelEnv, "nonsense")
	out.Reset()
	log.New(&out, "x").Info("info survives a bad level")
	if !strings.Contains(out.String(), "info survives") {
		t.Fatalf("bad level must fall back to info: %q", out.String())
	}
}
