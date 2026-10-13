package e2e

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"reflect"
	"time"

	"github.com/letya999/hermes-hub/internal/communication"
)

type meteringRecord struct {
	JobID, RunID, SessionID                                string
	InputTokens, OutputTokens, InputCounter, OutputCounter *int64
}

func usageRecords(root string) (map[string]meteringRecord, error) {
	f, err := os.Open(filepath.Join(root, "usage.jsonl"))
	if os.IsNotExist(err) {
		return map[string]meteringRecord{}, nil
	}
	if err != nil {
		return nil, err
	}
	defer f.Close()
	records := map[string]meteringRecord{}
	s := bufio.NewScanner(f)
	for s.Scan() {
		var entry struct {
			JobID         string `json:"job_id"`
			RunID         string `json:"run_id"`
			SessionID     string `json:"session_id"`
			InputTokens   *int64 `json:"input_tokens"`
			OutputTokens  *int64 `json:"output_tokens"`
			InputCounter  *int64 `json:"input_counter"`
			OutputCounter *int64 `json:"output_counter"`
		}
		if json.Unmarshal(s.Bytes(), &entry) != nil {
			return nil, fmt.Errorf("invalid metering record")
		}
		key := entry.JobID + "/" + entry.RunID
		if _, exists := records[key]; exists {
			return nil, fmt.Errorf("duplicate metering record")
		}
		records[key] = meteringRecord{entry.JobID, entry.RunID, entry.SessionID, entry.InputTokens, entry.OutputTokens, entry.InputCounter, entry.OutputCounter}
	}
	return records, s.Err()
}

// Read the active spool without reopening its writer or taking its process lock.
func usageAgentCase(ctx context.Context, client *http.Client, config communication.Config, user communication.User, mode string) (string, string) {
	before, err := usageRecords(config.SpoolDir)
	if err != nil {
		return "FAIL", "metering baseline unreadable"
	}
	run := mockPromptJobCase
	if mode == "telegram-server" {
		run = serverPromptJobCase
	}
	jobID := ""
	if status, reason := run(ctx, client, config, user, "text", "", "", &jobID); status != "PASS" {
		return status, reason
	}
	deadline, cancel := context.WithTimeout(ctx, 45*time.Second)
	defer cancel()
	for {
		after, err := usageRecords(config.SpoolDir)
		if err != nil {
			return "FAIL", "metering journal unreadable or duplicate job/run"
		}
		for key, entry := range after {
			if entry.JobID != jobID {
				continue
			}
			if _, exists := before[key]; exists {
				continue
			}
			data, err := os.ReadFile(filepath.Join(config.SpoolDir, "mappings", entry.JobID+".json"))
			var mapping communication.JobMapping
			if err != nil || json.Unmarshal(data, &mapping) != nil || mapping.PrincipalID != user.ID || mapping.Status != "completed" || mapping.RunID != entry.RunID || mapping.SessionID != entry.SessionID {
				continue
			}
			if entry.InputTokens == nil || entry.OutputTokens == nil || entry.InputCounter == nil || entry.OutputCounter == nil || *entry.InputTokens <= 0 || *entry.OutputTokens <= 0 || *entry.InputTokens > *entry.InputCounter || *entry.OutputTokens > *entry.OutputCounter {
				return "FAIL", "completed real run has unknown or inconsistent provider token accounting"
			}
			repeated, err := usageRecords(config.SpoolDir)
			if err != nil || !reflect.DeepEqual(repeated[key], entry) {
				return "FAIL", "metering reread changed the job/run record"
			}
			return "PASS", "real completed owner run has positive provider counters and one stable journal entry; Telegram /usage rendering not exercised"
		}
		select {
		case <-deadline.Done():
			return "FAIL", "completed run was not metered within deadline"
		case <-time.After(time.Second):
		}
	}
}
