package stack

import "testing"

func TestComposeHostPathsForContainerSupervisor(t *testing.T) {
	root, dir := composeHostPaths("/", "/spaces/alice", `C:\hub\spaces`)
	if root != "C:/hub" || dir != "C:/hub/spaces/alice" {
		t.Fatalf("container render used non-host bind paths: %q %q", root, dir)
	}
	root, dir = composeHostPaths("/", "/spaces/alice", "/srv/spaces")
	if root != "/srv" || dir != "/srv/spaces/alice" {
		t.Fatalf("linux host bind paths: %q %q", root, dir)
	}
	root, dir = composeHostPaths("/project", "/project/spaces/alice", "/srv/spaces")
	if root != "/project" || dir != "/project/spaces/alice" {
		t.Fatal("host render path was remapped")
	}
}
