package main

import (
	"encoding/json"
	"io"
	"time"

	"github.com/letya999/hermes-hub/internal/communication"
)

// runUsage renders the metering report for `hubctl usage`: the append-only
// journal read directly, aggregated per scope and per actor over the window.
func runUsage(out io.Writer, spoolDir string, window time.Duration, actor, model string) error {
	records, err := communication.ReadUsageJournal(spoolDir)
	if err != nil {
		return err
	}
	since := time.Time{}
	if window > 0 {
		since = time.Now().UTC().Add(-window)
	}
	return json.NewEncoder(out).Encode(communication.AggregateUsage(records, since, actor, model))
}
