package stack

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

func TestHonchoProviderNamespacesAndDisableAreExplicit(t *testing.T) {
	spaces := map[string]string{}
	names := map[string]string{}
	for _, owner := range []string{"alice", "bob"} {
		dir := t.TempDir()
		os.Mkdir(filepath.Join(dir, "hermes"), 0700)
		s := Settings{User: owner, Honcho: true, HonchoURL: "https://honcho.example"}
		if err := writeHonchoConfig(dir, s); err != nil {
			t.Fatal(err)
		}
		name := filepath.Join(dir, "hermes", "honcho.json")
		body, err := os.ReadFile(name)
		if err != nil {
			t.Fatal(err)
		}
		var config struct {
			Hosts map[string]struct {
				Workspace string
				PeerName  string
			}
		}
		if err := json.Unmarshal(body, &config); err != nil {
			t.Fatal(err)
		}
		host := config.Hosts["hermes"]
		if host.PeerName != owner {
			t.Error("owner identity lost")
		}
		spaces[owner] = host.Workspace
		names[owner] = name
		s.Honcho = false
		if err := writeHonchoConfig(dir, s); err != nil {
			t.Fatal(err)
		}
		if _, err := os.Stat(name); !os.IsNotExist(err) {
			t.Error("disabled provider left an active configuration")
		}
	}
	if spaces["alice"] == spaces["bob"] {
		t.Error("different owners share remote memory workspace")
	}
}
