package devcheck

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

func TestDockerCleanRemovesOnlyUnreferencedState(t *testing.T) {
	root := t.TempDir()
	space := filepath.Join(root, "spaces", "local")
	if err := os.MkdirAll(space, 0755); err != nil {
		t.Fatal(err)
	}
	compose := "services:\n  runtime:\n    image: hermes-hub:0.3.0-dev\n  toolhub:\n    image: hermes-hub:0.3.0-dev\n"
	if err := os.WriteFile(filepath.Join(space, "compose.dev.yaml"), []byte(compose), 0600); err != nil {
		t.Fatal(err)
	}
	keep, err := activeImageTags(root)
	if err != nil {
		t.Fatal(err)
	}
	var calls []string
	run := func(ctx context.Context, args ...string) ([]byte, error) {
		calls = append(calls, strings.Join(args, " "))
		switch args[0] {
		case "image":
			if len(args) > 1 && args[1] == "ls" {
				for _, arg := range args {
					if arg == "dangling=true" {
						return []byte("ae15d8c8a0a5\n"), nil
					}
				}
				return []byte("hermes-hub:0.3.0-dev\nhermes-hub:0.2.0-dev\nhermes-hub:test\nhermes-hub:<none>\nunprefixed\n"), nil
			}
		case "ps":
			for _, arg := range args {
				if strings.HasPrefix(arg, "ancestor=") {
					return []byte("hermes-hub-local-dev-runtime-1\tExited (0) 5 hours ago\nai-stp-api-1\tExited (0) 5 hours ago\n\n"), nil
				}
			}
			return []byte("hermes-builder-aa\tExited (0) 2 hours ago\nhermes-build-proxy-bb\tUp 3 minutes\nhermes-build-seed-cc\tCreated\nother-app\tExited (0) 1 day ago\n"), nil
		case "network":
			return []byte("hermes-build-net-dd\nbridge\n"), nil
		case "volume":
			return []byte("hermes-build-context-ee\nhermes-build-output-ee\nhermes-build-state-shared\nhermes-build-state-ff\n"), nil
		}
		return []byte(""), nil
	}
	if err := dockerClean(context.Background(), run, keep, false); err != nil {
		t.Fatal(err)
	}
	joined := strings.Join(calls, "\n")
	for _, want := range []string{
		"rmi hermes-hub:0.2.0-dev",
		"rm hermes-hub-local-dev-runtime-1",
		"image prune -f",
		"rm hermes-builder-aa",
		"rm hermes-build-seed-cc",
		"network rm hermes-build-net-dd",
		"volume rm hermes-build-context-ee",
		"volume rm hermes-build-output-ee",
		"volume rm hermes-build-state-ff",
	} {
		if !strings.Contains(joined, want) {
			t.Fatalf("missing %q in\n%s", want, joined)
		}
	}
	for _, unwanted := range []string{
		"rmi hermes-hub:0.3.0-dev",
		"rmi hermes-hub:test",
		"rm hermes-build-proxy-bb",
		"rm other-app",
		"rm ai-stp-api-1",
		"network rm bridge",
		"volume rm hermes-build-state-shared",
	} {
		if strings.Contains(joined, unwanted) {
			t.Fatalf("unexpected %q in\n%s", unwanted, joined)
		}
	}
}

func TestDockerCleanDeepRemovesSharedBuildState(t *testing.T) {
	var calls []string
	run := func(ctx context.Context, args ...string) ([]byte, error) {
		calls = append(calls, strings.Join(args, " "))
		if args[0] == "volume" {
			return []byte("hermes-build-state-shared\n"), nil
		}
		return []byte(""), nil
	}
	if err := dockerClean(context.Background(), run, map[string]bool{"test": true}, true); err != nil {
		t.Fatal(err)
	}
	if !slices.Contains(calls, "volume rm hermes-build-state-shared") {
		t.Fatalf("deep clean kept shared state: %v", calls)
	}
}

func TestDockerCleanPropagatesListingErrors(t *testing.T) {
	failing := func(ctx context.Context, args ...string) ([]byte, error) {
		return nil, fmt.Errorf("daemon down")
	}
	if err := dockerClean(context.Background(), failing, map[string]bool{}, false); err == nil {
		t.Fatal("listing failure accepted")
	}
	if err := dockerClean(context.Background(), nil, map[string]bool{}, false); err == nil {
		t.Fatal("nil runner accepted")
	}
	for _, command := range []string{"image", "ps", "network", "volume"} {
		failingOn := func(ctx context.Context, args ...string) ([]byte, error) {
			if args[0] == command {
				return nil, fmt.Errorf("daemon down")
			}
			return []byte(""), nil
		}
		if err := dockerClean(context.Background(), failingOn, map[string]bool{}, false); err == nil {
			t.Fatalf("%s listing failure accepted", command)
		}
	}
	runnerErr := func(ctx context.Context, args ...string) ([]byte, error) {
		switch args[0] {
		case "image":
			if slices.Contains(args, "prune") {
				return nil, fmt.Errorf("prune failed")
			}
			return []byte("hermes-hub:0.1.0-dev\n"), nil
		case "rmi":
			return nil, fmt.Errorf("image in use")
		}
		return []byte(""), nil
	}
	if err := dockerClean(context.Background(), runnerErr, map[string]bool{"test": true}, false); err == nil {
		t.Fatal("image prune failure accepted")
	}
	ancestorFails := func(ctx context.Context, args ...string) ([]byte, error) {
		switch args[0] {
		case "image":
			if slices.Contains(args, "dangling=true") {
				return []byte("ae15d8c8a0a5\n"), nil
			}
			return []byte(""), nil
		case "ps":
			if slices.ContainsFunc(args, func(arg string) bool { return strings.HasPrefix(arg, "ancestor=") }) {
				return nil, fmt.Errorf("ps failed")
			}
			return []byte(""), nil
		}
		return []byte(""), nil
	}
	if err := dockerClean(context.Background(), ancestorFails, map[string]bool{"test": true}, false); err != nil {
		t.Fatal(err)
	}
}

func TestDockerCleanPropagatesTagScanError(t *testing.T) {
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, "spaces", "x", "compose.dev.yaml"), 0755); err != nil {
		t.Fatal(err)
	}
	if err := DockerClean(context.Background(), root, false); err == nil {
		t.Fatal("unreadable compose layout accepted")
	}
	if _, err := exec.LookPath("docker"); err != nil {
		t.Skip("docker CLI unavailable")
	}
	if err := DockerClean(context.Background(), t.TempDir(), false); err != nil {
		t.Fatal(err)
	}
}

func TestDockerExecWrappers(t *testing.T) {
	if _, err := exec.LookPath("docker"); err != nil {
		t.Skip("docker CLI unavailable")
	}
	if _, err := cliDocker(context.Background(), "version"); err != nil {
		t.Fatal(err)
	}
	if err := streamDocker(context.Background(), "version"); err != nil {
		t.Fatal(err)
	}
}

func TestDockerBuildRejectsBlankRefs(t *testing.T) {
	for _, pair := range [][2]string{{"", "prod"}, {"image", ""}, {"bad image", "prod"}, {"image", "bad\ttarget"}} {
		if err := DockerBuild(context.Background(), pair[0], pair[1]); err == nil {
			t.Fatalf("accepted %#v", pair)
		}
	}
	var got []string
	run := func(ctx context.Context, args ...string) error {
		got = append(got, args...)
		return nil
	}
	if err := dockerBuild(context.Background(), "hermes-hub:test", "prod", run); err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(got, []string{"build", "--target", "prod", "-t", "hermes-hub:test", "-f", "docker/Dockerfile", "."}) {
		t.Fatalf("unexpected build args: %v", got)
	}
	if err := dockerBuild(context.Background(), "hermes-hub:test", "prod", func(ctx context.Context, args ...string) error {
		return fmt.Errorf("build failed")
	}); err == nil {
		t.Fatal("build failure accepted")
	}
	if err := dockerBuild(context.Background(), "hermes-hub:test", "prod", nil); err == nil {
		t.Fatal("nil runner accepted")
	}
}

func TestActiveImageTagsKeepsComposeReferences(t *testing.T) {
	root := t.TempDir()
	space := filepath.Join(root, "spaces", "alice")
	if err := os.MkdirAll(space, 0755); err != nil {
		t.Fatal(err)
	}
	body := "services:\n  a:\n    image: hermes-hub:9.9.9-prod\n  b:\n    image: other:1\n"
	if err := os.WriteFile(filepath.Join(space, "compose.prod.yml"), []byte(body), 0600); err != nil {
		t.Fatal(err)
	}
	keep, err := activeImageTags(root)
	if err != nil {
		t.Fatal(err)
	}
	if !keep["9.9.9-prod"] || !keep["test"] || keep["1"] {
		t.Fatalf("unexpected keep set: %v", keep)
	}
	broken := filepath.Join(root, "spaces", "broken")
	if err := os.MkdirAll(filepath.Join(broken, "compose.dev.yaml"), 0755); err != nil {
		t.Fatal(err)
	}
	if _, err := activeImageTags(root); err == nil {
		t.Fatal("unreadable compose file accepted")
	}
}
