package devcheck

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"
	"testing"
)

func TestRegistryRepoResolution(t *testing.T) {
	t.Setenv("HUB_REGISTRY_REPO", "")
	t.Setenv("GITHUB_REPOSITORY", "")
	for _, remote := range []string{
		"git@github.com:Owner/Hermes-Hub.git",
		"https://github.com/owner/hermes-hub.git",
		"https://token@github.com/OWNER/hermes-hub",
		"https://github.com/owner/hermes-hub/",
	} {
		repo, err := registryRepo(context.Background(), func(context.Context, ...string) ([]byte, error) {
			return []byte(remote + "\n"), nil
		})
		if err != nil || repo != "owner/hermes-hub" {
			t.Fatalf("remote %q resolved to %q err=%v", remote, repo, err)
		}
	}
	t.Setenv("GITHUB_REPOSITORY", "Ci/Repo")
	if repo, err := registryRepo(context.Background(), func(context.Context, ...string) ([]byte, error) {
		return nil, errors.New("git must not run")
	}); err != nil || repo != "ci/repo" {
		t.Fatalf("GITHUB_REPOSITORY not honored: %q %v", repo, err)
	}
	t.Setenv("HUB_REGISTRY_REPO", "override/repo")
	if repo, err := registryRepo(context.Background(), nil); err != nil || repo != "override/repo" {
		t.Fatalf("HUB_REGISTRY_REPO not honored: %q %v", repo, err)
	}
	t.Setenv("HUB_REGISTRY_REPO", "")
	t.Setenv("GITHUB_REPOSITORY", "")
	if _, err := registryRepo(context.Background(), func(context.Context, ...string) ([]byte, error) {
		return []byte("https://gitlab.example.com/o/r.git"), nil
	}); err == nil || !strings.Contains(err.Error(), "HUB_REGISTRY_REPO") {
		t.Fatalf("non-github remote must fail with override hint: %v", err)
	}
}

func TestDockerPullPrefersShaTagAndFallsBackToEdge(t *testing.T) {
	var calls []string
	run := func(ctx context.Context, args ...string) error {
		calls = append(calls, strings.Join(args, " "))
		if args[0] == "pull" && strings.Contains(args[1], "sha123-") {
			return fmt.Errorf("not found")
		}
		return nil
	}
	inspect := func(ctx context.Context, args ...string) ([]byte, error) { return []byte("sha123\n"), nil }
	if err := dockerPull(context.Background(), "hermes-hub:test", "prod", "o/r", "sha123", run, inspect); err != nil {
		t.Fatal(err)
	}
	want := []string{
		"pull ghcr.io/o/r/hermes-hub:sha123-prod",
		"pull ghcr.io/o/r/hermes-hub:edge-prod",
		"tag ghcr.io/o/r/hermes-hub:edge-prod hermes-hub:test",
	}
	if !slices.Equal(calls, want) {
		t.Fatalf("unexpected pull sequence: %v", calls)
	}
}

func TestDockerPullUsesShaTagWhenPresent(t *testing.T) {
	var calls []string
	run := func(ctx context.Context, args ...string) error {
		calls = append(calls, strings.Join(args, " "))
		return nil
	}
	inspect := func(ctx context.Context, args ...string) ([]byte, error) { return []byte("sha123\n"), nil }
	if err := dockerPull(context.Background(), "hermes-hub:test", "prod", "o/r", "sha123", run, inspect); err != nil {
		t.Fatal(err)
	}
	want := []string{
		"pull ghcr.io/o/r/hermes-hub:sha123-prod",
		"tag ghcr.io/o/r/hermes-hub:sha123-prod hermes-hub:test",
	}
	if !slices.Equal(calls, want) {
		t.Fatalf("unexpected pull sequence: %v", calls)
	}
}

func TestDockerPullFailsWhenNothingPullable(t *testing.T) {
	run := func(ctx context.Context, args ...string) error { return fmt.Errorf("denied") }
	inspect := func(ctx context.Context, args ...string) ([]byte, error) { return nil, nil }
	if err := dockerPull(context.Background(), "hermes-hub:test", "prod", "o/r", "", run, inspect); err == nil ||
		!strings.Contains(err.Error(), "edge-prod") {
		t.Fatalf("missing refs must fail closed: %v", err)
	}
}

func TestDockerPublishPushesShaAndEdgeTags(t *testing.T) {
	t.Setenv("HUB_DOCKER_CACHE_FROM", "")
	t.Setenv("HUB_DOCKER_CACHE_TO", "")
	var got []string
	if err := dockerPublish(context.Background(), "prod", "o/r", "sha123", func(ctx context.Context, args ...string) error {
		got = append(got, args...)
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	want := []string{"build", "--target", "prod",
		"-t", "ghcr.io/o/r/hermes-hub:sha123-prod",
		"-t", "ghcr.io/o/r/hermes-hub:edge-prod",
		"--push", "--build-arg", "GIT_SHA=sha123",
		"-f", "docker/Dockerfile", "."}
	if !slices.Equal(got, want) {
		t.Fatalf("unexpected publish args: %v", got)
	}
	if err := dockerPublish(context.Background(), "prod", "o/r", "", func(context.Context, ...string) error { return nil }); err == nil {
		t.Fatal("empty sha accepted")
	}
}

func TestDockerPublishSidecarTargetsUseOwnDockerfiles(t *testing.T) {
	t.Setenv("HUB_DOCKER_CACHE_FROM", "")
	t.Setenv("HUB_DOCKER_CACHE_TO", "")
	for _, target := range []string{"stt", "tts"} {
		var got []string
		if err := dockerPublish(context.Background(), target, "o/r", "sha123", func(ctx context.Context, args ...string) error {
			got = append(got, args...)
			return nil
		}); err != nil {
			t.Fatal(err)
		}
		want := []string{"build", "--target", target,
			"-t", "ghcr.io/o/r/hermes-hub:sha123-" + target,
			"-t", "ghcr.io/o/r/hermes-hub:edge-" + target,
			"--push", "--build-arg", "GIT_SHA=sha123",
			"-f", "docker/Dockerfile." + target, "."}
		if !slices.Equal(got, want) {
			t.Fatalf("unexpected %s publish args: %v", target, got)
		}
	}
}
