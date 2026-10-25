//go:build integration

package devcheck

import (
	"bufio"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"os/exec"
	"time"
)

// connectorBenchImage is the fixture provider image: small, present on every
// CI runner and already pulled in local stacks.
const connectorBenchImage = "python:3.12-slim"

// connectorBenchServer is a minimal newline-delimited JSON-RPC MCP server:
// initialize, tools/list and one echo tool, speaking MCP on the container's
// own stdio — the same channel a real container workload serves.
const connectorBenchServer = `import sys,json
for line in sys.stdin:
    line=line.strip()
    if not line:continue
    m=json.loads(line)
    i=m.get("id");me=m.get("method","")
    if me=="initialize":r={"protocolVersion":"2024-11-05","capabilities":{"tools":{}},"serverInfo":{"name":"bench-fixture","version":"0.1"}}
    elif me=="tools/list":r={"tools":[{"name":"echo","description":"echo","inputSchema":{"type":"object","properties":{"message":{"type":"string"}}}}]}
    elif me=="tools/call":r={"content":[{"type":"text","text":"ok"}],"isError":False}
    elif me.startswith("notifications/"):continue
    else:r={"error":{"code":-32601,"message":"nf"}}
    if i is not None:
        sys.stdout.write(json.dumps({"jsonrpc":"2.0","id":i,"result":r})+"\n");sys.stdout.flush()
`

// ConnectorBench measures the real workload lifecycle the #286 budgets cover
// on a fixture MCP image already local: container create, start+initialize
// handshake through the tools/list admission probe, the first tools/call and
// removal. Install = create + handshake (start through the tools/list reply),
// the same work a T0/T1 install performs after the image is present. The
// verdict fails on budget regression: install p95 >15 s, first call p95 >2 s,
// remove p95 >5 s.
func ConnectorBench(ctx context.Context, runs int, out io.Writer) error {
	if runs < 5 || runs > 60 {
		return fmt.Errorf("runs must be between 5 and 60")
	}
	if _, err := cliDocker(ctx, "image", "inspect", connectorBenchImage); err != nil {
		if err := streamDocker(ctx, "pull", connectorBenchImage); err != nil {
			return fmt.Errorf("fixture image unavailable: %w", err)
		}
	}
	samples := make([]connectorBenchSample, 0, runs)
	for i := 0; i < runs; i++ {
		if err := ctx.Err(); err != nil {
			return err
		}
		sample, err := connectorBenchRun(ctx)
		if err != nil {
			return fmt.Errorf("bench run %d: %w", i, err)
		}
		samples = append(samples, sample)
	}
	report := connectorBenchReport{
		Install:   benchPhase(samples, func(s connectorBenchSample) float64 { return s.InstallMS }),
		FirstCall: benchPhase(samples, func(s connectorBenchSample) float64 { return s.FirstCallMS }),
		Remove:    benchPhase(samples, func(s connectorBenchSample) float64 { return s.RemoveMS }),
	}
	if err := json.NewEncoder(out).Encode(report); err != nil {
		return err
	}
	return connectorBenchVerdict(report)
}

// connectorBenchRun times one full create→handshake→probe→call→remove cycle.
// The workload profile mirrors production: read-only rootfs, no capabilities,
// no-new-privileges, network none, tmpfs /tmp, unprivileged user.
func connectorBenchRun(ctx context.Context) (sample connectorBenchSample, err error) {
	var nonce [5]byte
	if _, err := rand.Read(nonce[:]); err != nil {
		return sample, err
	}
	name := "hermes-connector-bench-" + hex.EncodeToString(nonce[:])
	defer func() {
		cleanup, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		if _, rmErr := cliDocker(cleanup, "rm", "-f", name); rmErr != nil {
			err = fmt.Errorf("%v; cleanup %s: %v", err, name, rmErr)
		}
	}()
	start := time.Now()
	if _, err := cliDocker(ctx, "create", "--name", name, "-i",
		"--network", "none", "--read-only", "--tmpfs", "/tmp",
		"--cap-drop", "ALL", "--security-opt", "no-new-privileges",
		"--user", "10001:10001",
		connectorBenchImage, "python", "-u", "-c", connectorBenchServer); err != nil {
		return sample, fmt.Errorf("create: %w", err)
	}
	sample.CreateMS = elapsedMS(start)

	attach := exec.CommandContext(ctx, "docker", "start", "-ai", name) // #nosec G204 -- fixed bench arguments.
	stdin, err := attach.StdinPipe()
	if err != nil {
		return sample, err
	}
	stdout, err := attach.StdoutPipe()
	if err != nil {
		return sample, err
	}
	attach.Stderr = io.Discard
	start = time.Now()
	if err := attach.Start(); err != nil {
		return sample, fmt.Errorf("start+attach: %w", err)
	}
	reader := bufio.NewReaderSize(stdout, 1<<20)
	request := func(id int, method string) error {
		body, _ := json.Marshal(map[string]any{"jsonrpc": "2.0", "id": id, "method": method, "params": map[string]any{}})
		_, err := stdin.Write(append(body, '\n'))
		return err
	}
	reply := func() error {
		line, err := reader.ReadBytes('\n')
		if err != nil {
			return err
		}
		var msg struct {
			Error *json.RawMessage `json:"error"`
		}
		if json.Unmarshal(line, &msg) != nil {
			return fmt.Errorf("unreadable reply: %s", line)
		}
		if msg.Error != nil {
			return fmt.Errorf("jsonrpc error: %s", *msg.Error)
		}
		return nil
	}
	kill := func() {
		stdin.Close()
		_ = attach.Process.Kill()
		_ = attach.Wait()
	}
	if err := request(1, "initialize"); err != nil {
		kill()
		return sample, fmt.Errorf("initialize write: %w", err)
	}
	if err := reply(); err != nil {
		kill()
		return sample, fmt.Errorf("initialize reply: %w", err)
	}
	if err := request(2, "tools/list"); err != nil {
		kill()
		return sample, fmt.Errorf("tools/list write: %w", err)
	}
	if err := reply(); err != nil {
		kill()
		return sample, fmt.Errorf("tools/list reply: %w", err)
	}
	// Install ends when the admission probe has its answer: create through
	// the tools/list reply.
	sample.HandshakeMS = elapsedMS(start)
	sample.InstallMS = sample.CreateMS + sample.HandshakeMS
	start = time.Now()
	if err := request(3, "tools/call"); err != nil {
		kill()
		return sample, fmt.Errorf("tools/call write: %w", err)
	}
	if err := reply(); err != nil {
		kill()
		return sample, fmt.Errorf("tools/call reply: %w", err)
	}
	sample.FirstCallMS = elapsedMS(start)
	kill()

	start = time.Now()
	if _, err := cliDocker(ctx, "rm", "-f", name); err != nil {
		return sample, fmt.Errorf("remove: %w", err)
	}
	sample.RemoveMS = elapsedMS(start)
	return sample, nil
}

func elapsedMS(start time.Time) float64 {
	return float64(time.Since(start).Microseconds()) / 1000
}
