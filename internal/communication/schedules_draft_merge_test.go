package communication

import (
	"testing"

	"github.com/letya999/hermes-hub/internal/identity"
)

// validateScheduleDraft rejects malformed drafts for every job kind while a
// well-formed watcher draft passes the full field matrix.
func TestValidateScheduleDraftBranches(t *testing.T) {
	env := identity.TelegramEnvelope("alice", 11, "alice", "policy-1")
	valid := Schedule{
		ScheduleID: "sched-1", Envelope: env, UserID: "alice", Channel: "telegram_bot",
		JobKind: "watcher", Input: "watch the page", Timezone: "UTC",
		Probe: &WatchProbe{URL: "https://example.com/feed", Regex: "v(\\d+)"},
	}
	if err := validateScheduleDraft(valid, env); err != nil {
		t.Fatalf("valid draft: %v", err)
	}
	cases := []struct {
		name  string
		patch func(*Schedule)
	}{
		{"bad id", func(s *Schedule) { s.ScheduleID = "bad id!" }},
		{"bad kind", func(s *Schedule) { s.JobKind = "bogus" }},
		{"check no probe", func(s *Schedule) { s.JobKind = "check"; s.Probe = nil }},
		{"check bad expect", func(s *Schedule) { s.JobKind = "check"; s.Expect = "weird" }},
		{"check equals", func(s *Schedule) { s.JobKind = "check"; s.Expect = "equals:" }},
		{"brief probe", func(s *Schedule) { s.JobKind = "brief" }},
		{"retries non-check", func(s *Schedule) { s.MaxRetries = 3 }},
		{"retries bound", func(s *Schedule) { s.JobKind = "check"; s.Expect = "changed"; s.MaxRetries = 9 }},
		{"http probe in check", func(s *Schedule) { s.JobKind = "check"; s.Expect = "changed"; s.Probe = &WatchProbe{URL: "ftp://x"} }},
		{"probe userinfo", func(s *Schedule) { s.Probe.URL = "https://u@h/x" }},
		{"probe bad regex", func(s *Schedule) { s.Probe.Regex = "(" }},
		{"regex no group", func(s *Schedule) { s.Probe.Regex = "abc" }},
		{"source ids non-digest", func(s *Schedule) { s.SourceIDs = []string{"a"} }},
		{"source dup", func(s *Schedule) { s.JobKind = "digest"; s.Probe = nil; s.SourceIDs = []string{"a", "a"} }},
		{"notify bad", func(s *Schedule) { s.NotifyMode = "weird" }},
		{"notify agent on digest", func(s *Schedule) { s.JobKind = "digest"; s.Probe = nil; s.NotifyMode = "agent" }},
		{"quiet one bound", func(s *Schedule) { s.QuietFrom = "22:00" }},
		{"quiet equal", func(s *Schedule) { s.QuietFrom = "22:00"; s.QuietTo = "22:00" }},
		{"daily limit", func(s *Schedule) { s.DailyLimit = 33 }},
		{"sink non-event", func(s *Schedule) { s.Sink = "notify" }},
		{"delivery bad", func(s *Schedule) { s.JobKind = "event"; s.Sink = "notify"; s.Delivery = "weird" }},
		{"stream non-mcp", func(s *Schedule) { s.JobKind = "event"; s.Delivery = "stream" }},
		{"probe url long", func(s *Schedule) { s.Probe.URL = "https://x/" + string(make([]byte, 2100)) }},
		{"probe kind bad", func(s *Schedule) { s.Probe.Kind = "carrier-pigeon" }},
		{"github probe bad", func(s *Schedule) { s.Probe = &WatchProbe{Kind: "github_issue", URL: "https://not-github/x"} }},
		{"empty input", func(s *Schedule) { s.Input = "  " }},
		{"bad channel", func(s *Schedule) { s.Channel = "sms" }},
		{"no timezone", func(s *Schedule) { s.Timezone = "" }},
		{"control-char url", func(s *Schedule) { s.Probe.URL = "https://x/\x00a" }},
	}
	for _, tc := range cases {
		d := valid
		probe := *d.Probe
		d.Probe = &probe
		tc.patch(&d)
		if err := validateScheduleDraft(d, env); err == nil {
			t.Fatalf("%s accepted", tc.name)
		}
	}
	// Ownership: caller must match the envelope.
	if err := validateScheduleDraft(valid, identity.TelegramEnvelope("mallory", 99, "mallory", "policy-1")); err == nil {
		t.Fatal("foreign caller accepted")
	}
	// Valid variants: check+expect, event+sink, github issue probe, mcp_tool.
	for name, patch := range map[string]func(*Schedule){
		"check equals": func(s *Schedule) { s.JobKind = "check"; s.Expect = "equals:green" },
		"event sink":   func(s *Schedule) { s.JobKind = "event"; s.Sink = "notify" },
		"quiet window": func(s *Schedule) { s.QuietFrom = "22:00"; s.QuietTo = "07:00" },
		"github probe": func(s *Schedule) {
			s.Probe = &WatchProbe{Kind: "github_issue", URL: "https://api.github.com/repos/a/b/issues/3"}
		},
		"task probe": func(s *Schedule) {
			s.JobKind = "check"
			s.Expect = "changed"
			s.Probe = &WatchProbe{URL: "task:t-12345"}
		},
	} {
		d := valid
		patch(&d)
		if err := validateScheduleDraft(d, env); err != nil {
			t.Fatalf("%s rejected: %v", name, err)
		}
	}
}
