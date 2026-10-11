package main

import (
	"slices"
	"testing"
)

// hubctl build/up paths must always force BuildKit: the classic builder
// materializes every stage as cache images and duplicates the tagged image.
// The env list is the single source shared with devcheck — a dropped or
// renamed variable anywhere must fail here.
func TestDockerCLIEnvForcesBuildKit(t *testing.T) {
	env := dockerCLIEnv()
	for _, want := range []string{"DOCKER_BUILDKIT=1", "COMPOSE_DOCKER_CLI_BUILD=1", "COMPOSE_BAKE=true"} {
		if !slices.Contains(env, want) {
			t.Fatalf("dockerCLIEnv missing %s", want)
		}
	}
}
