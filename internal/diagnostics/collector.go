package diagnostics

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"os"
	"os/exec"
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
	Project  string
	AgentNet string
	Owner    string
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
	Containers map[string]string `json:"containers"`
	Supervisor int               `json:"supervisor"`
}

func Docker(ctx context.Context, args ...string) ([]byte, error) {
	return exec.CommandContext(ctx, "docker", args...).CombinedOutput() // #nosec G204 -- fixed read-only Docker commands and IDs returned by Docker.
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
	listCtx, listCancel := context.WithTimeout(ctx, dockerCallTimeout*2)
	listed, err := run(listCtx, "ps", "-a", "--format", "{{.ID}}\t{{.Names}}\t{{.Label \"com.docker.compose.project\"}}\t{{.Networks}}\t{{.Status}}")
	listCancel()
	if err != nil {
		return fmt.Errorf("list Docker containers: %w", err)
	}
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		return err
	}
	cursorPath := path + ".cursor.json"
	current := cursor{Containers: map[string]string{}}
	if body, err := os.ReadFile(cursorPath); err == nil {
		if err := json.Unmarshal(body, &current); err != nil {
			log.Printf("diagnostic cursor reset after invalid JSON: %v", err)
			current = cursor{Containers: map[string]string{}}
		}
		if current.Containers == nil {
			current.Containers = map[string]string{}
		}
	} else if !os.IsNotExist(err) {
		return err
	}
	next := cursor{Containers: map[string]string{}, Supervisor: current.Supervisor}
	var output bytes.Buffer
	var failures []string
	for _, line := range strings.Split(strings.TrimSpace(string(listed)), "\n") {
		fields := strings.Split(line, "\t")
		for len(fields) < 5 {
			fields = append(fields, "")
		}
		id, name, project, networks, status := fields[0], fields[1], fields[2], fields[3], fields[4]
		if id == "" || !scope.owns(project, networks) {
			continue
		}
		last := current.Containers[id]
		running := strings.HasPrefix(status, "Up")
		if running && last == containerDone {
			last = "" // same ID restarted: drain the new life once
		}
		if !running && last == containerDone {
			next.Containers[id] = containerDone // keep the mark or it is re-drained
			continue
		}
		args := []string{"logs", "--timestamps", "--tail", fmt.Sprint(maxLinesPerCycle)}
		if last != "" && last != containerDone {
			args = append(args, "--since", last)
		}
		callCtx, callCancel := context.WithTimeout(ctx, dockerCallTimeout)
		body, err := run(callCtx, append(args, id)...)
		callCancel()
		if err != nil {
			// One failing container must not stall collection for the rest.
			// Running containers keep their position so the next cycle retries;
			// stopped ones are marked done — dead-container log reads fail
			// deterministically through the proxy, and retrying them every
			// cycle is what produced the failure spam this avoids.
			failures = append(failures, name)
			if running {
				next.Containers[id] = last
			} else {
				next.Containers[id] = containerDone
			}
			continue
		}
		for _, entry := range strings.Split(strings.TrimSuffix(string(body), "\n"), "\n") {
			stamp, message, ok := strings.Cut(entry, " ")
			if !ok || stamp <= last {
				continue
			}
			if _, err := time.Parse(time.RFC3339Nano, stamp); err != nil {
				continue
			}
			fmt.Fprintf(&output, "%s [%s] %s\n", stamp, name, Redact(message))
			last = stamp
		}
		if running {
			next.Containers[id] = last
		} else {
			next.Containers[id] = containerDone
		}
	}
	hostPath := filepath.Join(filepath.Dir(path), "supervisor.log")
	if host, err := os.Open(hostPath); err == nil {
		offset := int64(next.Supervisor)
		if info, err := host.Stat(); err == nil {
			if offset < 0 || offset > info.Size() {
				offset = 0 // host log rolled over
			}
			if _, err := host.Seek(offset, 0); err == nil {
				if tail, err := io.ReadAll(host); err == nil {
					for _, line := range strings.Split(strings.TrimSuffix(string(tail), "\n"), "\n") {
						if line == "" || !TokenMentioned(line, scope.Owner) {
							continue
						}
						fmt.Fprintf(&output, "[%s] [host-supervisor] %s\n", time.Now().UTC().Format(time.RFC3339Nano), Redact(line))
					}
					next.Supervisor = int(info.Size())
				}
			}
		}
		_ = host.Close()
	}
	if output.Len() > 0 {
		data := output.Bytes()
		if len(data) > maxLogBytes {
			data = data[len(data)-maxLogBytes:]
			if cut := bytes.IndexByte(data, '\n'); cut >= 0 {
				data = data[cut+1:]
			}
		}
		flags := os.O_CREATE | os.O_APPEND | os.O_WRONLY
		if info, err := os.Stat(path); err == nil && info.Size()+int64(len(data)) > maxLogBytes {
			flags = os.O_CREATE | os.O_TRUNC | os.O_WRONLY
		}
		f, err := os.OpenFile(path, flags, 0600) // #nosec G304 -- fixed project .local diagnostics path.
		if err != nil {
			return err
		}
		_, writeErr := f.Write(data)
		closeErr := f.Close()
		if writeErr != nil {
			return writeErr
		}
		if closeErr != nil {
			return closeErr
		}
	}
	body, err := json.Marshal(next)
	if err != nil {
		return err
	}
	tmp, err := os.CreateTemp(filepath.Dir(path), "diagnostics-cursor-*.tmp")
	if err != nil {
		return err
	}
	defer os.Remove(tmp.Name())
	if err := tmp.Chmod(0600); err != nil {
		tmp.Close()
		return err
	}
	if _, err := tmp.Write(body); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	if err := os.Remove(cursorPath); err != nil && !os.IsNotExist(err) {
		return err
	}
	if err := os.Rename(tmp.Name(), cursorPath); err != nil {
		return err
	}
	if len(failures) > 0 {
		return fmt.Errorf("docker logs failed for: %s", strings.Join(failures, ", "))
	}
	return nil
}
