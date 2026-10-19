package routines

import (
	"crypto/sha256"
	"encoding/hex"
	"strconv"
	"strings"
	"time"

	"github.com/letya999/hermes-hub/internal/commodel"
)

// DefaultCatchUp bounds how far a missed schedule may backfill when the
// gateway resumes after downtime.
const DefaultCatchUp = time.Hour

func OccurrenceID(o commodel.RoutineOccurrence) string { return occurrenceID(o) }

func occurrenceID(o commodel.RoutineOccurrence) string {
	sum := sha256.Sum256([]byte(o.ScheduleID + "\x00" + strconv.FormatUint(o.Revision, 10) + "\x00" + o.DueAt.UTC().Format(time.RFC3339Nano)))
	return "routine-" + hex.EncodeToString(sum[:])
}

// RoutineConversation isolates routine sessions: every occurrence of one
// schedule shares a stable "routine-<id>" conversation instead of the owner's
// interactive task conversation. TaskID/TopicID remain on the job for
// delivery routing only.
func RoutineConversation(scheduleID string) string {
	return "routine-" + scheduleID
}

// RoutineFailureDigest normalizes an owner-facing error text so repeated
// occurrences of the same failure hash identically — the per-occurrence job
// ID is stripped before hashing.
func RoutineFailureDigest(jobID, message string) string {
	sum := sha256.Sum256([]byte(strings.ReplaceAll(message, jobID, "")))
	return hex.EncodeToString(sum[:])
}
