package communication

import (
	"time"

	"github.com/letya999/hermes-hub/internal/commodel"
)

// Domain-model records live in internal/commodel so channel adapters and the
// routines engine can share them without importing the gateway. The aliases
// keep this package's existing call sites and its exported contract stable.
type (
	Job               = commodel.Job
	Delivery          = commodel.Delivery
	DeliveryButton    = commodel.DeliveryButton
	Task              = commodel.Task
	User              = commodel.User
	SlackLink         = commodel.SlackLink
	RoutineOccurrence = commodel.RoutineOccurrence
	Schedule          = commodel.Schedule
	ScheduleCreation  = commodel.ScheduleCreation
	RoutineTiming     = commodel.RoutineTiming
	RoutineWindow     = commodel.RoutineWindow
	WatchProbe        = commodel.WatchProbe
	WatchProbeSource  = commodel.WatchProbeSource
	ProbeNeed         = commodel.ProbeNeed
	RenderStep        = commodel.RenderStep
	ExtractStep       = commodel.ExtractStep
	RoutineDraft      = commodel.RoutineDraft
	JobMapping        = commodel.JobMapping
	RunOutcome        = commodel.RunOutcome
	PendingControl    = commodel.PendingControl
)

func jobFingerprint(job Job) string { return commodel.JobFingerprint(job) }

func mappingFromJob(job Job, now time.Time) JobMapping {
	return commodel.MappingFromJob(job, now)
}
