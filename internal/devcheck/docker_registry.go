package devcheck

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"regexp"
	"strings"
)

// GHCR keeps CI-built hub images so local checks and the containers job pull a
// ready image instead of rebuilding the heavy base layers. Every CI build
// pushes an immutable <sha>-<target> tag (exact-run traceability) and moves
// edge-<target> (latest build). Verification pulls only the exact SHA: a
// missing image must not spend time testing an unrelated edge build.
const registryHost = "ghcr.io"

var gitURLPattern = regexp.MustCompile(`github\.com[:/]([A-Za-z0-9_.-]+)/([A-Za-z0-9_.-]+?)(?:\.git)?/?\s*$`)

func cliGit(ctx context.Context, args ...string) ([]byte, error) {
	cmd := exec.CommandContext(ctx, "git", args...) // #nosec G204 -- fixed read-only subcommands.
	return cmd.CombinedOutput()
}

// registryRepo resolves owner/repo for the GHCR namespace. HUB_REGISTRY_REPO
// overrides for tests and mirrors; GITHUB_REPOSITORY is set by the runner; the
// fallback parses the origin remote without printing it anywhere.
func registryRepo(ctx context.Context, git func(context.Context, ...string) ([]byte, error)) (string, error) {
	if repo := strings.TrimSpace(os.Getenv("HUB_REGISTRY_REPO")); repo != "" {
		return strings.ToLower(repo), nil
	}
	if repo := strings.TrimSpace(os.Getenv("GITHUB_REPOSITORY")); repo != "" {
		return strings.ToLower(repo), nil
	}
	out, err := git(ctx, "remote", "get-url", "origin")
	if err != nil {
		return "", fmt.Errorf("origin remote lookup failed: %w", err)
	}
	match := gitURLPattern.FindStringSubmatch(strings.TrimSpace(string(out)))
	if match == nil {
		return "", fmt.Errorf("origin remote is not a github.com remote; set HUB_REGISTRY_REPO")
	}
	return strings.ToLower(match[1] + "/" + match[2]), nil
}

func gitSHA(ctx context.Context, git func(context.Context, ...string) ([]byte, error)) (string, error) {
	out, err := git(ctx, "rev-parse", "HEAD")
	if err != nil {
		return "", fmt.Errorf("HEAD lookup failed: %w", err)
	}
	return strings.TrimSpace(string(out)), nil
}

func registryRef(repo, tag string) string {
	return registryHost + "/" + repo + "/hermes-hub:" + tag
}

func validRef(parts ...string) bool {
	for _, part := range parts {
		if part == "" || strings.ContainsAny(part, " \t\r\n") {
			return false
		}
	}
	return true
}

// DockerPull fetches the CI-built image for HEAD and retags it only after
// checking the embedded revision. Unpublished commits cannot pass this gate.
func DockerPull(ctx context.Context, image, target string) error {
	repo, err := registryRepo(ctx, cliGit)
	if err != nil {
		return err
	}
	sha, err := gitSHA(ctx, cliGit)
	if err != nil {
		return err
	}
	return dockerPull(ctx, image, target, repo, sha, streamDocker, cliDocker)
}

func dockerPull(ctx context.Context, image, target, repo, sha string, run dockerExec, inspect dockerRunner) error {
	if !validRef(image, target, repo, sha) {
		return fmt.Errorf("image, target, repo and source revision required")
	}
	ref := registryRef(repo, sha+"-"+target)
	if err := run(ctx, "pull", ref); err != nil {
		return fmt.Errorf("CI image for %s unavailable; complete the image workflow for this commit: %w", sha, err)
	}
	out, err := inspect(ctx, "image", "inspect", "-f", `{{index .Config.Labels "org.opencontainers.image.revision"}}`, ref)
	if err != nil {
		return fmt.Errorf("inspect source revision: %w", err)
	}
	if strings.TrimSpace(string(out)) != sha {
		return fmt.Errorf("CI image source revision does not match HEAD %s", sha)
	}
	if err := run(ctx, "tag", ref, image); err != nil {
		return fmt.Errorf("tag %s as %s failed: %w", ref, image, err)
	}
	return nil
}

// DockerPublish builds one target and pushes both the immutable <sha>-<target>
// tag and the moving edge-<target> tag to GHCR. It runs only in CI: the
// containers job and local docker-pull consume the pushed refs.
func DockerPublish(ctx context.Context, target string) error {
	repo, err := registryRepo(ctx, cliGit)
	if err != nil {
		return err
	}
	sha, err := gitSHA(ctx, cliGit)
	if err != nil {
		return err
	}
	return dockerPublish(ctx, target, repo, sha, streamDocker)
}

func dockerPublish(ctx context.Context, target, repo, sha string, run dockerExec) error {
	if run == nil || !validRef(target, repo, sha) {
		return fmt.Errorf("target, repo and sha required")
	}
	return dockerPublishRefs(ctx, target, sha, registryRef(repo, sha+"-"+target), registryRef(repo, "edge-"+target), run)
}

var publishImagePattern = regexp.MustCompile(`^[a-z0-9][a-z0-9.:-]*/[a-z0-9][a-z0-9._/-]*$`)
var publishEnvPattern = regexp.MustCompile(`^[a-z0-9][a-z0-9-]{0,31}$`)
var publishSHAPattern = regexp.MustCompile(`^[a-f0-9]{40}$`)

// DockerPublishTo publishes the portable image set without assuming a registry.
// Authentication belongs to the runner's isolated Docker config, never argv.
func DockerPublishTo(ctx context.Context, image, target, environment string) error {
	sha, err := gitSHA(ctx, cliGit)
	if err != nil {
		return err
	}
	return dockerPublishTo(ctx, image, target, environment, sha, streamDocker)
}

func dockerPublishTo(ctx context.Context, image, target, environment, sha string, run dockerExec) error {
	if run == nil || !publishImagePattern.MatchString(image) || !publishEnvPattern.MatchString(environment) || !publishSHAPattern.MatchString(sha) {
		return fmt.Errorf("qualified registry image, environment slug and full source SHA required")
	}
	switch target {
	case "prod-control", "prod", "cli-tools", "cell-proxy", "credential-broker":
	default:
		return fmt.Errorf("unsupported portable image target")
	}
	return dockerPublishRefs(ctx, target, sha, image+":"+sha, image+":edge-"+environment, run)
}

func dockerPublishRefs(ctx context.Context, target, sha, immutable, edge string, run dockerExec) error {
	args := []string{"build", "--target", target,
		"-t", immutable,
		"-t", edge,
		"--push", "--build-arg", "GIT_SHA=" + sha}
	args = append(args, cacheArgs()...)
	return run(ctx, append(args, "-f", targetDockerfile(target), ".")...)
}

// targetDockerfile maps a build target to its Dockerfile. The hub families
// (dev/prod plus their -control variants) share docker/Dockerfile; the
// standalone media sidecars own dedicated files built through the same
// publish/pull plumbing.
func targetDockerfile(target string) string {
	switch target {
	case "stt", "tts":
		return "docker/Dockerfile." + target
	}
	return "docker/Dockerfile"
}
