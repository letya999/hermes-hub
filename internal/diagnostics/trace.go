package diagnostics

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

// TraceEvent is one redacted, timestamped line from any chain component.
type TraceEvent struct {
	Time   time.Time
	Source string
	Line   string
}

// TraceOptions describes where `hubctl trace` may read. Docker access is
// injected so the merge logic is testable without a daemon.
type TraceOptions struct {
	JobID         string
	User          string
	SpacesRoot    string
	SupervisorLog string
	DockerNames   func(ctx context.Context) ([]string, error)
	DockerLogs    func(ctx context.Context, name string) ([]byte, error)
}

// Trace merges supervisor log lines, the job owner's Hermes space logs and
// managed-container logs that mention the job's lineage (job_id, run_id,
// session_id), sorted by time and redacted (SPEC-0053 R3.5). Space logs are
// read only from the job's own principal — tracing another user's job is
// refused outright.
func Trace(ctx context.Context, opts TraceOptions) ([]TraceEvent, error) {
	principal, ids, err := traceJobIdentity(opts.SpacesRoot, opts.JobID)
	if err != nil {
		return nil, err
	}
	if principal == "" {
		principal = opts.User
	}
	if opts.User != "" && principal != "" && opts.User != principal {
		return nil, fmt.Errorf("job %s belongs to principal %s, not %s", opts.JobID, principal, opts.User)
	}
	var events []TraceEvent
	collect := func(source string, data []byte) {
		for _, line := range strings.Split(string(data), "\n") {
			line = strings.TrimSpace(line)
			if line == "" || !traceMatches(line, ids) {
				continue
			}
			events = append(events, TraceEvent{Time: traceLineTime(line), Source: source, Line: Redact(line)})
		}
	}
	if b, err := os.ReadFile(opts.SupervisorLog); err == nil {
		collect("supervisor", b)
	}
	if principal != "" {
		logsDir := filepath.Join(opts.SpacesRoot, principal, "logs")
		for _, name := range []string{"agent.log", "gateway.log", "errors.log"} {
			if b, err := os.ReadFile(filepath.Join(logsDir, name)); err == nil {
				collect("space/"+name, b)
			}
		}
	}
	if opts.DockerNames != nil && opts.DockerLogs != nil {
		if names, err := opts.DockerNames(ctx); err == nil {
			for _, name := range names {
				if b, err := opts.DockerLogs(ctx, name); err == nil {
					collect("docker/"+name, b)
				}
			}
		}
	}
	sort.SliceStable(events, func(i, j int) bool {
		if events[i].Time.Equal(events[j].Time) {
			return events[i].Source < events[j].Source
		}
		return events[i].Time.Before(events[j].Time)
	})
	return events, nil
}

// traceJobIdentity resolves the job's principal and correlation IDs from the
// persisted supervisor state. A missing file or job is not an error: trace
// still merges host/container lines keyed on the job ID alone.
func traceJobIdentity(spacesRoot, jobID string) (string, map[string]bool, error) {
	ids := map[string]bool{jobID: true}
	b, err := os.ReadFile(filepath.Join(spacesRoot, ".control", "supervisor.json"))
	if err != nil {
		return "", ids, nil
	}
	var state struct {
		Jobs map[string]struct {
			Request struct {
				PrincipalID string `json:"principal_id"`
			} `json:"request"`
			Response struct {
				RunID     string `json:"run_id"`
				SessionID string `json:"session_id"`
			} `json:"response"`
		} `json:"jobs"`
	}
	if json.Unmarshal(b, &state) != nil {
		return "", ids, nil
	}
	job, found := state.Jobs[jobID]
	if !found {
		return "", ids, nil
	}
	if job.Response.RunID != "" {
		ids[job.Response.RunID] = true
	}
	if job.Response.SessionID != "" {
		ids[job.Response.SessionID] = true
	}
	return job.Request.PrincipalID, ids, nil
}

func traceMatches(line string, ids map[string]bool) bool {
	for id := range ids {
		if strings.Contains(line, id) {
			return true
		}
	}
	return false
}

// traceLineTime extracts a timestamp from a JSON log record or a common
// timestamp prefix; lines without one sort first (zero time).
func traceLineTime(line string) time.Time {
	if strings.HasPrefix(line, "{") {
		var record struct {
			Time string `json:"time"`
		}
		if json.Unmarshal([]byte(line), &record) == nil && record.Time != "" {
			if ts, err := time.Parse(time.RFC3339Nano, record.Time); err == nil {
				return ts
			}
		}
	}
	for _, layout := range []string{"2006-01-02T15:04:05Z07:00", "2006-01-02T15:04:05", "2006/01/02 15:04:05"} {
		if len(line) >= len(layout) {
			if ts, err := time.Parse(layout, line[:len(layout)]); err == nil {
				return ts
			}
		}
	}
	return time.Time{}
}
