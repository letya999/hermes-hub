package materialize

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/letya999/credential-broker/contract"
	"github.com/letya999/credential-broker/provider"
)

func fixture(t *testing.T) (*Manager, contract.Contract, string) {
	t.Helper()
	d := t.TempDir()
	_ = os.Chmod(d, 0700)
	m, e := New(d, false)
	if e != nil {
		t.Fatal(e)
	}
	c := contract.Contract{ID: "file-example", Revision: 1, Title: "File example", Storage: "local", Fields: []contract.Field{{ID: "token", Label: "Token", Kind: "secret", Required: true, MaxBytes: 100}, {ID: "config", Label: "JSON", Kind: "json", Required: true, MaxBytes: 1000}}, Deliveries: []contract.Delivery{{Type: "env", Field: "token", Target: "API_TOKEN"}, {Type: "file", Field: "config", Target: "/run/secrets/config.json", EnvName: "CONFIG_FILE"}, {Type: "json_file", Target: "/app/auth.json", Mapping: map[string]string{"token": "token"}}}, State: &contract.State{Target: "/home/mcp/.config/service", Files: []contract.StateFile{{Name: "runtime_session.json", MaxBytes: 1000, JSON: true}}}}
	return m, c, "l" + strings.Repeat("a", 32)
}
func TestFilesStateAndCleanup(t *testing.T) {
	m, c, id := fixture(t)
	defer m.Close()
	values := provider.Bundle{"token": []byte("fake-token"), "config": []byte(`{"client_id":"example"}`)}
	out, e := m.Prepare(id, time.Now().Add(time.Minute), c, values, provider.Bundle{"runtime_session.json": []byte(`{"session":"old"}`)})
	if e != nil || out.Env["API_TOKEN"] != "fake-token" || out.Env["CONFIG_FILE"] != "/run/secrets/config.json" || len(out.Mounts) != 3 {
		t.Fatal(e, out)
	}
	for _, mount := range out.Mounts {
		if mount.ReadOnly {
			fi, e := os.Stat(mount.Source)
			if e != nil || fi.Mode().Perm() != 0400 {
				t.Fatal(e)
			}
			raw, _ := os.ReadFile(mount.Source)
			if !json.Valid(raw) {
				t.Fatal("json")
			}
		}
	}
	if _, e = m.Prepare(id, time.Now(), c, values, nil); e == nil {
		t.Fatal("duplicate materialization")
	}
	stateDir := out.Mounts[2].Source
	_ = os.WriteFile(filepath.Join(stateDir, "runtime_session.json"), []byte(`{"session":"new"}`), 0600)
	snap, e := m.Snapshot(id, c)
	if e != nil || string(snap["runtime_session.json"]) != `{"session":"new"}` {
		t.Fatal(e)
	}
	_ = os.Remove(filepath.Join(stateDir, "runtime_session.json"))
	snap, e = m.Snapshot(id, c)
	if e != nil || len(snap) != 0 {
		t.Fatal("optional absent state")
	}
	_ = os.Symlink(out.Mounts[0].Source, filepath.Join(stateDir, "runtime_session.json"))
	if _, e = m.Snapshot(id, c); e == nil {
		t.Fatal("symlink state")
	}
	_ = os.Remove(filepath.Join(stateDir, "runtime_session.json"))
	_ = os.WriteFile(filepath.Join(stateDir, "runtime_session.json"), []byte(`bad`), 0600)
	if _, e = m.Snapshot(id, c); e == nil {
		t.Fatal("invalid JSON")
	}
	if e = m.Remove(id); e != nil {
		t.Fatal(e)
	}
	if _, e = os.Stat(out.Mounts[0].Source); !os.IsNotExist(e) {
		t.Fatal("cleanup")
	}
	if e = m.Remove("../outside"); e == nil {
		t.Fatal("cleanup traversal")
	}
	if _, e = m.Snapshot(id, c); e == nil {
		t.Fatal("absent lease")
	}
}
func TestMaterializationRefusalsAndStartup(t *testing.T) {
	m, c, id := fixture(t)
	defer m.Close()
	v := provider.Bundle{"token": []byte("x"), "config": []byte(`{}`)}
	if _, e := m.Prepare("../bad", time.Now(), c, v, nil); e == nil {
		t.Fatal("id")
	}
	if _, e := m.Prepare(id, time.Now(), c, nil, nil); e == nil {
		t.Fatal("missing fields")
	}
	if _, e := m.Prepare(id, time.Now(), c, v, provider.Bundle{"runtime_session.json": []byte("bad")}); e == nil {
		t.Fatal("state validation")
	}
	if _, e := os.Stat(filepath.Join(m.root, id)); !os.IsNotExist(e) {
		t.Fatal("failed materialization leaked directory")
	}
	if _, e := m.Prepare(id, time.Now(), c, v, nil); e != nil {
		t.Fatal(e)
	}
	if _, e := New(m.root, false); e != nil {
		t.Fatal("startup removes stale lease dirs", e)
	}
	_ = os.WriteFile(filepath.Join(m.root, "unexpected"), nil, 0600)
	if _, e := New(m.root, false); e == nil {
		t.Fatal("unknown file must block startup")
	}
	if _, e := New("relative", false); e == nil {
		t.Fatal("root")
	}
}
