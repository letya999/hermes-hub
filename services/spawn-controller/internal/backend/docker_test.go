package backend_test

import (
	"context"
	"crypto/rand"
	"encoding/json"
	"os/exec"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/letya999/hermes-hub/services/spawn-controller/internal/backend"
	"github.com/letya999/hermes-hub/services/spawn-controller/internal/backend/backendtest"
)

// Docker fixtures live entirely behind two markers: the name prefix and the
// test label. Cleanup removes only what carries the label; nothing else on
// this daemon (a live dev stack) is ever touched.
const (
	testContainerPrefix = "hermes-m40-test-"
	testNetworkPrefix   = "hermes-m40-test-net-"
	testLabel           = "hermes.test=chg-0336"
)

var dockerProbe = struct {
	sync.Once
	ok     bool
	reason string
}{}

func dockerAvailable() (bool, string) {
	dockerProbe.Once.Do(func() {
		if _, err := exec.LookPath("docker"); err != nil {
			dockerProbe.reason = "docker CLI not on PATH"
			return
		}
		ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
		defer cancel()
		if out, err := exec.CommandContext(ctx, "docker", "info", "--format", "{{.ServerVersion}}").CombinedOutput(); err != nil {
			dockerProbe.reason = "docker daemon unavailable: " + strings.TrimSpace(string(out))
			return
		}
		dockerProbe.ok = true
	})
	return dockerProbe.ok, dockerProbe.reason
}

func requireDocker(t *testing.T) {
	t.Helper()
	if ok, reason := dockerAvailable(); !ok {
		t.Skipf("docker leg skipped: %s", reason)
	}
}

func dockerConfig() backend.DockerConfig {
	return backend.DockerConfig{
		ContainerPrefix: testContainerPrefix,
		NetworkPrefix:   testNetworkPrefix,
		Labels:          map[string]string{"hermes.test": "chg-0336"},
		Trusted:         true,
	}
}

// dockerCLI runs the docker binary in tests; the argv is built here, never
// from request data.
func dockerCLI(t *testing.T, args ...string) []byte {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	out, err := exec.CommandContext(ctx, "docker", args...).CombinedOutput()
	if err != nil {
		t.Fatalf("docker %s: %v: %s", strings.Join(args, " "), err, strings.TrimSpace(string(out)))
	}
	return out
}

// dockerInspect maps `docker inspect` onto the suite's ContainerInfo for the
// container our backend named after the spec key.
func dockerInspect(t *testing.T, key string) backendtest.ContainerInfo {
	t.Helper()
	out := dockerCLI(t, "inspect", testContainerPrefix+key)
	var docs []struct {
		Args   []string
		Config struct {
			Cmd []string
			Env []string
		}
		Mounts []struct {
			Type        string
			Source      string
			Destination string
			RW          bool
		}
		NetworkSettings struct {
			Networks map[string]json.RawMessage
		}
	}
	if err := json.Unmarshal(out, &docs); err != nil || len(docs) != 1 {
		t.Fatalf("docker inspect decode: %v (%d docs)", err, len(docs))
	}
	doc := docs[0]
	info := backendtest.ContainerInfo{Args: doc.Args, Cmd: doc.Config.Cmd, Env: doc.Config.Env}
	for _, m := range doc.Mounts {
		if m.Type == "bind" {
			info.Binds = append(info.Binds, backendtest.BindMount{Source: m.Source, Target: m.Destination, ReadOnly: !m.RW})
		}
	}
	for name := range doc.NetworkSettings.Networks {
		// --network none still reports a "none" pseudo-entry: it is the
		// absence of a network, not an attachment.
		if name != "none" {
			info.Networks = append(info.Networks, name)
		}
	}
	return info
}

// spawnForeignContainer starts a workload the backend must not own: it
// carries the shared test label (so cleanup removes it) but none of the
// backend's tenant labels.
func spawnForeignContainer(t *testing.T) {
	t.Helper()
	name := testContainerPrefix + "foreign-" + rand.Text()[:12]
	dockerCLI(t, "run", "-d", "--name", name, "--label", testLabel, "alpine:latest", "sleep", "300")
}

// runscInstalled reports whether the daemon offers the gVisor runtime.
func runscInstalled(t *testing.T) bool {
	t.Helper()
	out := dockerCLI(t, "info", "--format", "{{json .Runtimes}}")
	var runtimes map[string]json.RawMessage
	if err := json.Unmarshal(out, &runtimes); err != nil {
		t.Fatalf("docker info runtimes: %v", err)
	}
	_, ok := runtimes["runsc"]
	return ok
}

// registerDockerCleanup removes only label-marked containers and networks in
// t.Cleanup. It is safe to register from every docker test: the filters are
// the same and a second run finds nothing.
func registerDockerCleanup(t *testing.T) {
	t.Helper()
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
		defer cancel()
		out, _ := exec.CommandContext(ctx, "docker", "ps", "-aq", "--filter", "label="+testLabel).Output()
		for _, id := range strings.Fields(string(out)) {
			_ = exec.CommandContext(ctx, "docker", "rm", "-f", id).Run()
		}
		out, _ = exec.CommandContext(ctx, "docker", "network", "ls", "-q", "--filter", "label="+testLabel).Output()
		for _, id := range strings.Fields(string(out)) {
			_ = exec.CommandContext(ctx, "docker", "network", "rm", id).Run()
		}
	})
}
