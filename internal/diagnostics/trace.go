package diagnostics

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"
)

const maxTraceEvents = 1000
const maxTraceOutput = 1 << 20

type TraceEvent struct {
	Time   time.Time
	Source string
	Line   string
}

type TraceOptions struct {
	JobID          string
	User           string
	SpacesRoot     string
	SupervisorLog  string
	Environment    string
	IncludeContent bool
	// Home resolves the runtime's Hermes home from trusted context settings only.
	Home        func(contextID, environment string) (string, error)
	DockerNames func(ctx context.Context, principal, contextID, environment string) ([]string, error)
	DockerLogs  func(ctx context.Context, name string) ([]byte, error)
}

type traceBinding struct{ principal, context, job, run, session string }

// Trace reads only authority-bound sources; every truncation/failure is visible.
func Trace(ctx context.Context, opts TraceOptions) ([]TraceEvent, error) {
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	binding, err := traceJobIdentity(opts.SpacesRoot, opts.JobID)
	if err != nil {
		return nil, err
	}
	if opts.User != "" && opts.User != binding.principal {
		return nil, errors.New("trace job belongs to another principal")
	}
	environment := opts.Environment
	if environment == "" {
		environment = "prod"
	}
	if environment != "dev" && environment != "prod" {
		return nil, errors.New("invalid trace environment")
	}
	home := filepath.Join(opts.SpacesRoot, binding.context)
	if err := traceOwnedPath(opts.SpacesRoot, home); err != nil {
		return nil, err
	}
	var events []TraceEvent
	outputBytes := 0
	limited := false
	add := func(event TraceEvent) bool {
		if len(events) >= maxTraceEvents-1 || outputBytes+len(event.Line) > maxTraceOutput-256 {
			limited = true
			return false
		}
		events = append(events, event)
		outputBytes += len(event.Line)
		return true
	}
	gap := func(source, reason string) { add(TraceEvent{Source: source, Line: "diagnostic_gap=" + reason}) }
	ids := map[string]bool{binding.job: true}
	plainIDs := map[string]bool{binding.job: true}
	if binding.run != "" {
		ids[binding.run] = true
		plainIDs[binding.run] = true
	}
	if binding.session != "" {
		ids[binding.session] = true
	}
	collect := func(source, line string) error {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		line = strings.TrimSpace(line)
		if line == "" {
			return nil
		}
		var fields struct {
			Principal   string `json:"principal"`
			PrincipalID string `json:"principal_id"`
			ContextID   string `json:"context_id"`
		}
		structured := json.Unmarshal([]byte(line), &fields) == nil
		matched := false
		if structured {
			if fields.Principal != "" && fields.Principal != binding.principal || fields.PrincipalID != "" && fields.PrincipalID != binding.principal || fields.ContextID != "" && fields.ContextID != binding.context {
				return nil
			}
			matched = traceMatches(line, ids)
		} else {
			matched = traceMatches(line, plainIDs)
		}
		if !matched {
			return nil
		}
		text := ProjectDiagnosticLine(line, opts.IncludeContent)
		if !opts.IncludeContent {
			metadata := map[string]string{"job_id": binding.job}
			if binding.run != "" && len(binding.run) <= 256 {
				metadata["run_id"] = Redact(binding.run)
			}
			if binding.session != "" && len(binding.session) <= 256 {
				metadata["session_id"] = Redact(binding.session)
			}
			encoded, _ := json.Marshal(metadata)
			text = string(encoded) + " " + text
		}
		if !add(TraceEvent{Time: traceLineTime(line), Source: source, Line: text}) {
			return errDiagnosticBatch
		}
		return nil
	}
	var seenFiles []os.FileInfo
	readFile := func(source, path string) {
		if limited || ctx.Err() != nil {
			return
		}
		info, err := os.Lstat(path)
		if os.IsNotExist(err) {
			return
		}
		if err != nil || !info.Mode().IsRegular() {
			gap(source, "source_unsafe")
			return
		}
		root, err := os.OpenRoot(filepath.Dir(path))
		if err != nil {
			gap(source, "source_unreadable")
			return
		}
		defer root.Close()
		f, err := root.Open(filepath.Base(path))
		if os.IsNotExist(err) {
			return
		}
		if err != nil {
			gap(source, "source_unreadable")
			return
		}
		defer f.Close()
		info, err = f.Stat()
		if err != nil || !info.Mode().IsRegular() {
			gap(source, "source_unsafe")
			return
		}
		for _, seen := range seenFiles {
			if os.SameFile(info, seen) {
				return
			}
		}
		seenFiles = append(seenFiles, info)
		if info.Size() > maxDiagnosticBatch {
			if _, err = f.Seek(info.Size()-maxDiagnosticBatch, io.SeekStart); err != nil {
				gap(source, "source_unreadable")
				return
			}
			// Drop the bounded first fragment; no partial line is interpreted as a record.
			var one [1]byte
			for i := 0; i < maxDiagnosticLine; i++ {
				if _, err = f.Read(one[:]); err != nil {
					break
				}
				if one[0] == '\n' {
					break
				}
			}
			gap(source, "source_tail_limited")
		}
		err = streamDiagnosticLines(io.LimitReader(f, maxDiagnosticBatch), func(line string) error { return collect(source, line) })
		if err != nil && !errors.Is(err, errDiagnosticBatch) {
			gap(source, "source_read_incomplete")
		}
	}
	if opts.SupervisorLog != "" {
		readFile("supervisor", opts.SupervisorLog)
		for i := 1; i <= rotatedLogFiles; i++ {
			readFile("supervisor."+strconv.Itoa(i), opts.SupervisorLog+"."+strconv.Itoa(i))
		}
	}
	hermesHome := home
	if opts.Home != nil {
		hermesHome, err = opts.Home(binding.context, environment)
		if err != nil {
			return nil, err
		}
	}
	if err := traceOwnedPath(home, hermesHome); err != nil {
		return nil, err
	}
	logsDir := filepath.Join(hermesHome, "logs")
	if err := traceOwnedPath(home, logsDir); err != nil {
		return nil, err
	}
	for _, name := range []string{"agent.log", "gateway.log", "errors.log"} {
		readFile("space/"+name, filepath.Join(logsDir, name))
	}
	if opts.DockerNames != nil && opts.DockerLogs != nil && !limited && ctx.Err() == nil {
		names, err := opts.DockerNames(ctx, binding.principal, binding.context, environment)
		if err != nil {
			gap("docker", "discovery_failed")
		} else {
			seen := map[string]bool{}
			for _, name := range names {
				if limited || ctx.Err() != nil {
					break
				}
				if !safeDiagnosticLabel(name) || seen[name] {
					continue
				}
				seen[name] = true
				if len(seen) > 32 {
					gap("docker", "source_count_limited")
					break
				}
				body, err := opts.DockerLogs(ctx, name)
				if err != nil {
					gap("docker/"+name, "source_unreadable")
					continue
				}
				if len(body) > maxDiagnosticBatch {
					gap("docker/"+name, "source_bytes_limited")
					body = body[:maxDiagnosticBatch]
				}
				if err := streamDiagnosticLines(strings.NewReader(string(body)), func(line string) error { return collect("docker/"+name, line) }); err != nil && !errors.Is(err, errDiagnosticBatch) {
					gap("docker/"+name, "source_read_incomplete")
				}
			}
		}
	}
	if ctx.Err() != nil {
		gap("trace", "deadline")
	}
	if limited {
		events = append(events, TraceEvent{Source: "trace", Line: "diagnostic_gap=output_limited"})
	}
	sort.SliceStable(events, func(i, j int) bool {
		if events[i].Time.Equal(events[j].Time) {
			return events[i].Source < events[j].Source
		}
		return events[i].Time.Before(events[j].Time)
	})
	return events, nil
}

func traceJobIdentity(spacesRoot, jobID string) (traceBinding, error) {
	var binding traceBinding
	if !safeDiagnosticLabel(jobID) {
		return binding, errors.New("invalid trace job ID")
	}
	body, err := readDiagnosticFile(filepath.Join(spacesRoot, ".control", "supervisor.json"), 32<<20)
	if err != nil {
		return binding, errors.New("trace authority record unavailable")
	}
	var state struct {
		Jobs map[string]struct {
			Request struct {
				PrincipalID string `json:"principal_id"`
				ContextID   string `json:"context_id"`
			} `json:"request"`
			Response struct {
				RunID     string `json:"run_id"`
				SessionID string `json:"session_id"`
			} `json:"response"`
		} `json:"jobs"`
	}
	if json.Unmarshal(body, &state) != nil {
		return binding, errors.New("invalid trace authority record")
	}
	job, found := state.Jobs[jobID]
	if !found || !safeDiagnosticLabel(job.Request.PrincipalID) {
		return binding, errors.New("trace job owner unknown")
	}
	contextID := job.Request.ContextID
	if contextID == "" {
		contextID = job.Request.PrincipalID
	}
	if !safeDiagnosticLabel(contextID) {
		return binding, errors.New("invalid trace context")
	}
	return traceBinding{principal: job.Request.PrincipalID, context: contextID, job: jobID, run: job.Response.RunID, session: job.Response.SessionID}, nil
}

func traceOwnedPath(root, path string) error {
	absRoot, err := filepath.Abs(root)
	if err != nil {
		return err
	}
	absPath, err := filepath.Abs(path)
	if err != nil {
		return err
	}
	rel, err := filepath.Rel(absRoot, absPath)
	if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return errors.New("trace home escapes owned context")
	}
	current := absRoot
	parts := []string{}
	if rel != "." {
		parts = strings.Split(rel, string(filepath.Separator))
	}
	for _, part := range append([]string{""}, parts...) {
		current = filepath.Join(current, part)
		info, err := os.Lstat(current)
		if os.IsNotExist(err) {
			continue
		}
		if err != nil {
			return err
		}
		if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
			return errors.New("unsafe trace home")
		}
	}
	return nil
}

func traceMatches(line string, ids map[string]bool) bool {
	var entry struct {
		JobID     string `json:"job_id"`
		RunID     string `json:"run_id"`
		SessionID string `json:"session_id"`
	}
	if json.Unmarshal([]byte(line), &entry) == nil {
		if entry.JobID != "" {
			return ids[entry.JobID]
		}
		if entry.RunID != "" {
			return ids[entry.RunID]
		}
		return entry.SessionID != "" && ids[entry.SessionID]
	}
	for _, field := range strings.Fields(line) {
		field = strings.Trim(field, `"',.;:()[]{}<>`)
		if key, value, ok := strings.Cut(field, "="); ok {
			if key != "job_id" && key != "run_id" && key != "session_id" {
				continue
			}
			field = strings.Trim(value, `"',.;:()[]{}<>`)
		}
		if ids[field] {
			return true
		}
	}
	return false
}

func traceLineTime(line string) time.Time {
	var record struct {
		Time string `json:"time"`
	}
	if json.Unmarshal([]byte(line), &record) == nil && record.Time != "" {
		if ts, err := time.Parse(time.RFC3339Nano, record.Time); err == nil {
			return ts
		}
	}
	if first, _, ok := strings.Cut(strings.TrimPrefix(line, "["), " "); ok {
		if ts, err := time.Parse(time.RFC3339Nano, strings.TrimSuffix(first, "]")); err == nil {
			return ts
		}
	}
	if len(line) >= 19 {
		for _, layout := range []string{"2006-01-02T15:04:05", "2006/01/02 15:04:05"} {
			if ts, err := time.Parse(layout, line[:19]); err == nil {
				return ts
			}
		}
	}
	return time.Time{}
}
