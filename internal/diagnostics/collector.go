package diagnostics

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"log"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
)

const maxLogBytes = 100 << 20

type DockerRun func(context.Context, ...string) ([]byte, error)

type cursor struct {
	Containers map[string]string `json:"containers"`
	Supervisor int               `json:"supervisor"`
}

func Docker(ctx context.Context, args ...string) ([]byte, error) {
	return exec.CommandContext(ctx, "docker", args...).CombinedOutput() // #nosec G204 -- fixed read-only Docker commands and IDs returned by Docker.
}

// Follow copies new Docker and host-supervisor log lines into one bounded
// text file. ToolHub starts it automatically when diagnostics are enabled.
func Follow(ctx context.Context, path string, run DockerRun, interval time.Duration) {
	var reported string
	for {
		attempt, cancel := context.WithTimeout(ctx, interval)
		err := Collect(attempt, path, run)
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

func Collect(ctx context.Context, path string, run DockerRun) error {
	listed, err := run(ctx, "ps", "-a", "--format", "{{.ID}}\t{{.Names}}")
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
	for _, line := range strings.Split(strings.TrimSpace(string(listed)), "\n") {
		id, name, ok := strings.Cut(line, "\t")
		if !ok || id == "" || (!strings.HasPrefix(name, "hermes-") && !strings.HasPrefix(name, "work-")) {
			continue
		}
		last := current.Containers[id]
		args := []string{"logs", "--timestamps"}
		if last == "" {
			args = append(args, "--tail", "1000")
		} else {
			args = append(args, "--since", last)
		}
		body, err := run(ctx, append(args, id)...)
		if err != nil {
			return fmt.Errorf("docker logs %s: %w", name, err)
		}
		for _, entry := range strings.Split(strings.TrimSuffix(string(body), "\n"), "\n") {
			stamp, message, ok := strings.Cut(entry, " ")
			if !ok || stamp <= last {
				continue
			}
			if _, err := time.Parse(time.RFC3339Nano, stamp); err != nil {
				continue
			}
			fmt.Fprintf(&output, "%s [%s] %s\n", stamp, name, message)
			last = stamp
		}
		next.Containers[id] = last
	}
	if host, err := os.ReadFile(filepath.Join(filepath.Dir(path), "supervisor.log")); err == nil {
		if current.Supervisor < 0 || current.Supervisor > len(host) {
			next.Supervisor = 0 // host log rolled over
		}
		for _, line := range strings.Split(strings.TrimSuffix(string(host[next.Supervisor:]), "\n"), "\n") {
			if line != "" {
				fmt.Fprintf(&output, "[%s] [host-supervisor] %s\n", time.Now().UTC().Format(time.RFC3339Nano), line)
			}
		}
		next.Supervisor = len(host)
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
	return os.Rename(tmp.Name(), cursorPath)
}
