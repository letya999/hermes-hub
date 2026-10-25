package compaction

// ScanResult is the metadata-only carrier scan a sidecar reports: rotation
// lineage plus the count of in-place summary rows. Nothing in it carries
// message content.
type ScanResult struct {
	Lineage       []string
	Tip           string
	Rotations     int
	InPlace       int
	LastSummaryAt *float64
}

// Observe journals the compactions a scan proves happened but no writer
// recorded — upstream's own in-loop compression leaves summary rows with no
// hub-side event. The delta against journaled rows is the only source for
// observed events, so re-observing the same transcript appends nothing.
func (j *Journal) Observe(sessionID string, scan ScanResult) (int, error) {
	events, err := j.Events(sessionID)
	if err != nil {
		return 0, err
	}
	committed := 0
	for _, e := range events {
		if e.FailureClass == "" {
			committed++
		}
	}
	delta := scan.InPlace - committed
	if delta <= 0 {
		return 0, nil
	}
	for i := 0; i < delta; i++ {
		if err := j.Append(Event{
			SessionID: sessionID,
			Trigger:   TriggerAutoInLoop, Engine: EngineNative, Method: MethodInLoop,
		}); err != nil {
			return i, err
		}
	}
	return delta, nil
}
