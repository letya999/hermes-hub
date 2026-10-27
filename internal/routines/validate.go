package routines

import (
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"path/filepath"
	"regexp"
	"strings"
	"time"
	"unicode"

	"github.com/letya999/hermes-hub/internal/commodel"
	"github.com/letya999/hermes-hub/internal/envstore"
	"github.com/letya999/hermes-hub/internal/identity"
)

// DraftHooks declares the owner-supplied checks a draft validation needs:
// the provider channel's own field rules (the gateway resolves its registry)
// and the script compiler. Both are required — a nil hook panics loudly.
type DraftHooks struct {
	Channel func(in commodel.Schedule) error
	Script  func(src string) error
}

// ValidateSchedule checks a complete draft without mutating the spool.
func ValidateSchedule(in commodel.Schedule, caller identity.Envelope, now time.Time, hooks DraftHooks) error {
	in = NormalizeScheduleDraft(in)
	var err error
	in, err = ResolveRelativeReminder(in, now)
	if err != nil {
		return err
	}
	if err := ValidateScheduleDraft(in, caller, hooks); err != nil {
		return err
	}
	loc, err := time.LoadLocation(in.Timezone)
	if err != nil {
		return errors.New("invalid timezone")
	}
	_, err = NextSchedule(in, loc, now)
	return err
}

func ScheduleIntent(in commodel.Schedule) string {
	in.PriorEnvelopes = nil
	in.OriginJobID, in.DestinationTaskID = "", ""
	in.Creation = nil
	in.LastOccurrenceState, in.LastDeliveryState = "", ""
	in.Revision, in.Enabled, in.Paused = 0, false, false
	in.NextDue, in.CreatedAt, in.UpdatedAt, in.SnoozedUntil = time.Time{}, time.Time{}, time.Time{}, time.Time{}
	in.InitialProbeValue, in.NativeCronRef, in.LastFailureSHA256, in.LastOccurrenceID = nil, "", "", ""
	if in.JobKind == "" {
		in.JobKind = "agent"
	}
	if in.CatchUpWindow <= 0 {
		in.CatchUpWindow = DefaultCatchUp
	}
	b, _ := json.Marshal(in)
	return fmt.Sprintf("%x", sha256.Sum256(b))
}

func ResolveRelativeReminder(in commodel.Schedule, now time.Time) (commodel.Schedule, error) {
	if !strings.HasPrefix(in.Expression, "after:") {
		return in, nil
	}
	delay, err := time.ParseDuration(strings.TrimPrefix(in.Expression, "after:"))
	if in.JobKind != "reminder" || err != nil || delay < time.Second || delay > 8760*time.Hour || delay%time.Second != 0 {
		return commodel.Schedule{}, errors.New("after: requires a reminder and an integral delay from 1s to 8760h")
	}
	in.Expression = "once:" + now.Add(delay).UTC().Format(time.RFC3339Nano)
	return in, nil
}

var probeToolPattern = regexp.MustCompile(`^[a-zA-Z0-9][a-zA-Z0-9._-]{0,127}$`)

// validMCPProbe bounds a tool-call probe (SPEC-0054 R3.2): a named projected
// tool and no URL — credentials stay inside the ToolHub workload so the
// probe never carries a source address.
func validMCPProbe(probe *commodel.WatchProbe) error {
	if probe == nil || !probeToolPattern.MatchString(probe.Tool) {
		return errors.New("mcp_tool probe needs a projected tool name")
	}
	if probe.URL != "" {
		return errors.New("mcp_tool probe takes no url")
	}
	if probe.RequeueTool != "" && !probeToolPattern.MatchString(probe.RequeueTool) {
		return errors.New("invalid requeue tool name")
	}
	if probe.Args != nil {
		b, err := json.Marshal(probe.Args)
		if err != nil || len(b) > 8*1024 {
			return errors.New("probe args exceed bounds")
		}
	}
	return nil
}

// validateProbeNeeds bounds hub-prefetched tool calls for script/command
// probes (SPEC-0059 R5.2): at most four projected read tools, each with a
// bounded args object. Other kinds cannot spend tool budget they never run.
func validateProbeNeeds(probe *commodel.WatchProbe) error {
	if len(probe.Needs) == 0 {
		return nil
	}
	if probe.Kind != "script" && probe.Kind != "command" {
		return errors.New("needs require a script or command probe kind")
	}
	if len(probe.Needs) > 4 {
		return errors.New("probe needs exceed bounds")
	}
	for _, need := range probe.Needs {
		if !probeToolPattern.MatchString(need.Tool) {
			return errors.New("invalid need tool name")
		}
		if need.Args != nil {
			b, err := json.Marshal(need.Args)
			if err != nil || len(b) > 4*1024 {
				return errors.New("need args exceed bounds")
			}
		}
	}
	return nil
}

// validTaskProbe reports whether url is a hub-internal task predicate
// (task:<id>) — a verified source the probe resolves without any fetch
// (SPEC-0054 R1.3).
func validTaskProbe(url string) bool {
	id := strings.TrimPrefix(url, "task:")
	return id != url && id != "" && len(id) <= 80 && commodel.IDPattern.MatchString(id)
}

func NormalizeScheduleDraft(in commodel.Schedule) commodel.Schedule {
	in.PriorEnvelopes = nil
	in.LastOccurrenceID, in.LastOccurrenceState, in.LastDeliveryState = "", "", ""
	if in.Probe != nil {
		probe := *in.Probe
		probe.URL = strings.TrimSpace(probe.URL)
		if probe.Kind == "tool" {
			// "tool" is the readable alias for the same ToolHub path: any
			// projected read tool, not only connector MCPs (SPEC-0059 R5.1).
			probe.Kind = "mcp_tool"
		}
		in.Probe = &probe
	}
	return in
}

func ValidateScheduleDraft(in commodel.Schedule, caller identity.Envelope, hooks DraftHooks) error {
	if in.Probe != nil && strings.ContainsFunc(in.Probe.URL, func(r rune) bool { return unicode.IsControl(r) || unicode.Is(unicode.Cf, r) }) {
		return errors.New("probe URL contains invisible or control characters")
	}
	if len(in.ScheduleID) > 80 || !commodel.IDPattern.MatchString(in.ScheduleID) || !caller.Equal(in.Envelope) || caller.Validate(in.UserID, in.Envelope.ContextID, in.Envelope.RuntimeID, in.Envelope.PolicyVersion) != nil {
		return errors.New("invalid schedule ownership")
	}
	if in.JobKind != "" && in.JobKind != "agent" && in.JobKind != "script" && in.JobKind != "reminder" && in.JobKind != "watcher" && in.JobKind != "check" && in.JobKind != "digest" && in.JobKind != "brief" && in.JobKind != "event" {
		return errors.New("invalid job kind")
	}
	if in.JobKind == "check" && in.Expect != "changed" && in.Expect != "unchanged" && (!strings.HasPrefix(in.Expect, "equals:") || len(in.Expect) <= len("equals:") || len(in.Expect) > 1024) {
		return errors.New("invalid check expect")
	}
	if in.JobKind == "brief" && in.Probe != nil {
		return errors.New("a brief aggregates sources and carries no probe")
	}
	if in.Probe != nil && in.JobKind != "watcher" && in.JobKind != "check" && in.JobKind != "event" {
		return errors.New("a probe requires a watcher, check or event schedule; for source event subscriptions use watch_subscribe")
	}
	if in.MaxRetries != 0 && (in.JobKind != "check" || in.MaxRetries < 0 || in.MaxRetries > 8) {
		return errors.New("max_retries is a check bound between 0 and 8")
	}
	if in.JobKind == "check" {
		if in.Probe == nil {
			return errors.New("check needs a probe")
		}
		if in.Probe.Kind == "mcp_tool" {
			if err := validMCPProbe(in.Probe); err != nil {
				return err
			}
		} else if strings.HasPrefix(in.Probe.URL, "task:") {
			if !validTaskProbe(in.Probe.URL) {
				return errors.New("invalid task probe")
			}
		} else if in.Probe.Kind != "service" {
			u, err := url.Parse(in.Probe.URL)
			if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" || u.User != nil {
				return errors.New("check probe must be http(s)")
			}
			if in.Probe.Regex != "" {
				if _, err := regexp.Compile(in.Probe.Regex); err != nil {
					return errors.New("check probe regex does not compile")
				}
			}
		}
	}
	if in.JobKind == "watcher" || in.JobKind == "event" {
		if in.Probe == nil {
			return errors.New("watcher needs a probe")
		}
		if in.Probe.Kind == "mcp_tool" {
			if err := validMCPProbe(in.Probe); err != nil {
				return err
			}
		} else if in.Probe.Kind == "hook" {
			if u, err := url.Parse(in.Probe.URL); err != nil || u.Scheme != "hook" {
				return errors.New("hook probe needs a hook:<label> url")
			}
		} else if in.Probe.Kind != "command" && in.Probe.Kind != "service" {
			u, err := url.Parse(in.Probe.URL)
			if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" || u.User != nil {
				return errors.New("watch probe must be a plain http(s) url")
			}
			if in.Probe.Regex != "" {
				if _, err := regexp.Compile(in.Probe.Regex); err != nil {
					return fmt.Errorf("watch probe regex does not compile: %w", err)
				}
			}
		}
	}
	if len(in.SourceIDs) > 128 || (len(in.SourceIDs) > 0 && in.JobKind != "digest" && in.JobKind != "brief") {
		return errors.New("source_ids require a digest or brief and at most 128 sources")
	}
	seenSources := map[string]bool{}
	for _, id := range in.SourceIDs {
		if !commodel.IDPattern.MatchString(id) || len(id) > 80 || seenSources[id] || id == in.ScheduleID {
			return errors.New("invalid digest source_ids")
		}
		seenSources[id] = true
	}
	if in.NotifyMode != "" && in.NotifyMode != "immediate" && in.NotifyMode != "digest" && in.NotifyMode != "agent" {
		return errors.New("invalid notify mode")
	}
	if in.NotifyMode == "agent" && in.JobKind != "watcher" && in.JobKind != "check" {
		return errors.New("agent escalation requires a watcher or check")
	}
	if (in.QuietFrom == "") != (in.QuietTo == "") {
		return errors.New("quiet window needs both bounds")
	}
	if in.QuietFrom != "" {
		from, err1 := time.Parse("15:04", in.QuietFrom)
		to, err2 := time.Parse("15:04", in.QuietTo)
		if err1 != nil || err2 != nil || from.Equal(to) {
			return errors.New("invalid quiet window")
		}
	}
	if in.DailyLimit < 0 || in.DailyLimit > 32 {
		return errors.New("daily_limit must be 0..32")
	}
	if in.Sink != "" && in.Sink != "notify" && in.Sink != "agent" {
		return errors.New("invalid event sink")
	}
	if in.Sink != "" && in.JobKind != "event" {
		return errors.New("sink requires an event schedule")
	}
	if in.DeliverMode != "" && in.DeliverMode != "notify" && in.DeliverMode != "content" && in.DeliverMode != "artifact" {
		return errors.New("invalid deliver mode")
	}
	if in.DeliverMode != "" && in.DeliverMode != "notify" && in.JobKind != "watcher" {
		return errors.New("deliver mode requires a watcher")
	}
	if in.ArtifactName != "" && filepath.Base(in.ArtifactName) != in.ArtifactName {
		return errors.New("invalid artifact name")
	}
	if in.Delivery != "" && in.Delivery != "poll" && in.Delivery != "stream" {
		return errors.New("invalid event delivery")
	}
	if in.Delivery == "stream" && (in.JobKind != "event" || in.Probe == nil || in.Probe.Kind != "mcp_tool") {
		return errors.New("stream delivery needs an event schedule with an mcp_tool probe")
	}
	if in.Probe != nil {
		if err := ExpandRecipe(in.Probe); err != nil {
			return err
		}
		if len(in.Probe.URL) > 2048 {
			return errors.New("probe URL exceeds limit")
		}
		if in.Probe.Kind != "" && in.Probe.Kind != "http" && in.Probe.Kind != "github_issue" && in.Probe.Kind != "mcp_tool" && in.Probe.Kind != "script" && in.Probe.Kind != "command" && in.Probe.Kind != "render" && in.Probe.Kind != "service" && in.Probe.Kind != "hook" {
			return errors.New("invalid probe kind")
		}
		if in.Probe.Kind == "render" || in.Probe.Kind == "service" {
			if !commodel.ServiceNamePattern.MatchString(in.Probe.Service) {
				return errors.New("invalid probe service name")
			}
		}
		if in.Probe.Kind == "render" {
			u, err := url.Parse(in.Probe.URL)
			if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" || u.User != nil {
				return errors.New("render probe must name an http(s) target url")
			}
			if len(in.Probe.Steps) > 8 {
				return errors.New("render probe exceeds the browser step bound")
			}
			for _, step := range in.Probe.Steps {
				if step.Action != "click" && step.Action != "fill" && step.Action != "scroll" && step.Action != "wait" && step.Action != "wait_for" && step.Action != "evaluate" {
					return errors.New("invalid browser step action")
				}
			}
		}
		if in.Probe.Regex != "" {
			re, err := regexp.Compile(in.Probe.Regex)
			if err != nil || re.NumSubexp() == 0 {
				return errors.New("probe regex needs a capture group")
			}
		}
		if len(in.Probe.Extract) > extractMaxSteps {
			return errors.New("extract pipeline exceeds bounds")
		}
		for _, step := range in.Probe.Extract {
			if err := ValidateExtractStep(step); err != nil {
				return err
			}
		}
		if in.Probe.Kind == "script" || in.Probe.Kind == "command" {
			if err := hooks.Script(in.Probe.Script); err != nil {
				return fmt.Errorf("probe script does not compile: %w", err)
			}
		} else if in.Probe.Script != "" {
			return errors.New("script requires a script or command probe kind")
		}
		if in.Probe.Filter != "" && in.JobKind != "event" {
			return errors.New("a probe filter gates event subscriptions only")
		}
		if in.Probe.Filter != "" {
			// The event gate is a script too (SPEC-0081 R4.1): an
			// uncompilable filter must fail here, not silently at poll time.
			if err := hooks.Script(in.Probe.Filter); err != nil {
				return fmt.Errorf("probe filter does not compile: %w", err)
			}
		}
		if err := validateProbeNeeds(in.Probe); err != nil {
			return err
		}
		if in.Probe.Kind == "github_issue" {
			u, err := url.Parse(in.Probe.URL)
			if err != nil || u.Scheme != "https" || u.Host != "api.github.com" || u.User != nil || u.RawQuery != "" || u.Fragment != "" || in.Probe.Regex != "" || !regexp.MustCompile(`^/repos/[A-Za-z0-9_.-]+/[A-Za-z0-9_.-]+/issues/[1-9][0-9]*$`).MatchString(u.Path) {
				return errors.New("GitHub issue probe needs its public REST issue URL")
			}
		}
		if in.Delivery == "stream" && in.Probe.Kind == "mcp_tool" && !commodel.WaitCapableProbeTool(in.Probe.Tool) && in.Probe.MinIntervalSeconds == 0 {
			return errors.New("stream delivery on a plain poll tool requires probe.min_interval_seconds; wait-style tools (wait/settled/new_message) hold the call open and need none")
		}
		if in.Probe.MinIntervalSeconds != 0 && in.Delivery != "stream" {
			return errors.New("probe.min_interval_seconds is a stream-delivery cadence contract")
		}
		if in.Probe.MinIntervalSeconds != 0 && (in.Probe.MinIntervalSeconds < 5 || in.Probe.MinIntervalSeconds > 3600) {
			return errors.New("probe.min_interval_seconds must be 5 through 3600 seconds")
		}
	}
	if in.JobKind == "script" && hooks.Script(in.Input) != nil {
		return errors.New("script does not compile")
	}
	if strings.TrimSpace(in.Input) == "" || len(in.Input) > 64*1024 || envstore.LooksLikeEnv(in.Input) {
		return errors.New("invalid routine input")
	}
	if err := hooks.Channel(in); err != nil {
		return err
	}
	if in.Timezone == "" {
		return errors.New("timezone required")
	}
	return nil
}
