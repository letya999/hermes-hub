package routines

import (
	"encoding/json"
	"os"
	"path/filepath"
	"slices"

	"github.com/letya999/hermes-hub/internal/commodel"
	"github.com/letya999/hermes-hub/internal/identity"
)

// OwnsHistoryEnvelope reports whether an envelope belongs to the schedule's
// owner context — current identity or one of the durable prior envelopes.
func OwnsHistoryEnvelope(item commodel.Schedule, envelope identity.Envelope) bool {
	return envelope.PrincipalID == item.Envelope.PrincipalID && envelope.ContextID == item.Envelope.ContextID && (envelope == item.Envelope || slices.Contains(item.PriorEnvelopes, envelope))
}

// ReminderStateLocked derives a reminder occurrence's outcome on owner reads;
// transport failures must never look like successful sends. The engine lock
// must be held.
func (e *Engine) ReminderStateLocked(item commodel.Schedule) (string, string) {
	id := item.LastOccurrenceID
	if !commodel.IDPattern.MatchString(id) || len(id) > 160 {
		return "unavailable", ""
	}
	state := "unavailable"
	for _, dir := range []string{"", "archive"} {
		body, err := os.ReadFile(filepath.Join(e.root, "occurrences", dir, id+".json"))
		if os.IsNotExist(err) {
			continue
		}
		var o commodel.RoutineOccurrence
		if err != nil || len(body) > 128*1024 || json.Unmarshal(body, &o) != nil || o.Job.ID != id || o.ScheduleID != item.ScheduleID || !OwnsHistoryEnvelope(item, o.Job.Envelope) {
			return "unavailable", ""
		}
		state = o.State
		break
	}
	if state == "unavailable" {
		return state, ""
	}
	for _, delivery := range []struct{ dir, state string }{{"failed", "uncertain"}, {"done", "delivered"}} {
		_, err := os.Stat(filepath.Join(e.root, "outbox", delivery.dir, "reminder-"+id+".json"))
		if err == nil {
			return state, delivery.state
		}
		if !os.IsNotExist(err) {
			return state, "unavailable"
		}
	}
	return state, ""
}
