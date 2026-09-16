package toolhub

import (
	"context"
	"fmt"
	"io"
	"os"
	"os/exec"
	"regexp"
	"strings"
)

var dockerImageIDPattern = regexp.MustCompile(`sha256:[a-f0-9]{64}`)

type artifactLoadRunner func(context.Context, io.Reader, ...string) ([]byte, error)

// LoadStoredOCIArtifact imports a verified quarantine archive into the local
// Docker image store. It never talks to a remote daemon and never writes a
// credential into Docker's config. The returned image ID is useful for a
// controller's pre-start inspect; the catalog keeps the OCI manifest digest.
func LoadStoredOCIArtifact(ctx context.Context, directory, digest, image string, maxBytes int64) (string, error) {
	if err := validateLocalImageName(image); err != nil {
		return "", err
	}
	artifact, _, err := OpenStoredOCIArtifact(ctx, directory, digest, maxBytes)
	if err != nil {
		return "", err
	}
	defer artifact.Close()
	return loadStoredOCIArtifact(ctx, artifact, image)
}

func loadStoredOCIArtifact(ctx context.Context, artifact io.Reader, image string) (string, error) {
	if artifact == nil {
		return "", ErrInvalid
	}
	config, err := os.MkdirTemp("", "hermes-docker-load-")
	if err != nil {
		return "", err
	}
	defer os.RemoveAll(config)
	run := func(ctx context.Context, input io.Reader, args ...string) ([]byte, error) {
		return runArtifactDocker(ctx, config, input, args...)
	}
	return loadStoredOCIArtifactWith(ctx, artifact, image, run)
}

func loadStoredOCIArtifactWith(ctx context.Context, artifact io.Reader, image string, run artifactLoadRunner) (string, error) {
	if artifact == nil || run == nil {
		return "", ErrInvalid
	}
	endpoint, err := run(ctx, nil, "context", "inspect", "--format", "{{(index .Endpoints \"docker\").Host}}")
	if err != nil || (!strings.HasPrefix(strings.TrimSpace(string(endpoint)), "npipe://") && !strings.HasPrefix(strings.TrimSpace(string(endpoint)), "unix://")) {
		return "", fmt.Errorf("%w: local Docker context required", ErrIsolation)
	}
	osType, err := run(ctx, nil, "info", "--format", "{{.OSType}}")
	if err != nil || strings.TrimSpace(string(osType)) != "linux" {
		return "", fmt.Errorf("%w: local Linux Docker runtime required", ErrIsolation)
	}
	output, err := run(ctx, artifact, "load")
	if err != nil {
		return "", err
	}
	match := string(dockerImageIDPattern.Find(output))
	if match == "" {
		return "", ErrIsolation
	}
	if _, err := run(ctx, nil, "tag", match, image); err != nil {
		return "", err
	}
	inspect, err := run(ctx, nil, "image", "inspect", image, "--format", "{{.Id}}")
	if err != nil || !dockerImageIDPattern.MatchString(strings.TrimSpace(string(inspect))) {
		return "", ErrIsolation
	}
	return strings.TrimSpace(string(inspect)), nil
}

func validateLocalImageName(image string) error {
	if image == "" || strings.ContainsAny(image, "@ \t\r\n") || strings.Contains(image, "..") || strings.HasPrefix(image, "/") || strings.HasSuffix(image, "/") {
		return ErrInvalid
	}
	for _, part := range strings.Split(image, "/") {
		if part == "" || !regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_.:-]{0,127}$`).MatchString(part) {
			return ErrInvalid
		}
	}
	return nil
}

func runArtifactDocker(ctx context.Context, dockerConfig string, input io.Reader, args ...string) ([]byte, error) {
	cmd := exec.CommandContext(ctx, "docker", args...) // #nosec G204 -- fixed Docker subcommands and validated image names.
	cmd.Stdin = input
	cmd.Env = []string{"PATH=" + os.Getenv("PATH"), "DOCKER_CONFIG=" + dockerConfig}
	output := &boundedOutput{limit: 1 << 20, exceeded: make(chan struct{})}
	cmd.Stdout, cmd.Stderr = output, output
	if err := cmd.Run(); err != nil {
		return []byte(output.String()), fmt.Errorf("docker %s failed: %w", args[0], err)
	}
	if output.overflowed() {
		return nil, ErrOutputLimit
	}
	return []byte(output.String()), nil
}
