package diagnostics

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"github.com/gofrs/flock"
	"io"
	"log"
	"os"
	"path/filepath"
	"strings"
	"time"
)

const (
	maxLogBytes      = 100 << 20
	maxLinesPerCycle = 5000 // per-container throttle per collection

	// collectCycleBudget bounds one collection pass independently of the poll
	// interval; a container skipped when the budget expires keeps its cursor
	// and is retried on the next cycle.
	collectCycleBudget = 2 * time.Minute
	// containerDone marks a stopped container whose log tail was drained once;
	// docker logs against dead containers is what generated the failure spam.
	containerDone = "!"
)

// dockerCallTimeout bounds one Docker API call so a stalled daemon or a huge
// log fetch cannot starve the rest of the collection cycle. A var so tests
// can shrink it.
var dockerCallTimeout = 15 * time.Second

type DockerRun func(context.Context, ...string) ([]byte, error)

// Scope restricts collection to one stack: containers carrying the owning
// Compose project label, plus supervisor-spawned runtimes attached to the
// per-user agent network. Owner scopes host-supervisor lines: only lines
// naming the space owner as a whole token are copied, so one stack's
// collector cannot pull another user's host activity into its file.
type Scope struct {
	Project        string
	AgentNet       string
	Owner          string
	IncludeContent bool // Explicit operator opt-in; production defaults to metadata only.
}

func (s Scope) owns(project, networks string) bool {
	if s.Project != "" && project == s.Project {
		return true
	}
	if s.AgentNet == "" {
		return false
	}
	for _, network := range strings.Split(networks, ",") {
		if network == s.AgentNet {
			return true
		}
	}
	return false
}

type cursor struct {
	Version        int               `json:"version,omitempty"`
	Containers     map[string]string `json:"containers"`
	Ordinals       map[string]int    `json:"ordinals,omitempty"`
	Drained        map[string]bool   `json:"drained,omitempty"`
	Gaps           map[string]string `json:"gaps,omitempty"`
	Supervisor     int               `json:"supervisor"`
	HostGeneration string            `json:"host_generation,omitempty"`
}

// TokenMentioned reports whether token appears as a whole whitespace field;
// substring matches would leak lines about foreign identifiers that merely
// extend the token (alice vs alice2).
func TokenMentioned(line, token string) bool {
	if token == "" {
		return false
	}
	for _, field := range strings.Fields(line) {
		if strings.Trim(field, `"',.;:()[]{}<>`) == token {
			return true
		}
	}
	return false
}

// Follow copies new Docker and host-supervisor log lines into one bounded
// text file. ToolHub starts it automatically when diagnostics are enabled.
func Follow(ctx context.Context, path string, run DockerRun, interval time.Duration, scope Scope) {
	var reported string
	for {
		attempt, cancel := context.WithTimeout(ctx, collectCycleBudget)
		err := Collect(attempt, path, run, scope)
		cancel()
		if err != nil && ctx.Err() == nil && err.Error() != reported {
			log.Printf("diagnostic collector: %v", err)
			reported = err.Error()
		} else if err == nil {
			reported = ""
		}
		select {
		case <-ctx.Done():
			return
		case <-time.After(interval):
		}
	}
}

func Collect(ctx context.Context, path string, run DockerRun, scope Scope) error {
	nativeStream := run == nil
	if nativeStream {
		run = Docker
	}
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		return err
	}
	lock := flock.New(path + ".collector.lock")
	locked, err := lock.TryLock()
	if err != nil {
		return err
	}
	defer lock.Close()
	if !locked {
		return errors.New("diagnostic collector already active")
	}
	listCtx, listCancel := context.WithTimeout(ctx, dockerCallTimeout*2)
	listed, err := run(listCtx, "ps", "-a", "--format", "{{.ID}}\t{{.Names}}\t{{.Label \"com.docker.compose.project\"}}\t{{.Networks}}\t{{.Status}}")
	listCancel()
	if err != nil {
		return fmt.Errorf("list Docker containers: %w", err)
	}
	if len(listed) > maxDiagnosticBatch {
		return errors.New("container inventory exceeds bound")
	}
	current := cursor{Containers: map[string]string{}}
	var output bytes.Buffer
	gap := func(source, reason string) {
		if output.Len() < maxDiagnosticBatch {
			line := fmt.Sprintf("%s [%s] diagnostic_gap=%s\n", time.Now().UTC().Format(time.RFC3339Nano), source, reason)
			if output.Len()+len(line) <= maxDiagnosticBatch {
				output.WriteString(line)
			}
		}
	}
	cursorPath := path + ".cursor.json"
	if body, err := readDiagnosticFile(cursorPath, maxDiagnosticBatch); err == nil {
		if json.Unmarshal(body, &current) != nil {
			current = cursor{Containers: map[string]string{}}
			gap("collector", "corrupt_cursor_replay")
		}
	} else if !os.IsNotExist(err) {
		return err
	}
	if current.Version != 0 && current.Version != 2 {
		return errors.New("unsupported diagnostic cursor version")
	}
	next := cursor{Version: 2, Containers: map[string]string{}, Ordinals: map[string]int{}, Drained: map[string]bool{}, Gaps: map[string]string{}, Supervisor: current.Supervisor, HostGeneration: current.HostGeneration}
	var failures []string
	for _, row := range strings.Split(strings.TrimSpace(string(listed)), "\n") {
		fields := strings.Split(row, "\t")
		if len(fields) < 5 {
			continue
		}
		id, name, project, networks, status := fields[0], fields[1], fields[2], fields[3], fields[4]
		if !safeDiagnosticLabel(id) || !safeDiagnosticLabel(name) || !scope.owns(project, networks) {
			continue
		}
		last := current.Containers[id]
		ordinal := current.Ordinals[id]
		running := strings.HasPrefix(status, "Up")
		legacyDone := last == containerDone
		if legacyDone {
			last = ""
			ordinal = 0
		}
		next.Containers[id] = last
		next.Ordinals[id] = ordinal
		if !running && (current.Drained[id] || legacyDone) {
			next.Drained[id] = true
			continue
		}
		if !running && current.Gaps[id] == "unreadable_stopped" {
			next.Gaps[id] = current.Gaps[id]
			failures = append(failures, name)
			continue
		}
		if output.Len() > maxDiagnosticBatch-maxDiagnosticLine-1024 {
			continue
		}
		if current.Version == 0 && last != "" {
			gap(name, "legacy_cursor_replay")
		}
		if running && legacyDone {
			gap(name, "legacy_drained_position_missing")
		}
		args := []string{"logs", "--timestamps"}
		if last != "" {
			args = append(args, "--since", last)
		}
		lastTime := time.Time{}
		if last != "" {
			var err error
			lastTime, err = time.Parse(time.RFC3339Nano, last)
			if err != nil {
				gap(name, "invalid_source_cursor")
				next.Gaps[id] = "invalid_source_cursor"
				failures = append(failures, name)
				continue
			}
		}
		newLast, newOrdinal := last, ordinal
		sameAtCursor := 0
		streamOrdinal := 0
		streamStamp := time.Time{}
		count := 0
		consume := func(entry string) error {
			stamp, message, ok := strings.Cut(entry, " ")
			if !ok {
				return nil
			}
			parsed, err := time.Parse(time.RFC3339Nano, stamp)
			if err != nil {
				return nil
			}
			if parsed.Equal(streamStamp) {
				streamOrdinal++
			} else {
				streamStamp = parsed
				streamOrdinal = 1
			}
			if parsed.Before(lastTime) {
				return nil
			}
			if parsed.Equal(lastTime) {
				sameAtCursor++
				if sameAtCursor <= ordinal {
					return nil
				}
			}
			if count >= maxLinesPerCycle || output.Len() > maxDiagnosticBatch-maxDiagnosticLine-1024 {
				return errDiagnosticBatch
			}
			if !lastTime.IsZero() && count == 0 && !parsed.Equal(lastTime) && sameAtCursor < ordinal {
				gap(name, "source_position_missing")
			}
			if len(entry) > maxDiagnosticLine {
				return errDiagnosticLine
			}
			fmt.Fprintf(&output, "%s [%s] source_position=%s/%s/%d %s\n", stamp, name, id, parsed.UTC().Format(time.RFC3339Nano), streamOrdinal, ProjectDiagnosticLine(message, scope.IncludeContent))
			newLast, newOrdinal = stamp, streamOrdinal
			count++
			return nil
		}
		callCtx, callCancel := context.WithTimeout(ctx, dockerCallTimeout)
		var readErr error
		if nativeStream {
			readErr = dockerLines(callCtx, consume, append(args, id)...)
		} else {
			body, err := run(callCtx, append(args, id)...)
			readErr = err
			if err == nil {
				readErr = streamDiagnosticLines(bytes.NewReader(body), consume)
			}
		}
		callCancel()
		limited := errors.Is(readErr, errDiagnosticBatch)
		if readErr != nil && !limited {
			reason := "source_read_failed"
			if !running {
				reason = "unreadable_stopped"
			}
			if errors.Is(readErr, errDiagnosticLine) {
				reason = "oversized_source_record"
			}
			gap(name, reason)
			next.Gaps[id] = reason
			failures = append(failures, name)
			// Keep the old cursor: any durable data replay carries the same source_position.
			continue
		}
		next.Containers[id], next.Ordinals[id] = newLast, newOrdinal
		if !lastTime.IsZero() && count == 0 && sameAtCursor < ordinal {
			gap(name, "source_position_missing")
			next.Gaps[id] = "source_position_missing"
		} else {
			next.Drained[id] = !running && !limited
		}
	}
	for id := range current.Containers {
		if _, ok := next.Containers[id]; !ok {
			gap("collector", "source_disappeared")
		}
	}
	if scope.Owner != "" && output.Len() < maxDiagnosticBatch-maxDiagnosticLine-1024 {
		hostPath := filepath.Join(filepath.Dir(path), "host", "supervisor.log")
		if _, newErr := os.Lstat(filepath.Dir(hostPath)); os.IsNotExist(newErr) {
			if _, legacyErr := os.Lstat(filepath.Join(filepath.Dir(path), "supervisor.log")); legacyErr == nil {
				gap("collector", "legacy_host_mount_requires_recreate")
			}
		}
		generation, genErr := readDiagnosticFile(hostPath+".generation", 128)
		if genErr == nil && (len(generation) != 32 || !validGeneration(string(generation))) {
			genErr = errors.New("invalid host generation")
		}
		if genErr != nil && !os.IsNotExist(genErr) {
			gap("host-supervisor", "generation_unreadable")
		} else if host, err := openDiagnosticFile(hostPath); err == nil {
			offset := int64(current.Supervisor)
			info, statErr := host.Stat()
			if statErr == nil {
				if string(generation) != current.HostGeneration || offset < 0 || offset > info.Size() {
					gap("host-supervisor", "source_rotated")
					offset = 0
				}
				if _, err = host.Seek(offset, io.SeekStart); err == nil {
					reader := bufio.NewReaderSize(host, maxDiagnosticLine)
					for lines := 0; lines < maxLinesPerCycle && output.Len() < maxDiagnosticBatch-maxDiagnosticLine-1024; lines++ {
						raw, readErr := reader.ReadSlice('\n')
						if readErr != nil {
							if !errors.Is(readErr, io.EOF) {
								gap("host-supervisor", "source_record_unreadable")
							}
							break
						}
						offset += int64(len(raw))
						line := strings.TrimSuffix(string(raw), "\n")
						if ownerMentioned(line, scope.Owner) {
							fmt.Fprintf(&output, "[%s] [host-supervisor] %s source_position=host/%s/%d\n", time.Now().UTC().Format(time.RFC3339Nano), ProjectDiagnosticLine(line, scope.IncludeContent), generation, offset)
						}
					}
					next.Supervisor = int(offset)
					next.HostGeneration = string(generation)
				}
			}
			_ = host.Close()
		} else if !os.IsNotExist(err) {
			gap("host-supervisor", "source_unreadable")
		}
	}
	if err := appendDiagnostic(path, output.Bytes(), maxLogBytes); err != nil {
		return err
	}
	body, err := json.Marshal(next)
	if err != nil {
		return err
	}
	if err = syncedReplace(cursorPath, body); err != nil {
		return err
	}
	if len(failures) > 0 {
		return fmt.Errorf("docker logs failed for: %s", strings.Join(failures, ", "))
	}
	return nil
}

func readDiagnosticFile(path string, limit int64) ([]byte, error) {
	f, err := openDiagnosticFile(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil {
		return nil, err
	}
	if !info.Mode().IsRegular() || info.Size() > limit {
		return nil, errors.New("diagnostic file exceeds safe bound or type")
	}
	body, err := io.ReadAll(io.LimitReader(f, limit+1))
	if err != nil {
		return nil, err
	}
	if int64(len(body)) > limit {
		return nil, errors.New("diagnostic file exceeds safe bound")
	}
	return body, nil
}

func safeDiagnosticLabel(value string) bool {
	if value == "" || value == "." || value == ".." || len(value) > 128 {
		return false
	}
	for _, r := range value {
		if !(r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' || r == '_' || r == '-' || r == '.') {
			return false
		}
	}
	return true
}

func validGeneration(value string) bool {
	for _, r := range value {
		if !(r >= '0' && r <= '9' || r >= 'a' && r <= 'f') {
			return false
		}
	}
	return true
}

func ownerMentioned(line, owner string) bool {
	var entry struct {
		Principal   string `json:"principal"`
		PrincipalID string `json:"principal_id"`
	}
	if json.Unmarshal([]byte(line), &entry) == nil {
		return entry.Principal == owner || entry.PrincipalID == owner
	}
	return TokenMentioned(line, owner)
}

// Source reads never follow an alias into credential/config files, even locally.
func openDiagnosticFile(path string) (*os.File, error) {
	dirInfo, err := os.Lstat(filepath.Dir(path))
	if err != nil {
		return nil, err
	}
	if !dirInfo.IsDir() || dirInfo.Mode()&os.ModeSymlink != 0 {
		return nil, errors.New("unsafe diagnostic source directory")
	}
	root, err := os.OpenRoot(filepath.Dir(path))
	if err != nil {
		return nil, err
	}
	defer root.Close()
	info, err := root.Lstat(filepath.Base(path))
	if err != nil {
		return nil, err
	}
	if !info.Mode().IsRegular() {
		return nil, errors.New("unsafe diagnostic source")
	}
	f, err := root.Open(filepath.Base(path))
	if err != nil {
		return nil, err
	}
	opened, err := f.Stat()
	if err != nil || !os.SameFile(info, opened) {
		_ = f.Close()
		return nil, errors.New("diagnostic source changed during open")
	}
	return f, nil
}
