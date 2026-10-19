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

// Byte-exact golden: the occurrence id is sha256 over
// scheduleID "\x00" revision "\x00" dueAt(RFC3339Nano UTC). Any change to
// field order or separator rewrites every existing spool id — a migration.
func TestOccurrenceIDExactGolden(t *testing.T) {
	due := time.Date(2030, 1, 2, 3, 4, 5, 0, time.UTC)
	o := commodel.RoutineOccurrence{ScheduleID: "r-1", Revision: 7, DueAt: due}
	const want = "routine-3ac85bafc318aff65f8c219ef8bafed9244edf58e777500cb1163acffb0c09ab"
	if got := OccurrenceID(o); got != want {
		t.Fatalf("occurrence id drifted: got %q want %q", got, want)
	}
}

// RoutineConversation pins every occurrence of one schedule to a single
// conversation distinct from any interactive task conversation.
func TestRoutineConversationStable(t *testing.T) {
	if got := RoutineConversation("r-1"); got != "routine-r-1" {
		t.Fatalf("conversation id drifted: %q", got)
	}
	if !commodel.IDPattern.MatchString(RoutineConversation("r-1")) {
		t.Fatal("routine conversation id left the filename-safe charset")
	}
}

// RoutineFailureDigest strips the per-occurrence job id before hashing, so
// the same failure in two occurrences digests identically.
func TestRoutineFailureDigestIgnoresJobID(t *testing.T) {
	a := RoutineFailureDigest("job-42", "run job-42 failed at step 3 for job-42")
	b := RoutineFailureDigest("job-99", "run job-99 failed at step 3 for job-99")
	if a != b {
		t.Fatal("digest is occurrence-specific; dedup broken")
	}
	const want = "5c695ddc030de7605d250e88adb29b4b66e0644b5f29c579f1496d431d5b8c82"
	if a != want {
		t.Fatalf("digest drifted: got %q want %q", a, want)
	}
}
