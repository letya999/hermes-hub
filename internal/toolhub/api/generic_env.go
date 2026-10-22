package api

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

// IsolatedCommandEnv prevents the trusted ToolHive control process from
// inheriting arbitrary host credentials. Only Docker locality and the OS
// runtime essentials cross the controller boundary; per-workload secret refs
// are appended explicitly by the caller.
func IsolatedToolHiveEnviron(extra map[string]string) []string {
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

func IsolatedCommandEnv(ctx context.Context, env map[string]string, binary string, args ...string) ([]byte, error) {
	cmd := exec.CommandContext(ctx, binary, args...) // #nosec G204 -- binary and args are fixed by the reviewed controller plan.
	cmd.Env = IsolatedToolHiveEnviron(env)
	var output bytes.Buffer
	cmd.Stdout, cmd.Stderr = &output, &output
	if err := cmd.Run(); err != nil {
		return nil, fmt.Errorf("isolated ToolHive command failed: %w: %s", err, strings.TrimSpace(output.String()))
	}
	return output.Bytes(), nil
}

// LocalCommandExit runs one controller-spawned binary, streaming combined
// output into the sink, and returns the process exit code so `docker exec`
// surfaces the tool's own status rather than a flattened error.
func LocalCommandExit(ctx context.Context, sink io.Writer, binary string, args ...string) (int, error) {
	cmd := exec.CommandContext(ctx, binary, args...) // #nosec G204 -- binary and args are fixed by the reviewed controller plan.
	cmd.Env = IsolatedToolHiveEnviron(nil)
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

// LocalCommandInput runs one binary with a bounded stdin payload — the
// channel that delivers cred-proxy rules into a running sidecar, where
// docker cp cannot reach the read-only rootfs.
func LocalCommandInput(ctx context.Context, stdin io.Reader, binary string, args ...string) ([]byte, error) {
	cmd := exec.CommandContext(ctx, binary, args...) // #nosec G204 -- binary and args are fixed by the reviewed controller plan.
	cmd.Env = IsolatedToolHiveEnviron(nil)
	cmd.Stdin = stdin
	out, err := cmd.CombinedOutput()
	if err != nil {
		return out, fmt.Errorf("local workload command failed: %w: %s", err, strings.TrimSpace(string(out)))
	}
	return out, nil
}

func LocalCommandEnv(ctx context.Context, env map[string]string, binary string, args ...string) ([]byte, error) {
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

func ReadGenericSecrets(path string, definition ToolDefinition) (map[string]string, error) {
	if path == "" {
		return nil, nil
	}
	if filepath.Ext(path) != ".env" || NoSymlinkPath(path) != nil {
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
	fileBound := FileBoundCredentialNames(definition)
	if !definition.CredentialsSatisfied(func(name string) bool {
		if values[name] != "" {
			return true
		}
		// File/json-bound inputs stage as <name>.secret siblings — the
		// injector removes them from env lines, so absence here is delivery,
		// not a missing credential.
		if !fileBound[name] {
			return false
		}
		_, err := os.Lstat(filepath.Join(filepath.Dir(path), name+".secret"))
		return err == nil
	}) {
		return nil, fmt.Errorf("%w: required runtime credential missing", ErrUnauthorized)
	}
	return values, nil
}

func EnvContainsForwardingSecret(env []string) bool {
	for _, value := range env {
		upper := strings.ToUpper(value)
		if strings.HasPrefix(upper, "HERMES_BRIDGE_TOKEN=") || strings.Contains(upper, "TOOLHIVE_SECRET") || strings.Contains(upper, "BRIDGE_AUTH=") {
			return true
		}
	}
	return false
}

func EnvHasBridgeToken(env []string) bool {
	for _, value := range env {
		if strings.HasPrefix(value, "HERMES_BRIDGE_TOKEN=") && len(strings.TrimPrefix(value, "HERMES_BRIDGE_TOKEN=")) >= 32 {
			return true
		}
	}
	return false
}

func GenericProxyConfig(egress []string) (string, error) {
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

// ProxyConfigFor selects the proxy renderer for one plan: web-class
// workloads render the deny-first policy with connection refinements, every
// other workload keeps the bounded allowlist. Refinements off the web class
// fail closed.
func ProxyConfigFor(plan ControllerPlan) (string, error) {
	if plan.Execution.EgressClass == EgressWeb {
		return WebProxyConfigFor(plan.Execution.Egress, plan.EgressAllow, plan.EgressDeny)
	}
	if len(plan.EgressAllow) > 0 || len(plan.EgressDeny) > 0 {
		return "", ErrIsolation
	}
	return GenericProxyConfig(plan.Execution.Egress)
}

// WebProxyConfigFor renders the web-class policy with connection
// refinements: deny entries join the built-in deny list, allow entries
// narrow the open tail to the named hosts. Deny is always evaluated first,
// so a host present in both lists stays denied.
func WebProxyConfigFor(extraDeny, allow, deny []string) (string, error) {
	config, err := renderWebProxy(extraDeny, deny)
	if err != nil {
		return "", err
	}
	if len(allow) == 0 {
		return config, nil
	}
	if len(allow) > 32 {
		return "", ErrIsolation
	}
	var names []string
	for _, entry := range allow {
		host, err := webEgressHost(entry)
		if err != nil {
			return "", err
		}
		names = append(names, host)
	}
	allowACL := "acl web_allow dstdomain " + strings.Join(names, " ") + "\n"
	marker := "http_access allow CONNECT web_port"
	return strings.Replace(config, marker, allowACL+"http_access allow CONNECT web_allow web_port", 1), nil
}

func webEgressHost(entry string) (string, error) {
	entry = strings.ToLower(strings.TrimSpace(entry))
	if entry == "" || strings.ContainsAny(entry, "\r\n") || strings.Contains(entry, "/") {
		return "", ErrIsolation
	}
	hostOnly, _, ok := strings.Cut(entry, ":")
	if !ok {
		hostOnly = entry
	}
	if !strings.Contains(hostOnly, ".") && hostOnly != "localhost" {
		return "", ErrIsolation
	}
	if !strings.HasPrefix(hostOnly, ".") && net.ParseIP(hostOnly) == nil {
		hostOnly = "." + hostOnly
	}
	return hostOnly, nil
}

// webDenyIPACLs are destination ranges a browser workload may never reach:
// unroutable, loopback, private, CGNAT, link-local and cloud metadata. The
// dst ACL resolves the CONNECT target at request time, so a rebound name is
// still denied on its resolved address.
var webDenyIPACLs = []string{
	"0.0.0.0/8", "10.0.0.0/8", "100.64.0.0/10", "127.0.0.0/8",
	"169.254.0.0/16", "169.254.169.254/32", "172.16.0.0/12", "192.0.0.0/24", "192.0.2.0/24",
	"192.168.0.0/16", "198.18.0.0/15", "198.51.100.0/24",
	"203.0.113.0/24", "224.0.0.0/4", "240.0.0.0/4",
	"::/128", "::1/128", "fc00::/7", "fe80::/10",
}

// webDenyHosts are hub-internal and loopback names a browser must never
// navigate to — page content is untrusted, and the hub control plane, the
// credential broker and the viewer endpoint stay out of reach.
var webDenyHosts = []string{
	"localhost", ".localhost", ".internal", ".local",
	"metadata.google.internal", "host.docker.internal",
	"toolhub", "credential-broker", "broker", "hub", "hermes-runtime",
}

// WebProxyConfig renders the web-class egress policy: CONNECT is limited to
// port 443 and every deny rule is evaluated before the allow tail, so the
// reviewed and built-in deny entries can never be bypassed by order.
func WebProxyConfig(extraDeny []string) (string, error) {
	return WebProxyConfigFor(extraDeny, nil, nil)
}

func renderWebProxy(extraDeny, connDeny []string) (string, error) {
	if len(extraDeny)+len(connDeny) > 64 {
		return "", ErrIsolation
	}
	hosts := append([]string(nil), webDenyHosts...)
	ips := append([]string(nil), webDenyIPACLs...)
	for _, entry := range append(append([]string(nil), extraDeny...), connDeny...) {
		entry = strings.ToLower(strings.TrimSpace(entry))
		if entry == "" || strings.ContainsAny(entry, "\r\n") {
			return "", ErrIsolation
		}
		if strings.Contains(entry, "/") {
			ips = append(ips, entry)
			continue
		}
		hostOnly, _, ok := strings.Cut(entry, ":")
		if !ok {
			hostOnly = entry
		}
		if net.ParseIP(hostOnly) != nil {
			ips = append(ips, hostOnly+"/32")
			continue
		}
		if !strings.Contains(hostOnly, ".") {
			return "", ErrIsolation
		}
		if !strings.HasPrefix(hostOnly, ".") {
			hostOnly = "." + hostOnly
		}
		hosts = append(hosts, hostOnly)
	}
	var lines []string
	lines = append(lines, "acl web_deny dstdomain "+strings.Join(hosts, " "))
	lines = append(lines, "acl web_deny_ip dst "+strings.Join(ips, " "))
	lines = append(lines, "acl web_port port 443")
	lines = append(lines, "http_access deny CONNECT web_deny")
	lines = append(lines, "http_access deny CONNECT web_deny_ip")
	lines = append(lines, "http_access allow CONNECT web_port")
	return "http_port 3128\nacl CONNECT method CONNECT\n" + strings.Join(lines, "\n") + "\nhttp_access deny all\ncache deny all\naccess_log none\ncache_log /dev/stderr\npid_filename /tmp/squid.pid\n", nil
}

func EqualStrings(a, b []string) bool { return strings.Join(a, "\x00") == strings.Join(b, "\x00") }
func ReflectExecution(a, b ExecutionPolicy) bool {
	return a.TimeoutSeconds == b.TimeoutSeconds && a.OutputBytes == b.OutputBytes && a.CPUMillis == b.CPUMillis && a.MemoryMiB == b.MemoryMiB && a.MaxPIDs == b.MaxPIDs && a.ShmSizeMiB == b.ShmSizeMiB && a.EgressClass == b.EgressClass && EqualStrings(a.Egress, b.Egress) && reflectTCPForwards(a.TCPForwards, b.TCPForwards) && ReflectMounts(a.Mounts, b.Mounts)
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
func ReflectMounts(a, b []Mount) bool {
	return len(a) == len(b) && func() bool {
		for i := range a {
			if a[i] != b[i] {
				return false
			}
		}
		return true
	}()
}
func Contains(values []string, wanted string) bool {
	for _, value := range values {
		if value == wanted {
			return true
		}
	}
	return false
}

func SecurityOptHasSeccomp(values []string, profilePath string) bool {
	if Contains(values, "seccomp="+profilePath) {
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
