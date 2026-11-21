//go:build linux

package stack

import (
	"os"
	"path/filepath"
	"testing"
)

func TestRuntimeConnectionReadableAsContainerUser(t *testing.T) {
	dir := t.TempDir()
	writeExposureConfig(t, dir)
	s := Settings{Tools: map[string]ToolEntry{"exposure": {Via: ToolViaToolHub}}, PassportServices: map[string]string{"egress-relay": "per-space"}}
	if err := ValidateRuntimeConnections(s, dir); err != nil {
		t.Fatal(err)
	}
	config := filepath.Join(dir, "connections", "exposure", "config.yaml")
	if err := os.Chmod(config, 0600); err != nil {
		t.Fatal(err)
	}
	if err := ValidateRuntimeConnections(s, dir); err == nil {
		t.Fatal("host-readable but runtime-unreadable config admitted")
	}
	if err := os.Chmod(config, 0640); err != nil {
		t.Fatal(err)
	}
	key := filepath.Join(dir, "connections", "exposure", "sish-key.pem")
	if err := os.Chmod(key, 0600); err != nil {
		t.Fatal(err)
	}
	if err := ValidateRuntimeConnections(s, dir); err == nil {
		t.Fatal("runtime-unreadable key admitted")
	}
}
