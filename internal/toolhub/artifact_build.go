package toolhub

import (
	"archive/tar"
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/netip"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"time"
)

type RestrictedBuildConfig struct {
	SeccompPath       string
	ArtifactDirectory string
	MaxArtifactBytes  int64
}

type artifactDockerRun func(context.Context, io.Reader, ...string) ([]byte, error)

// restrictedBuildMu serializes restricted builds: with HUB_BUILD_CACHE=1 every
// buildkitd mounts the same state volume, and a second daemon dies instantly on
// buildkitd.lock. Even without the cache, parallel builds thrash the host.
var restrictedBuildMu sync.Mutex

// BuildRestrictedOCI builds one verified recipe in the pinned rootless BuildKit
// topology and publishes only a provenance/SBOM-bearing OCI archive to the
// quarantine CAS. The caller still has to preflight tools/effects and approve a
// trusted catalog definition.
func BuildRestrictedOCI(ctx context.Context, recipe ArtifactRecipe, contextBytes []byte, config RestrictedBuildConfig) (StoredOCIArtifact, error) {
	if err := recipe.VerifyContext(contextBytes); err != nil {
		return StoredOCIArtifact{}, err
	}
	if err := VerifyArtifactContextNoSecrets(contextBytes); err != nil {
		return StoredOCIArtifact{}, err
	}
	if !filepath.IsAbs(config.SeccompPath) || !filepath.IsAbs(config.ArtifactDirectory) || config.MaxArtifactBytes < 1 || config.MaxArtifactBytes > 8<<30 {
		return StoredOCIArtifact{}, fmt.Errorf("%w: absolute builder paths and artifact bound required", ErrInvalid)
	}
	if err := safeStorePath(filepath.Join(config.ArtifactDirectory, "artifact-check")); err != nil {
		return StoredOCIArtifact{}, err
	}
	dockerConfig, err := os.MkdirTemp("", "hermes-docker-config-")
	if err != nil {
		return StoredOCIArtifact{}, err
	}
	defer os.RemoveAll(dockerConfig)
	run := func(ctx context.Context, input io.Reader, args ...string) ([]byte, error) {
		cmd := exec.CommandContext(ctx, "docker", args...) // #nosec G204 -- arguments are generated exclusively by the fixed builder plan.
		cmd.Stdin = input
		cmd.Env = []string{"PATH=" + os.Getenv("PATH"), "DOCKER_CONFIG=" + dockerConfig}
		output := &boundedOutput{limit: 1 << 20, exceeded: make(chan struct{})}
		cmd.Stdout, cmd.Stderr = output, output
		err := cmd.Run()
		if output.overflowed() {
			return nil, ErrOutputLimit
		}
		if err != nil {
			return []byte(output.String()), fmt.Errorf("docker %s failed: %w: %s", args[0], err, output.String())
		}
		return []byte(output.String()), nil
	}
	return buildRestrictedOCI(ctx, recipe, contextBytes, config, run)
}

func buildRestrictedOCI(ctx context.Context, recipe ArtifactRecipe, contextBytes []byte, config RestrictedBuildConfig, run artifactDockerRun) (StoredOCIArtifact, error) {
	if run == nil {
		return StoredOCIArtifact{}, ErrInvalid
	}
	restrictedBuildMu.Lock()
	defer restrictedBuildMu.Unlock()
	ctx, cancel := context.WithTimeout(ctx, 25*time.Minute)
	defer cancel()
	reapStaleBuildResources(ctx, run)
	host, err := run(ctx, nil, "context", "inspect", "--format", "{{(index .Endpoints \"docker\").Host}}")
	if err != nil || (!strings.HasPrefix(strings.TrimSpace(string(host)), "npipe://") && !strings.HasPrefix(strings.TrimSpace(string(host)), "unix://")) {
		return StoredOCIArtifact{}, fmt.Errorf("%w: local Docker context required", ErrIsolation)
	}
	osType, err := run(ctx, nil, "info", "--format", "{{.OSType}}")
	if err != nil || strings.TrimSpace(string(osType)) != "linux" {
		return StoredOCIArtifact{}, fmt.Errorf("%w: local Linux Docker runtime required", ErrIsolation)
	}
	for _, image := range []string{restrictedBuildKitImage, restrictedBuildProxyImage} {
		if _, err := run(ctx, nil, "image", "inspect", image); err != nil {
			return StoredOCIArtifact{}, fmt.Errorf("%w: pinned builder image unavailable", ErrIsolation)
		}
	}
	var nonce [8]byte
	if _, err := rand.Read(nonce[:]); err != nil {
		return StoredOCIArtifact{}, err
	}
	suffix := hex.EncodeToString(nonce[:])
	builder, proxy, seed := "hermes-builder-"+suffix, "hermes-build-proxy-"+suffix, "hermes-build-seed-"+suffix
	network := "hermes-build-net-" + suffix
	stateVolume := "hermes-build-state-" + suffix
	persistentState := buildCacheEnabled()
	if persistentState {
		stateVolume = "hermes-build-state-shared"
	}
	volumes := []string{stateVolume, "hermes-build-proxy-config-" + suffix, "hermes-build-context-" + suffix, "hermes-build-output-" + suffix}
	log.Printf("toolhub build start: dockerfile=%s context=%dB builder=%s", recipe.Dockerfile, len(contextBytes), builder)
	defer func() {
		cleanup, stop := context.WithTimeout(context.Background(), 20*time.Second)
		defer stop()
		for _, name := range []string{builder, proxy, seed} {
			if _, err := run(cleanup, nil, "rm", "-f", name); err != nil {
				log.Printf("toolhub build cleanup: remove container %s: %v", name, err)
			}
		}
		if _, err := run(cleanup, nil, "network", "rm", network); err != nil {
			log.Printf("toolhub build cleanup: remove network %s: %v", network, err)
		}
		for _, volume := range volumes {
			if persistentState && volume == stateVolume {
				continue
			}
			if _, err := run(cleanup, nil, "volume", "rm", volume); err != nil {
				log.Printf("toolhub build cleanup: remove volume %s: %v", volume, err)
			}
		}
	}()
	if _, err := run(ctx, nil, "network", "create", "--internal", "--label", "hermes-hub.role=artifact-build", network); err != nil {
		return StoredOCIArtifact{}, err
	}
	roles := []string{"state", "proxy-config", "context", "output"}
	for i, volume := range volumes {
		if _, err := run(ctx, nil, "volume", "create", "--label", "hermes-hub.role=artifact-build-"+roles[i], volume); err != nil {
			return StoredOCIArtifact{}, err
		}
	}
	seedArgs := []string{"create", "--name", seed, "--label", "hermes-hub.role=artifact-build-seed", "--user", "0:0", "--network", "none", "--read-only", "--cap-drop", "ALL", "--security-opt", "no-new-privileges", "--cpus", "0.1", "--memory", "32m", "--memory-swap", "32m", "--pids-limit", "16",
		"--mount", "type=volume,source=" + volumes[1] + ",target=/proxy-config", "--mount", "type=volume,source=" + volumes[2] + ",target=/context", "--mount", "type=volume,source=" + volumes[3] + ",target=/output",
		"--entrypoint", "/bin/chmod", restrictedBuildProxyImage, "0777", "/output"}
	if _, err := run(ctx, nil, seedArgs...); err != nil {
		return StoredOCIArtifact{}, err
	}
	proxyTar, err := artifactBuildTar("squid.conf", []byte(restrictedBuildProxyConfig()))
	if err != nil {
		return StoredOCIArtifact{}, err
	}
	if _, err := run(ctx, bytes.NewReader(proxyTar), "cp", "-", seed+":/proxy-config/."); err != nil {
		return StoredOCIArtifact{}, err
	}
	if _, err := run(ctx, bytes.NewReader(contextBytes), "cp", "-", seed+":/context/."); err != nil {
		return StoredOCIArtifact{}, err
	}
	if _, err := run(ctx, nil, "start", "--attach", seed); err != nil {
		return StoredOCIArtifact{}, err
	}
	proxyArgs, err := restrictedBuildProxyCreateArgs(proxy, network, volumes[1])
	if err != nil {
		return StoredOCIArtifact{}, err
	}
	if _, err := run(ctx, nil, proxyArgs...); err != nil {
		return StoredOCIArtifact{}, err
	}
	if _, err := run(ctx, nil, "network", "connect", "bridge", proxy); err != nil {
		return StoredOCIArtifact{}, err
	}
	builderArgs, err := restrictedBuildKitCreateArgs(builder, volumes[0], config.SeccompPath, network, proxy, volumes[2], volumes[3])
	if err != nil {
		return StoredOCIArtifact{}, err
	}
	if _, err := run(ctx, nil, builderArgs...); err != nil {
		return StoredOCIArtifact{}, err
	}
	if err := verifyRestrictedBuildContainers(ctx, run, network, builder, proxy); err != nil {
		return StoredOCIArtifact{}, err
	}
	if _, err := run(ctx, nil, "start", proxy); err != nil {
		return StoredOCIArtifact{}, err
	}
	if _, err := run(ctx, nil, "start", builder); err != nil {
		return StoredOCIArtifact{}, err
	}
	ready, stopReady := context.WithTimeout(ctx, 30*time.Second)
	defer stopReady()
	if err := waitLocalWorkload(ready, func() error {
		_, err := run(ready, nil, "exec", builder, "buildctl", "--addr", "unix:///run/user/1000/buildkit/buildkitd.sock", "debug", "workers")
		return err
	}); err != nil {
		return StoredOCIArtifact{}, err
	}
	egressReady, stopEgress := context.WithTimeout(ctx, 15*time.Second)
	defer stopEgress()
	if err := waitLocalWorkload(egressReady, func() error { return verifyRestrictedBuildEgress(egressReady, run, builder, proxy) }); err != nil {
		return StoredOCIArtifact{}, err
	}
	// BuildKit's rootless executor may use an isolated resolver that cannot
	// resolve Docker container names even though the builder itself can. Pin the
	// proxy endpoint to its address on the owned internal network instead of
	// falling back to a direct hostname or host proxy.
	proxyInfo, err := run(ctx, nil, "inspect", "--format", "{{(index .NetworkSettings.Networks \""+network+"\").IPAddress}}", proxy)
	if err != nil {
		return StoredOCIArtifact{}, fmt.Errorf("%w: build proxy address unavailable", ErrIsolation)
	}
	proxyIP := strings.TrimSpace(string(proxyInfo))
	parsedProxyIP, parseErr := netip.ParseAddr(proxyIP)
	if parseErr != nil || !parsedProxyIP.Is4() {
		return StoredOCIArtifact{}, fmt.Errorf("%w: invalid build proxy address", ErrIsolation)
	}
	proxyURL := "http://" + proxyIP + ":3128"
	buildArgs := []string{"exec", builder, "buildctl", "--addr", "unix:///run/user/1000/buildkit/buildkitd.sock", "build", "--progress", "plain", "--frontend", "dockerfile.v0", "--local", "context=/run/hermes-context", "--local", "dockerfile=/run/hermes-context",
		"--opt", "filename=" + recipe.Dockerfile,
		"--opt", "build-arg:HTTP_PROXY=" + proxyURL, "--opt", "build-arg:HTTPS_PROXY=" + proxyURL, "--opt", "build-arg:NO_PROXY=",
		"--opt", "attest:provenance=mode=max", "--opt", "attest:sbom=generator=" + restrictedSBOMScannerImage,
		"--output", "type=oci,dest=/run/hermes-output/artifact.tar"}
	if _, err := run(ctx, nil, buildArgs...); err != nil {
		return StoredOCIArtifact{}, err
	}
	temporary, err := os.CreateTemp("", "hermes-artifact-*.oci.tar")
	if err != nil {
		return StoredOCIArtifact{}, err
	}
	path := temporary.Name()
	if err := temporary.Close(); err != nil {
		return StoredOCIArtifact{}, err
	}
	defer os.Remove(path)
	if _, err := run(ctx, nil, "cp", builder+":/run/hermes-output/artifact.tar", path); err != nil {
		return StoredOCIArtifact{}, err
	}
	artifact, err := os.Open(path)
	if err != nil {
		return StoredOCIArtifact{}, err
	}
	defer artifact.Close()
	return PersistOCIArtifact(ctx, config.ArtifactDirectory, artifact, config.MaxArtifactBytes)
}

// buildCacheEnabled opts into a persistent rootless BuildKit state volume and
// package-manager cache mounts for local development iteration. Production
// keeps one-shot builders: no data crosses between builds.
func buildCacheEnabled() bool {
	return os.Getenv("HUB_BUILD_CACHE") == "1"
}

func verifyRestrictedBuildEgress(ctx context.Context, run artifactDockerRun, builder, proxy string) error {
	connect := func(host string) string {
		request := "CONNECT " + host + ":443 HTTP/1.1\r\nHost: " + host + ":443\r\n\r\n"
		body, _ := run(ctx, strings.NewReader(request), "exec", "-i", builder, "nc", "-w", "2", proxy, "3128")
		return string(body)
	}
	if !strings.Contains(connect("registry.npmjs.org"), " 200 ") || !strings.Contains(connect("example.com"), " 403 ") {
		return fmt.Errorf("%w: builder egress policy probe failed", ErrIsolation)
	}
	return nil
}

func artifactBuildTar(name string, data []byte) ([]byte, error) {
	var output bytes.Buffer
	w := tar.NewWriter(&output)
	if err := w.WriteHeader(&tar.Header{Name: name, Typeflag: tar.TypeReg, Mode: 0644, Size: int64(len(data))}); err != nil {
		return nil, err
	}
	if _, err := w.Write(data); err != nil {
		return nil, err
	}
	if err := w.Close(); err != nil {
		return nil, err
	}
	return output.Bytes(), nil
}

func verifyRestrictedBuildContainers(ctx context.Context, run artifactDockerRun, network string, names ...string) error {
	body, err := run(ctx, nil, append([]string{"inspect"}, names...)...)
	var containers []struct {
		Config     struct{ User string }
		HostConfig struct {
			ReadonlyRootfs bool
			Privileged     bool
			PidMode        string
			NanoCpus       int64
			Memory         int64
			PidsLimit      int64
			NetworkMode    string
			Binds          []string
			CapAdd         []string
			CapDrop        []string
			SecurityOpt    []string
			Mounts         []struct {
				Type string
			}
		}
	}
	if err != nil || json.Unmarshal(body, &containers) != nil || len(containers) != len(names) {
		return fmt.Errorf("%w: builder pre-start inspection unavailable", ErrIsolation)
	}
	for index, container := range containers {
		builderCaps := index == 0 && slices.Equal(container.HostConfig.CapAdd, []string{"CAP_SETGID", "CAP_SETUID"})
		proxyCaps := index != 0 && len(container.HostConfig.CapAdd) == 2 && slices.Contains(container.HostConfig.CapAdd, "CAP_SETGID") && slices.Contains(container.HostConfig.CapAdd, "CAP_SETUID")
		// Rootless BuildKit needs SETUID/SETGID during its own bootstrap; the
		// untrusted MCP/runtime profile still requires no-new-privileges.
		nnp := index == 0 || slices.Contains(container.HostConfig.SecurityOpt, "no-new-privileges=true") || slices.Contains(container.HostConfig.SecurityOpt, "no-new-privileges")
		seccomp := slices.ContainsFunc(container.HostConfig.SecurityOpt, func(value string) bool { return strings.HasPrefix(value, "seccomp=") })
		if container.HostConfig.Privileged || !container.HostConfig.ReadonlyRootfs || container.Config.User == "" || container.Config.User == "0" || container.Config.User == "0:0" || container.HostConfig.NanoCpus == 0 || container.HostConfig.Memory == 0 || container.HostConfig.PidsLimit == 0 || container.HostConfig.NetworkMode != network || container.HostConfig.PidMode != "" || len(container.HostConfig.Binds) != 0 || len(container.HostConfig.CapDrop) != 1 || container.HostConfig.CapDrop[0] != "ALL" || !nnp || (index == 0 && !seccomp) || (!builderCaps && !proxyCaps) || len(container.HostConfig.Mounts) == 0 {
			return fmt.Errorf("%w: unenforced builder profile %d", ErrIsolation, index)
		}
		for _, mount := range container.HostConfig.Mounts {
			if mount.Type == "bind" {
				return fmt.Errorf("%w: builder host mount", ErrIsolation)
			}
		}
	}
	return nil
}

// reapStaleBuildResources removes leftover containers, networks and volumes
// from earlier restricted builds. A crashed ToolHub can leave a builder whose
// buildkitd still holds the shared-state lock, failing every later build.
// Callers must hold restrictedBuildMu. Best effort: failures are logged, the
// build itself verifies its own topology afterwards.
func reapStaleBuildResources(ctx context.Context, run artifactDockerRun) {
	out, err := run(ctx, nil, "ps", "-aq", "--filter", "label=hermes-hub.role=artifact-builder", "--filter", "label=hermes-hub.role=artifact-build-egress", "--filter", "label=hermes-hub.role=artifact-build-seed")
	if err == nil {
		for _, id := range strings.Fields(string(out)) {
			if _, err := run(ctx, nil, "rm", "-f", id); err != nil {
				log.Printf("toolhub stale build reap: remove container %s: %v", id, err)
			}
		}
	}
	out, err = run(ctx, nil, "network", "ls", "-q", "--filter", "label=hermes-hub.role=artifact-build")
	if err == nil {
		for _, id := range strings.Fields(string(out)) {
			if _, err := run(ctx, nil, "network", "rm", id); err != nil {
				log.Printf("toolhub stale build reap: remove network %s: %v", id, err)
			}
		}
	}
	out, err = run(ctx, nil, "volume", "ls", "-q", "--filter", "name=hermes-build-")
	if err == nil {
		for _, id := range strings.Fields(string(out)) {
			if id == "hermes-build-state-shared" {
				continue
			}
			if _, err := run(ctx, nil, "volume", "rm", id); err != nil {
				log.Printf("toolhub stale build reap: remove volume %s: %v", id, err)
			}
		}
	}
}
