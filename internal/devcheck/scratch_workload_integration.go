//go:build integration

package devcheck

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os/exec"
	"strings"
	"time"

	"github.com/letya999/hermes-hub/internal/toolhub"
)

// ScratchWorkloadCanary proves the disposable executor's isolation on real
// Docker: a hostile script cannot reach the runtime state, credentials,
// control sockets, a writable workspace or the network, and its only way
// back is the reviewed export channel. The container spec here mirrors
// DockerScratchExec; drift between them fails the assertions.
func ScratchWorkloadCanary(ctx context.Context, image string) error {
	if strings.TrimSpace(image) == "" {
		return errors.New("scratch workload canary image is required")
	}
	script := strings.Join([]string{
		`echo "UID=$(id -u)"`,
		`touch /etc/escape 2>/dev/null && echo "ETCWRITE=allowed" || echo "ETCWRITE=denied"`,
		`mkdir -p /workspace 2>/dev/null; touch /workspace/escape 2>/dev/null && echo "WSWRITE=allowed" || echo "WSWRITE=denied"`,
		`test "$(ls -A /state 2>/dev/null | wc -l)" = "0" && echo "STATE=empty" || echo "STATE=content"; touch /state/x 2>/dev/null && echo "STATEWRITE=allowed" || echo "STATEWRITE=denied"`,
		`ls /var/run/docker.sock /run/docker.sock 2>/dev/null >/dev/null && echo "DOCKER=present" || echo "DOCKER=absent"`,
		`env | grep -icE 'token|secret|api_key|password|auth=' | xargs -I{} echo "ENVSECRETS={}"`,
		`wget -q -T 3 -O /dev/null http://169.254.169.254/ 2>/dev/null && echo "NET=open" || echo "NET=blocked"`,
		`dd if=/dev/zero of=/scratch/fill bs=1M count=200 2>/dev/null; S=$(stat -c%s /scratch/fill 2>/dev/null || echo 0); test "$S" -lt 200000000 && echo "DISK=bounded" || echo "DISK=unbounded"`,
		`echo canary > "$OUTPUTS/out.txt"`,
		`echo HOSTILE-DONE`,
	}, "; ")
	request, err := json.Marshal(toolhub.ScratchExecRequest{Command: script, TimeoutSeconds: 120})
	if err != nil {
		return err
	}
	runCtx, cancel := context.WithTimeout(ctx, 3*time.Minute)
	defer cancel()
	args := []string{"run", "--rm", "-i", "--network", "none", "--read-only",
		"--cap-drop", "ALL", "--security-opt", "no-new-privileges:true", "--user", "10001:10001",
		"--tmpfs", "/scratch:rw,exec,size=64m", "--tmpfs", "/outputs:rw,size=64m", "--tmpfs", "/tmp:rw,size=16m",
		"--memory", "256m", "--cpus", "0.5", "--pids-limit", "64",
		image, "hubctl", "exec-scratch"}
	cmd := exec.CommandContext(runCtx, "docker", args...)
	cmd.Stdin = bytes.NewReader(request)
	out, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("scratch workload run: %w: %s", err, out)
	}
	var result toolhub.ScratchExecResult
	if err := json.Unmarshal(bytes.TrimSpace(out), &result); err != nil {
		return fmt.Errorf("scratch workload result: %w: %s", err, out)
	}
	stdout := result.Stdout
	checks := map[string]string{
		"UID=10001":         "sandbox ran as non-root",
		"ETCWRITE=denied":   "read-only base image accepted a write",
		"WSWRITE=denied":    "a writable workspace was mounted",
		"STATE=empty":       "Hermes state content is visible to the workload",
		"STATEWRITE=denied": "Hermes state is writable by the workload",
		"DOCKER=absent":     "docker socket is visible to the workload",
		"ENVSECRETS=0":      "credential material leaked into the workload env",
		"NET=blocked":       "metadata/network path is reachable",
		"HOSTILE-DONE":      "hostile script did not complete inside the sandbox",
	}
	var failures []string
	for marker, failure := range checks {
		if !strings.Contains(stdout, marker) {
			failures = append(failures, failure)
		}
	}
	if strings.Contains(stdout, "DISK=unbounded") {
		failures = append(failures, "scratch disk is unbounded")
	}
	if result.ExportTar == "" {
		failures = append(failures, "reviewed export channel produced no output")
	}
	if len(failures) > 0 {
		return fmt.Errorf("scratch workload isolation failed: %s (stdout: %s)", strings.Join(failures, "; "), stdout)
	}
	fmt.Println("scratch workload canary passed: non-root read-only sandbox, no state/credential/docker-socket/workspace/network path, bounded disk, reviewed export channel")
	return nil
}
