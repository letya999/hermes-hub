//go:build !windows

package runtime

import (
	"net"
	"path/filepath"
	"testing"
)

func TestPrepareSkipsUnixSockets(t *testing.T) {
	root := t.TempDir()
	socketPath := filepath.Join(root, "runtime.sock")
	listener, err := net.Listen("unix", socketPath)
	if err != nil {
		t.Skipf("unix sockets unavailable: %v", err)
	}
	defer listener.Close()

	seen := map[string]bool{}
	err = Prepare([]string{root}, 1, 2, func(path string, _, _ int) error {
		seen[path] = true
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if seen[socketPath] {
		t.Fatalf("Prepare touched runtime socket %s", socketPath)
	}
}
