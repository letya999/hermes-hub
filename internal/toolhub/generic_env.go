package toolhub

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
)

// isolatedCommandEnv prevents the trusted ToolHive control process from
// inheriting arbitrary host credentials. Only Docker locality and the OS
// runtime essentials cross the controller boundary; per-workload secret refs
// are appended explicitly by the caller.
func isolatedToolHiveEnviron(extra map[string]string) []string {
	overridden := map[string]bool{}
	for key := range extra {
		overridden[strings.ToUpper(key)] = true
	}
	allowed := []string{"PATH", "SystemRoot", "WINDIR", "USERPROFILE", "HOME", "APPDATA", "LOCALAPPDATA", "TEMP", "TMP", "DOCKER_HOST", "DOCKER_CONTEXT"}
	env := make([]string, 0, len(allowed)+len(extra))
	for _, key := range allowed {
		if overridden[strings.ToUpper(key)] {
			continue
		}
		if value, ok := os.LookupEnv(key); ok {
			env = append(env, key+"="+value)
		}
	}
	for key, value := range extra {
		env = append(env, key+"="+value)
	}
	return env
}

func isolatedCommandEnv(ctx context.Context, env map[string]string, binary string, args ...string) ([]byte, error) {
	cmd := exec.CommandContext(ctx, binary, args...) // #nosec G204 -- binary and args are fixed by the reviewed controller plan.
	cmd.Env = isolatedToolHiveEnviron(env)
	var output bytes.Buffer
	cmd.Stdout, cmd.Stderr = &output, &output
	if err := cmd.Run(); err != nil {
		return nil, fmt.Errorf("isolated ToolHive command failed: %w: %s", err, strings.TrimSpace(output.String()))
	}
	return output.Bytes(), nil
}

// localCommandExit runs one controller-spawned binary, streaming combined
// output into the sink, and returns the process exit code so `docker exec`
// surfaces the tool's own status rather than a flattened error.
func localCommandExit(ctx context.Context, sink io.Writer, binary string, args ...string) (int, error) {
	cmd := exec.CommandContext(ctx, binary, args...) // #nosec G204 -- binary and args are fixed by the reviewed controller plan.
	cmd.Env = isolatedToolHiveEnviron(nil)
	if sink != nil {
		cmd.Stdout, cmd.Stderr = sink, sink
	}
	err := cmd.Run()
	if err == nil {
		return 0, nil
	}
	var exitErr *exec.ExitError
	if errors.As(err, &exitErr) {
		return exitErr.ExitCode(), nil
	}
	return -1, err
}

func localCommandEnv(ctx context.Context, env map[string]string, binary string, args ...string) ([]byte, error) {
	cmd := exec.CommandContext(ctx, binary, args...) // #nosec G204 -- binary and args are fixed by the reviewed controller plan.
	cmd.Env = os.Environ()
	for key, value := range env {
		cmd.Env = append(cmd.Env, key+"="+value)
	}
	var output bytes.Buffer
	cmd.Stdout, cmd.Stderr = &output, io.Discard
	if err := cmd.Run(); err != nil {
		return nil, fmt.Errorf("local workload command failed: %w", err)
	}
	return output.Bytes(), nil
}

func readGenericSecrets(path string, definition ToolDefinition) (map[string]string, error) {
	if path == "" {
		return nil, nil
	}
	if filepath.Ext(path) != ".env" || noSymlinkPath(path) != nil {
		return nil, ErrIsolation
	}
	body, err := os.ReadFile(path) // #nosec G304 -- path is the controller-owned workload file.
	if err != nil {
		return nil, err
	}
	allowed := map[string]bool{}
	for _, name := range definition.Environment {
		allowed[name] = true
	}
	for _, input := range definition.Credentials {
		allowed[input.Name] = true
	}
	values := map[string]string{}
	for _, line := range strings.Split(string(body), "\n") {
		if line == "" {
			continue
		}
		key, value, ok := strings.Cut(line, "=")
		if !ok || !allowed[key] || strings.ContainsAny(key, "\r\n") || strings.ContainsAny(value, "\r\n") || value == "" {
			return nil, ErrIsolation
		}
		if _, exists := values[key]; exists {
			return nil, ErrIsolation
		}
		values[key] = value
	}
	if !definition.credentialsSatisfied(func(name string) bool { return values[name] != "" }) {
		return nil, fmt.Errorf("%w: required runtime credential missing", ErrUnauthorized)
	}
	return values, nil
}

func envContainsForwardingSecret(env []string) bool {
	for _, value := range env {
		upper := strings.ToUpper(value)
		if strings.HasPrefix(upper, "HERMES_BRIDGE_TOKEN=") || strings.Contains(upper, "TOOLHIVE_SECRET") || strings.Contains(upper, "BRIDGE_AUTH=") {
			return true
		}
	}
	return false
}

func envHasBridgeToken(env []string) bool {
	for _, value := range env {
		if strings.HasPrefix(value, "HERMES_BRIDGE_TOKEN=") && len(strings.TrimPrefix(value, "HERMES_BRIDGE_TOKEN=")) >= 32 {
			return true
		}
	}
	return false
}

func genericProxyConfig(egress []string) (string, error) {
	if len(egress) == 0 || len(egress) > 32 {
		return "", ErrIsolation
	}
	var lines []string
	for i, host := range egress {
		host = strings.ToLower(strings.TrimSpace(host))
		if host == "" || strings.ContainsAny(host, "\r\n") {
			return "", ErrIsolation
		}
		if strings.Contains(host, "/") {
			lines = append(lines, fmt.Sprintf("acl hermes_%d dst %s", i, host))
		} else {
			hostOnly, port, ok := strings.Cut(host, ":")
			if !ok {
				hostOnly, port = host, "443"
			}
			// Squid dstdomain matches an apex exactly; the ".host" form also
			// covers subdomains, which is what a reviewed host means — an API
			// answered by team.slack.com must not be denied by "slack.com".
			// The registrable owner controls every subdomain, so the trust
			// boundary is unchanged. IP literals keep exact dstdomain form.
			if strings.Contains(hostOnly, ".") && net.ParseIP(hostOnly) == nil && !strings.HasPrefix(hostOnly, ".") {
				hostOnly = "." + hostOnly
			}
			if strings.Contains(hostOnly, ".") || hostOnly == "localhost" {
				lines = append(lines, fmt.Sprintf("acl hermes_%d dstdomain %s", i, hostOnly))
			} else {
				return "", ErrIsolation
			}
			lines = append(lines, fmt.Sprintf("acl hermes_port_%d port %s", i, port))
		}
	}
	for i := range egress {
		if strings.Contains(egress[i], "/") {
			lines = append(lines, fmt.Sprintf("http_access allow connect hermes_%d", i))
		} else {
			lines = append(lines, fmt.Sprintf("http_access allow connect hermes_%d hermes_port_%d", i, i))
		}
	}
	return "http_port 3128\nacl connect method CONNECT\n" + strings.Join(lines, "\n") + "\nhttp_access deny all\ncache deny all\naccess_log none\ncache_log /dev/stderr\npid_filename /tmp/squid.pid\n", nil
}

func equalStrings(a, b []string) bool { return strings.Join(a, "\x00") == strings.Join(b, "\x00") }
func reflectExecution(a, b ExecutionPolicy) bool {
	return a.TimeoutSeconds == b.TimeoutSeconds && a.OutputBytes == b.OutputBytes && a.CPUMillis == b.CPUMillis && a.MemoryMiB == b.MemoryMiB && a.MaxPIDs == b.MaxPIDs && equalStrings(a.Egress, b.Egress) && reflectTCPForwards(a.TCPForwards, b.TCPForwards) && reflectMounts(a.Mounts, b.Mounts)
}
func reflectTCPForwards(a, b []TCPForward) bool {
	return len(a) == len(b) && func() bool {
		for i := range a {
			if a[i] != b[i] {
				return false
			}
		}
		return true
	}()
}
func reflectMounts(a, b []Mount) bool {
	return len(a) == len(b) && func() bool {
		for i := range a {
			if a[i] != b[i] {
				return false
			}
		}
		return true
	}()
}
func contains(values []string, wanted string) bool {
	for _, value := range values {
		if value == wanted {
			return true
		}
	}
	return false
}

func securityOptHasSeccomp(values []string, profilePath string) bool {
	if contains(values, "seccomp="+profilePath) {
		return true
	}
	body, err := os.ReadFile(profilePath) // #nosec G304 -- absolute operator-owned profile validated before use.
	if err != nil {
		return false
	}
	var expected any
	if json.Unmarshal(body, &expected) != nil {
		return false
	}
	for _, value := range values {
		if !strings.HasPrefix(value, "seccomp=") {
			continue
		}
		var actual any
		if json.Unmarshal([]byte(strings.TrimPrefix(value, "seccomp=")), &actual) == nil && reflect.DeepEqual(actual, expected) {
			return true
		}
	}
	return false
}
