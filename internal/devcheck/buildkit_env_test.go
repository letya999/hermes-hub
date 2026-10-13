package devcheck

import (
	"slices"
	"testing"
)

// Every hub image build — hubctl, devcheck, CI publish — runs through the
// same forced-BuildKit env. The list is exported so both packages share one
// source of truth instead of drifting copies.
func TestBuildKitEnvIsTheSingleSource(t *testing.T) {
	want := []string{"DOCKER_BUILDKIT=1", "COMPOSE_DOCKER_CLI_BUILD=1", "COMPOSE_BAKE=true"}
	if !slices.Equal(BuildKitEnv, want) {
		t.Fatalf("BuildKitEnv drifted: %v", BuildKitEnv)
	}
}
