package api

import "testing"

// docker's workdir flag is lowercase -w; -W is not a real docker flag. If the
// value-taking map lists -W instead, a workload's "-w /dir" is read as a bare
// flag followed by a stray positional operand and fails closed validation.
func TestDockerPositionalsSkipsWorkdirValue(t *testing.T) {
	pos := dockerPositionals([]string{"-w", "/work", "image"})
	if len(pos) != 1 || pos[0] != "image" {
		t.Fatalf("docker -w value must not surface as positional: %v", pos)
	}
}
