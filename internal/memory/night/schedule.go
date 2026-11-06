package night

// Schedule is the published, per-user system clock projection.
// Ordinary routine CRUD has no path to its protected owner record.
type Schedule struct {
	Enabled       bool
	LocalTime     string
	Timezone      string
	Revision      string
	RetentionDays int
	FoldBytes     int
}
