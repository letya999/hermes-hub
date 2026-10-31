package main

import (
	"context"
	"flag"
	"fmt"
	"github.com/letya999/hermes-hub/internal/stack"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
)

type devServiceSpec struct {
	binary, source, base string
}

func devService(name string) (devServiceSpec, bool) {
	switch name {
	case "communication-hub", "credential-broker", "hub-media", "toolhub":
		source := map[string]string{"communication-hub": "./cmd/communication", "credential-broker": "./cmd/credential-broker", "hub-media": "./cmd/hub-media", "toolhub": "./cmd/toolhub"}[name]
		// SPEC-0082 R3: daemons run the lean core image; only Docker
		// consumers (toolhub, supervisor, workload-controller) need control.
		base := "hermes-hub:0.5.0-dev-core"
		if name == "toolhub" {
			base = "hermes-hub:0.5.0-dev-control"
		}
		return devServiceSpec{name, source, base}, true
	case "workload-controller", "supervisor":
		return devServiceSpec{"hubctl", "./cmd/hubctl", "hermes-hub:0.5.0-dev-control"}, true
	case "hermes-runtime":
		return devServiceSpec{"hub-runtime", "./cmd/runtime", "hermes-hub:0.5.0-dev"}, true
	case "model-relay":
		return devServiceSpec{"hub-runtime", "./cmd/runtime", "hermes-hub:0.5.0-dev"}, true
	case "egress-relay":
		return devServiceSpec{"hub-runtime", "./cmd/runtime", "hermes-hub:0.5.0-dev-core"}, true
	}
	return devServiceSpec{}, false
}

func runDevRebuild(ctx context.Context, args []string) (retErr error) {
	f := flag.NewFlagSet("dev-rebuild", flag.ContinueOnError)
	dir := f.String("dir", "", "dev space directory")
	root := f.String("root", ".", "source root")
	environment := f.String("env", "dev", "must be dev")
	if err := f.Parse(args); err != nil {
		return err
	}
	if *environment != "dev" || f.NArg() != 1 {
		return fmt.Errorf("dev-rebuild requires one service and --env dev")
	}
	service := f.Arg(0)
	spec, ok := devService(service)
	if !ok {
		return fmt.Errorf("unsupported dev service %q", service)
	}
	rootAbs, err := filepath.Abs(*root)
	if err != nil {
		return err
	}
	dirAbs, err := filepath.Abs(*dir)
	if err != nil {
		return err
	}
	settings, err := stack.ReadEnvironment(dirAbs, "dev")
	if err != nil {
		return err
	}
	services := stack.Compose(settings, rootAbs, dirAbs)["services"].(stack.M)
	if _, present := services[service]; !present && !(service == "hermes-runtime" && settings.ExecutionMode == "supervisor") {
		return fmt.Errorf("service %q is not in this dev stack", service)
	}
	if err := stack.RenderEnvironment(dirAbs, rootAbs, "dev"); err != nil {
		return err
	}
	compose := filepath.Join(dirAbs, "generated", "compose.dev.yaml")
	run := func(name string, env []string, workdir string, a ...string) error {
		cmd := exec.CommandContext(ctx, name, a...) // #nosec G204 -- fixed executable and service.
		cmd.Dir, cmd.Env = workdir, env
		cmd.Stdout, cmd.Stderr = os.Stdout, os.Stderr
		return cmd.Run()
	}
	docker := func(a ...string) error { return run("docker", supervisorComposeEnv(dirAbs), rootAbs, a...) }
	if err := docker("compose", "-f", compose, "config", "--quiet"); err != nil {
		return err
	}
	inspect := exec.CommandContext(ctx, "docker", "image", "inspect", "--format", "{{.Architecture}}", spec.base)
	inspect.Env = supervisorComposeEnv(dirAbs)
	out, err := inspect.Output()
	if err != nil {
		return err
	}
	arch := strings.TrimSpace(string(out))
	if arch != "amd64" && arch != "arm64" {
		return fmt.Errorf("unsupported architecture %q", arch)
	}
	buildDir, err := os.MkdirTemp("", "hermes-dev-rebuild-")
	if err != nil {
		return err
	}
	defer os.RemoveAll(buildDir)
	goDir := rootAbs
	if service == "credential-broker" {
		goDir = filepath.Join(rootAbs, "services", "credential-broker")
	}
	goEnv := append(os.Environ(), "GOOS=linux", "GOARCH="+arch, "CGO_ENABLED=0")
	goArgs := []string{"build", "-buildvcs=false", "-trimpath", "-ldflags=-s -w", "-o", filepath.Join(buildDir, "binary")}
	if err := run("go", goEnv, goDir, append(goArgs, spec.source)...); err != nil {
		return err
	}
	companions := filepath.Join(buildDir, "companions")
	if err := os.Mkdir(companions, 0700); err != nil {
		return err
	}
	if service == "hermes-runtime" {
		if err := run("go", goEnv, rootAbs, "build", "-buildvcs=false", "-trimpath", "-ldflags=-s -w", "-o", filepath.Join(companions, "hubctl"), "./cmd/hubctl"); err != nil {
			return err
		}
	}
	if err := stageDevCompanions(service, rootAbs, companions); err != nil {
		return err
	}
	defer func() {
		cleanupCtx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
		defer cancel()
		if err := pruneDanglingImages(cleanupCtx, dockerOutput); err != nil {
			if retErr == nil {
				retErr = fmt.Errorf("dev image cleanup: %w", err)
			} else {
				fmt.Fprintln(os.Stderr, "hubctl: dev image cleanup:", err)
			}
		}
	}()
	image := "hermes-hub-dev-" + settings.User + "-" + service + ":local"
	previous := exec.CommandContext(ctx, "docker", "image", "inspect", "--format", "{{.Id}}", image)
	previous.Env = supervisorComposeEnv(dirAbs)
	previousID, _ := previous.Output()
	if err := docker("build", "-f", filepath.Join(rootAbs, "docker", "Dockerfile.dev-overlay"), "--build-arg", "BASE="+spec.base, "--build-arg", "BINARY="+spec.binary, "-t", image, buildDir); err != nil {
		return err
	}
	if service == "hermes-runtime" && settings.ExecutionMode == "supervisor" {
		if err := writeAtomic(filepath.Join(dirAbs, "generated", "dev-runtime-image"), []byte(image+"\n")); err != nil {
			return err
		}
		return nil
	}
	if service == "communication-hub" && settings.ExecutionMode == "supervisor" {
		// Disposable script containers spawn --entrypoint communication-hub
		// from the supervisor's runtime image; pin the overlay so dev script
		// probes run the same code the rebuilt gateway does.
		if err := writeAtomic(filepath.Join(dirAbs, "generated", "dev-script-image"), []byte(image+"\n")); err != nil {
			return err
		}
	}
	override := filepath.Join(buildDir, "compose.override.yaml")
	if err := os.WriteFile(override, []byte("services:\n  "+service+":\n    image: "+image+"\n"), 0600); err != nil {
		return err
	}
	if err := docker("compose", "-f", compose, "-f", override, "up", "-d", "--wait", "--wait-timeout", "180", "--force-recreate", "--no-build", "--no-deps", service); err != nil {
		rollbackCtx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
		defer cancel()
		rollback := func(a ...string) error {
			cmd := exec.CommandContext(rollbackCtx, "docker", a...) // #nosec G204 -- fixed executable and service.
			cmd.Dir, cmd.Env, cmd.Stdout, cmd.Stderr = rootAbs, supervisorComposeEnv(dirAbs), os.Stdout, os.Stderr
			return cmd.Run()
		}
		rollbackFiles := []string{"-f", compose}
		if id := strings.TrimSpace(string(previousID)); id != "" {
			if restoreErr := rollback("tag", id, image); restoreErr != nil {
				return fmt.Errorf("start %s: %w; restore image: %v", service, err, restoreErr)
			}
			rollbackFiles = append(rollbackFiles, "-f", override)
		}
		rollbackArgs := append([]string{"compose"}, rollbackFiles...)
		rollbackArgs = append(rollbackArgs, "up", "-d", "--wait", "--wait-timeout", "180", "--force-recreate", "--no-build", "--no-deps", service)
		if restoreErr := rollback(rollbackArgs...); restoreErr != nil {
			return fmt.Errorf("start %s: %w; restore service: %v", service, err, restoreErr)
		}
		return fmt.Errorf("start %s: %w; previous service restored", service, err)
	}
	return nil
}

// stageDevCompanions copies non-Go runtime assets the overlay needs onto the
// service's /usr/local/bin. The base image predates them, so dev-rebuild must
// stage them just like the full Dockerfile does.
func stageDevCompanions(service, root, dest string) error {
	if service != "hermes-runtime" {
		return nil
	}
	src, err := os.ReadFile(filepath.Join(root, "docker", "hub-sidecar"))
	if err != nil {
		return err
	}
	return os.WriteFile(filepath.Join(dest, "hub-sidecar"), src, 0o755) // #nosec G703 -- dest is the private MkdirTemp build's companions directory; basename is fixed.
}
