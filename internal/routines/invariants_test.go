package routines

import (
	"regexp"
	"testing"
	"time"

	"github.com/letya999/hermes-hub/internal/commodel"
)

// Durable id formats are a replay contract: a spool written by one release
// must resolve identically after upgrade. This golden freezes the exact
// hash inputs and prefix — changing them is a spool migration, never a
// refactor.
func TestOccurrenceIDFormatGolden(t *testing.T) {
	due := time.Date(2030, 1, 2, 3, 4, 5, 0, time.UTC)
	o := commodel.RoutineOccurrence{ScheduleID: "r-1", Revision: 7, DueAt: due}
	id := OccurrenceID(o)
	if !regexp.MustCompile(`^routine-[0-9a-f]{64}$`).MatchString(id) {
		t.Fatalf("id %q broke the routine-<sha256> contract", id)
	}
	if id != OccurrenceID(o) {
		t.Fatal("id not deterministic")
	}
	other := o
	other.DueAt = due.Add(time.Second)
	if id == OccurrenceID(other) {
		t.Fatal("due time does not feed the id")
	}
}
