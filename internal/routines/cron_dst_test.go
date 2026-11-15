package routines

import (
	"testing"
	"time"
)

func TestNextCronSpringGapAndRepeatedHour(t *testing.T) {
	loc, err := time.LoadLocation("America/New_York")
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct{ expr, from, want string }{
		{"30 2 * * *", "2026-03-08T06:59:00Z", "2026-03-08T07:00:00Z"},
		{"30 1 * * *", "2026-11-01T05:29:00Z", "2026-11-01T05:30:00Z"},
		{"30 1 * * *", "2026-11-01T05:30:00Z", "2026-11-01T06:30:00Z"},
	} {
		from, _ := time.Parse(time.RFC3339, tc.from)
		due, err := NextCron(tc.expr, loc, from)
		if err != nil || due.Format(time.RFC3339) != tc.want {
			t.Fatalf("%s from %s: %s, %v; want %s", tc.expr, tc.from, due, err, tc.want)
		}
	}
}
