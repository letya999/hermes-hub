package diagnostics

import (
	"bytes"
	"encoding/json"
	"log"
	"strings"
	"testing"
	"time"
)

// SPEC-0053 R3.5: lifecycle logs are structured JSON with the fields a trace
// needs to join supervisor, runtime and space records.
func TestSlogJSONHasRequiredFields(t *testing.T) {
	var buf bytes.Buffer
	previous := log.Writer()
	log.SetOutput(&buf)
	defer log.SetOutput(previous)

	logger := Logger("supervisor")
	logger.Info("job admitted", JobFields("job-1", "run-9", "alice")...)

	line := strings.TrimSpace(buf.String())
	var record map[string]any
	if err := json.Unmarshal([]byte(line), &record); err != nil {
		t.Fatalf("log line is not JSON: %v\n%s", err, line)
	}
	for _, field := range []string{"component", "job_id", "run_id", "principal", "time"} {
		if _, ok := record[field]; !ok {
			t.Fatalf("missing field %q in %s", field, line)
		}
	}
	if record["component"] != "supervisor" || record["job_id"] != "job-1" || record["run_id"] != "run-9" || record["principal"] != "alice" {
		t.Fatalf("wrong field values: %s", line)
	}
	ts, err := time.Parse(time.RFC3339Nano, record["time"].(string))
	if err != nil {
		t.Fatalf("time not RFC3339: %v", err)
	}
	if ts.Location() != time.UTC {
		t.Fatalf("time not UTC: %s", ts.Location())
	}
}
