package supervisor

import (
	"context"
	"encoding/json"
	"path/filepath"
	"reflect"
	"testing"
)

func TestContainerSupervisorMountIsolation(t *testing.T) {
	root := t.TempDir()
	m := &Manager{cfg: Config{SpacesRoot: root, HostSpacesRoot: `C:\hub\spaces`, RuntimePort: 8080}}
	source := filepath.Join(root, "alice", "workspace")
	got, err := m.hostBindSource(source)
	if err != nil || got != "/run/desktop/mnt/host/c/hub/spaces/alice/workspace" {
		t.Fatalf("mapped source=%q: %v", got, err)
	}
	for _, source := range []string{filepath.Dir(root), filepath.Join(root, "..", "other", "workspace")} {
		if _, err := m.hostBindSource(source); err == nil {
			t.Fatalf("accepted outside source %s", source)
		}
	}
	m.cfg.HostSpacesRoot = "/srv/spaces"
	if got, err := m.hostBindSource(root); err != nil || got != "/srv/spaces" {
		t.Fatalf("root=%q: %v", got, err)
	}
	m.cfg.HostSpacesRoot = "relative"
	if _, err := m.hostBindSource(root); err == nil {
		t.Fatal("accepted relative host root")
	}
	m.cfg.HostSpacesRoot = ""
	if got, err := m.hostBindSource(source); err != nil || got != source {
		t.Fatalf("host mapping changed: %q %v", got, err)
	}
}

func TestContainerSupervisorOwnershipChecksHostMount(t *testing.T) {
	root := t.TempDir()
	source := "/srv/spaces/alice/workspace"
	m := &Manager{cfg: Config{SpacesRoot: root, HostSpacesRoot: "/srv/spaces", Command: func(_ context.Context, args ...string) ([]byte, error) {
		return json.Marshal([]map[string]string{{"Type": "bind", "Source": source, "Destination": "/scope"}})
	}}}
	item := Runtime{ContextID: "alice", Container: "runtime"}
	if err := m.verifyScopeMount(context.Background(), item); err != nil {
		t.Fatal(err)
	}
	source = "/srv/spaces/bob/workspace"
	if err := m.verifyScopeMount(context.Background(), item); err == nil {
		t.Fatal("accepted another user's workspace")
	}
}

func TestContainerSupervisorDockerRunContract(t *testing.T) {
	root := t.TempDir()
	var calls [][]string
	m := &Manager{cfg: Config{SpacesRoot: root, HostSpacesRoot: "/srv/spaces", RuntimePort: 8080,
		Command: func(_ context.Context, args ...string) ([]byte, error) { calls = append(calls, args); return nil, nil }}}
	args := []string{"run", "-p", "127.0.0.1:42000:8080", "--mount", "type=bind,src=" + filepath.Join(root, "alice", "workspace") + ",dst=/scope,readonly", "--mount", "type=volume,src=alice-data,dst=/state", "--env-file", filepath.Join(root, "alice", "runtime.env"), "hermes:test"}
	want := []string{"run", "--mount", "type=bind,src=/srv/spaces/alice/workspace,dst=/scope,readonly", "--mount", "type=volume,src=alice-data,dst=/state", "--env-file", filepath.Join(root, "alice", "runtime.env"), "hermes:test"}
	if _, err := m.command(context.Background(), args...); err != nil || len(calls) != 1 || !reflect.DeepEqual(calls[0], want) {
		t.Fatalf("calls=%v err=%v", calls, err)
	}
	if _, err := m.command(context.Background(), "run", "--mount", "type=bind,src="+filepath.Dir(root)+",dst=/scope"); err == nil || len(calls) != 1 {
		t.Fatal("outside bind reached Docker")
	}
	if got := m.runtimeAddress("alice", 42000, false); got != "http://alice:8080" {
		t.Fatal(got)
	}
	if got := m.runtimeAddress("alice", 42000, true); got != "http://alice-ctl:8091" {
		t.Fatal(got)
	}
	m.cfg.HostSpacesRoot = ""
	if got := m.runtimeAddress("alice", 42000, true); got != "http://127.0.0.1:42000" {
		t.Fatal(got)
	}
}
