package communication

import (
	"encoding/json"
	"os"
	"path/filepath"
)

// Derived on owner reads; transport failures must never look like successful sends.
func (s *Spool) reminderStateLocked(item Schedule) (string, string) {
	id := item.LastOccurrenceID
	if !spoolIDPattern.MatchString(id) || len(id) > 160 {
		return "unavailable", ""
	}
	state := "unavailable"
	for _, dir := range []string{"", "archive"} {
		body, err := os.ReadFile(filepath.Join(s.root, "occurrences", dir, id+".json"))
		if os.IsNotExist(err) {
			continue
		}
		var o RoutineOccurrence
		if err != nil || len(body) > 128*1024 || json.Unmarshal(body, &o) != nil || o.Job.ID != id || o.ScheduleID != item.ScheduleID || !routineOwnsHistoryEnvelope(item, o.Job.Envelope) {
			return "unavailable", ""
		}
		state = o.State
		break
	}
	if state == "unavailable" {
		return state, ""
	}
	for _, delivery := range []struct{ dir, state string }{{"failed", "uncertain"}, {"done", "delivered"}} {
		_, err := os.Stat(filepath.Join(s.root, "outbox", delivery.dir, "reminder-"+id+".json"))
		if err == nil {
			return state, delivery.state
		}
		if !os.IsNotExist(err) {
			return state, "unavailable"
		}
	}
	return state, ""
}
