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
// edge-<target> (latest build). docker-pull resolves the sha tag first and
// falls back to edge, then compares the embedded revision label with HEAD so
// a stale image is loudly reported instead of silently tested.
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

// DockerPull fetches the CI-built image for HEAD and retags it as image. When
// no <sha>-<target> tag exists yet (unpushed commit) it falls back to the
// moving edge-<target> tag and the revision check reports the mismatch.
func DockerPull(ctx context.Context, image, target string) error {
	repo, err := registryRepo(ctx, cliGit)
	if err != nil {
		return err
	}
	sha, _ := gitSHA(ctx, cliGit)
	return dockerPull(ctx, image, target, repo, sha, streamDocker, cliDocker)
}

func dockerPull(ctx context.Context, image, target, repo, sha string, run dockerExec, inspect dockerRunner) error {
	if !validRef(image, target, repo) {
		return fmt.Errorf("image, target and repo required")
	}
	ref := ""
	if sha != "" {
		ref = registryRef(repo, sha+"-"+target)
		if err := run(ctx, "pull", ref); err != nil {
			ref = ""
		}
	}
	if ref == "" {
		ref = registryRef(repo, "edge-"+target)
		if err := run(ctx, "pull", ref); err != nil {
			return fmt.Errorf("pull %s failed: %w", ref, err)
		}
	}
	if err := run(ctx, "tag", ref, image); err != nil {
		return fmt.Errorf("tag %s as %s failed: %w", ref, image, err)
	}
	out, err := inspect(ctx, "image", "inspect", "-f", `{{index .Config.Labels "org.opencontainers.image.revision"}}`, image)
	if err == nil {
		if rev := strings.TrimSpace(string(out)); validRef(rev, sha) && rev != sha {
			fmt.Printf("warning: pulled image is revision %s but HEAD is %s; image may not include the latest commits\n", rev, sha)
		}
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
	args := []string{"build", "--target", target,
		"-t", registryRef(repo, sha+"-"+target),
		"-t", registryRef(repo, "edge-"+target),
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
