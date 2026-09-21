package devcheck

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
)

// dockerRunner executes one docker CLI call and returns its combined output.
type dockerRunner func(ctx context.Context, args ...string) ([]byte, error)

// dockerExec runs one docker CLI call with output streamed to the console.
type dockerExec func(ctx context.Context, args ...string) error

// DockerBuild builds the hub image through BuildKit even when the operator
// shell exports DOCKER_BUILDKIT=0/COMPOSE_DOCKER_CLI_BUILD=0. The classic
// builder materializes every stage as cache images and leaves the superseded
// tagged image fully duplicated, so each rebuild used to add the whole image
// size again.
func DockerBuild(ctx context.Context, image, target string) error {
	return dockerBuild(ctx, image, target, streamDocker)
}

func streamDocker(ctx context.Context, args ...string) error {
	cmd := exec.CommandContext(ctx, "docker", args...) // #nosec G204 -- fixed subcommands with validated refs.
	cmd.Env = append(os.Environ(), "DOCKER_BUILDKIT=1", "COMPOSE_DOCKER_CLI_BUILD=1")
	cmd.Stdout, cmd.Stderr = os.Stdout, os.Stderr
	return cmd.Run()
}

func dockerBuild(ctx context.Context, image, target string, run dockerExec) error {
	if image == "" || target == "" || strings.ContainsAny(image+target, " \t\r\n") || run == nil {
		return fmt.Errorf("image and target required")
	}
	return run(ctx, "build", "--target", target, "-t", image, "-f", "docker/Dockerfile", ".")
}

var composeImagePattern = regexp.MustCompile(`(?m)^\s*image:\s*hermes-hub:(\S+)\s*$`)

// DockerClean reclaims Docker state that accumulates between rebuilds:
// superseded hermes-hub tags not referenced by generated compose files,
// dangling images (including retagged hermes-artifact loads), orphaned
// artifact-build containers/networks/volumes and, with deep, the persistent
// BuildKit state volume. Running containers and in-use resources are kept;
// removals are best-effort because the daemon rejects busy objects.
func DockerClean(ctx context.Context, root string, deep bool) error {
	keep, err := activeImageTags(root)
	if err != nil {
		return err
	}
	return dockerClean(ctx, cliDocker, keep, deep)
}

func cliDocker(ctx context.Context, args ...string) ([]byte, error) {
	cmd := exec.CommandContext(ctx, "docker", args...) // #nosec G204 -- fixed read/prune subcommands only.
	return cmd.CombinedOutput()
}

// activeImageTags lists the hermes-hub tags still referenced by generated
// compose files; "test" is always kept because docker-check produces it.
func activeImageTags(root string) (map[string]bool, error) {
	keep := map[string]bool{"test": true}
	for _, pattern := range []string{"compose*.yaml", "compose*.yml"} {
		matches, _ := filepath.Glob(filepath.Join(root, "spaces", "*", pattern))
		for _, name := range matches {
			body, err := os.ReadFile(name) // #nosec G304 -- globbed inside the project tree.
			if err != nil {
				return nil, err
			}
			for _, match := range composeImagePattern.FindAllSubmatch(body, -1) {
				keep[string(match[1])] = true
			}
		}
	}
	return keep, nil
}

func dockerClean(ctx context.Context, run dockerRunner, keep map[string]bool, deep bool) error {
	if run == nil {
		return fmt.Errorf("docker runner required")
	}
	if body, err := run(ctx, "image", "ls", "hermes-hub", "--format", "{{.Repository}}:{{.Tag}}"); err != nil {
		return fmt.Errorf("image listing failed: %w", err)
	} else {
		for _, ref := range strings.Fields(string(body)) {
			tag := strings.TrimPrefix(ref, "hermes-hub:")
			if tag == ref || tag == "<none>" || keep[tag] {
				continue
			}
			if _, err := run(ctx, "rmi", ref); err == nil {
				fmt.Println("removed stale image:", ref)
			}
		}
	}
	// Stopped stack containers keep superseded (dangling) images alive; compose
	// recreates them from the current image on the next up. Only this project's
	// hermes-* containers are removed.
	if body, err := run(ctx, "image", "ls", "-f", "dangling=true", "-q"); err == nil {
		for _, id := range strings.Fields(string(body)) {
			list, err := run(ctx, "ps", "-a", "--filter", "ancestor="+id, "--format", "{{.Names}}\t{{.Status}}")
			if err != nil {
				continue
			}
			for _, line := range strings.Split(strings.TrimSpace(string(list)), "\n") {
				name, status, _ := strings.Cut(line, "\t")
				if !strings.HasPrefix(name, "hermes-") || strings.HasPrefix(status, "Up") {
					continue
				}
				if _, err := run(ctx, "rm", name); err == nil {
					fmt.Println("removed stale container:", name)
				}
			}
		}
	}
	if _, err := run(ctx, "image", "prune", "-f"); err != nil {
		return fmt.Errorf("image prune failed: %w", err)
	}
	// Best-effort: the classic builder reports no BuildKit cache.
	_, _ = run(ctx, "builder", "prune", "-f")
	if body, err := run(ctx, "ps", "-a", "--format", "{{.Names}}\t{{.Status}}"); err != nil {
		return fmt.Errorf("container listing failed: %w", err)
	} else {
		for _, line := range strings.Split(strings.TrimSpace(string(body)), "\n") {
			name, status, _ := strings.Cut(line, "\t")
			if !isBuildResource(name, "hermes-builder-", "hermes-build-proxy-", "hermes-build-seed-") || strings.HasPrefix(status, "Up") {
				continue
			}
			if _, err := run(ctx, "rm", name); err == nil {
				fmt.Println("removed orphan build container:", name)
			}
		}
	}
	if body, err := run(ctx, "network", "ls", "--format", "{{.Name}}", "--filter", "label=hermes-hub.role"); err != nil {
		return fmt.Errorf("network listing failed: %w", err)
	} else {
		for _, name := range strings.Fields(string(body)) {
			if !strings.HasPrefix(name, "hermes-build-net-") {
				continue
			}
			if _, err := run(ctx, "network", "rm", name); err == nil {
				fmt.Println("removed orphan build network:", name)
			}
		}
	}
	if body, err := run(ctx, "volume", "ls", "-q", "--filter", "label=hermes-hub.role"); err != nil {
		return fmt.Errorf("volume listing failed: %w", err)
	} else {
		for _, name := range strings.Fields(string(body)) {
			if !strings.HasPrefix(name, "hermes-build-") || (name == "hermes-build-state-shared" && !deep) {
				continue
			}
			if _, err := run(ctx, "volume", "rm", name); err == nil {
				fmt.Println("removed build volume:", name)
			}
		}
	}
	return nil
}

func isBuildResource(name string, prefixes ...string) bool {
	for _, prefix := range prefixes {
		if strings.HasPrefix(name, prefix) {
			return true
		}
	}
	return false
}
