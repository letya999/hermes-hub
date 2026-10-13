package e2e

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/letya999/hermes-hub/internal/communication"
)

func TestMeteringRejectsDuplicatesAndMalformedJournal(t *testing.T) {
	root := t.TempDir()
	if entries, err := usageRecords(root); err != nil || len(entries) != 0 {
		t.Fatal(entries, err)
	}
	for _, data := range []string{"invalid\n", "{\"job_id\":\"a\",\"run_id\":\"b\"}\n{\"job_id\":\"a\",\"run_id\":\"b\"}\n"} {
		if err := os.WriteFile(filepath.Join(root, "usage.jsonl"), []byte(data), 0600); err != nil {
			t.Fatal(err)
		}
		if _, err := usageRecords(root); err == nil {
			t.Fatal("invalid journal accepted")
		}
	}
}

func TestUsageOracleRequiresActualJobProviderCounters(t *testing.T) {
	for _, unknown := range []bool{false, true} {
		t.Run(fmt.Sprint(unknown), func(t *testing.T) {
			root := t.TempDir()
			for _, dir := range []string{"mappings", "mock-sent"} {
				if err := os.MkdirAll(filepath.Join(root, dir), 0700); err != nil {
					t.Fatal(err)
				}
			}
			cfg := communication.Config{SpoolDir: root, ControlAuth: "synthetic"}
			client := &http.Client{Transport: transportFunc(func(r *http.Request) (*http.Response, error) {
				var input struct {
					Text   string
					ChatID int64 `json:"chat_id"`
				}
				if err := json.NewDecoder(r.Body).Decode(&input); err != nil {
					t.Fatal(err)
				}
				id := "mock-" + strings.Repeat("a", 64)
				marker := strings.Split(input.Text, ": ")[1]
				m := communication.JobMapping{JobID: id, PrincipalID: "alice", TaskID: "task", SessionID: "session", RunID: "run", Status: "completed"}
				d := communication.Delivery{JobID: id, TaskID: "task", ChatID: input.ChatID, Channel: "mock", Text: marker}
				for name, value := range map[string]any{"mappings/" + id + ".json": m, "mock-sent/job-" + id + "-response.json": map[string]any{"principal": "alice", "delivery": d}} {
					data, _ := json.Marshal(value)
					if err := os.WriteFile(filepath.Join(root, name), data, 0600); err != nil {
						t.Fatal(err)
					}
				}
				meter := map[string]any{"job_id": id, "run_id": "run", "session_id": "session", "input_tokens": 10, "output_tokens": 2, "input_counter": 10, "output_counter": 2}
				if unknown {
					meter["input_tokens"] = nil
				}
				data, _ := json.Marshal(meter)
				if err := os.WriteFile(filepath.Join(root, "usage.jsonl"), append(data, '\n'), 0600); err != nil {
					t.Fatal(err)
				}
				return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader(fmt.Sprintf(`{"job_id":%q,"task_id":"task"}`, id)))}, nil
			})}
			status, reason := usageAgentCase(context.Background(), client, cfg, communication.User{ID: "alice"}, "mock-server")
			want := "PASS"
			if unknown {
				want = "FAIL"
			}
			if status != want {
				t.Fatal(status, reason)
			}
		})
	}
}
