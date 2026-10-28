package skillstore

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"time"
)

// Bounds for the usage ledger: events are forensic breadcrumbs, not a log
// store — keep them bounded so a runaway agent cannot grow them (R12.5).
const (
	usageEventsMax = 256
	usageDetailMax = 500
	usageFileMax   = 256 * 1024
)

// UsageEvent is one recorded skill interaction (R12.5). Kind is
// use|view|helped|failed|missed|corrected; Detail is a bounded note.
type UsageEvent struct {
	At     string `json:"at"`
	Kind   string `json:"kind"`
	Detail string `json:"detail,omitempty"`
}

// UsageStats are the per-skill counters surfaced in skills_list and
// `hubctl skill stats` — shape only, never content (R12.5).
type UsageStats struct {
	Uses        int    `json:"uses"`
	Helped      int    `json:"helped"`
	Failed      int    `json:"failed"`
	TriggerMiss int    `json:"trigger_misses"`
	LastUsed    string `json:"last_used,omitempty"`
}

// UsageRecord is the persisted per-skill file under .state/usage/.
type UsageRecord struct {
	Stats  UsageStats   `json:"stats"`
	Events []UsageEvent `json:"events"`
}

func usageFile(storeDir, skill string) (string, error) {
	if !ValidName(skill) {
		return "", fmt.Errorf("%w: skill name %q", ErrInvalid, skill)
	}
	return filepath.Join(storeDir, ".state", "usage", skill+".json"), nil
}

// Usage loads a skill's recorded usage; absent files read as zero and a
// corrupt file fails closed rather than inventing counters.
func Usage(storeDir, skill string) (UsageRecord, error) {
	path, err := usageFile(storeDir, skill)
	if err != nil {
		return UsageRecord{}, err
	}
	body, err := os.ReadFile(path) // #nosec G304 -- ValidName-checked path inside the owned store
	if errors.Is(err, os.ErrNotExist) {
		return UsageRecord{}, nil
	}
	if err != nil {
		return UsageRecord{}, err
	}
	if len(body) > usageFileMax {
		return UsageRecord{}, fmt.Errorf("%w: usage file too large", ErrInvalid)
	}
	var rec UsageRecord
	if err := json.Unmarshal(body, &rec); err != nil {
		return UsageRecord{}, fmt.Errorf("%w: usage file corrupt", ErrInvalid)
	}
	return rec, nil
}

var usageKinds = map[string]bool{
	"use": true, "view": true, "helped": true,
	"failed": true, "missed": true, "corrected": true,
}

// RecordUsage appends one event and updates the counters. The store dir is
// hub-owned — callers are the hub itself (toolhub handlers), never the
// agent directly.
func RecordUsage(storeDir, skill, kind, detail string) error {
	if !usageKinds[kind] {
		return fmt.Errorf("%w: usage kind %q", ErrInvalid, kind)
	}
	if len(detail) > usageDetailMax {
		detail = detail[:usageDetailMax]
	}
	rec, err := Usage(storeDir, skill)
	if err != nil {
		return err
	}
	at := time.Now().UTC().Format(time.RFC3339)
	rec.Events = append(rec.Events, UsageEvent{At: at, Kind: kind, Detail: detail})
	if len(rec.Events) > usageEventsMax {
		rec.Events = rec.Events[len(rec.Events)-usageEventsMax:]
	}
	switch kind {
	case "use":
		rec.Stats.Uses++
		rec.Stats.LastUsed = at
	case "helped":
		rec.Stats.Helped++
		rec.Stats.LastUsed = at
	case "failed":
		rec.Stats.Failed++
		rec.Stats.LastUsed = at
	case "missed":
		rec.Stats.TriggerMiss++
	}
	path, err := usageFile(storeDir, skill)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		return err
	}
	body, err := json.Marshal(rec)
	if err != nil {
		return err
	}
	return writePrivateFile(path, body)
}
