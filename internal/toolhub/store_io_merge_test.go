package toolhub

import (
	"os"
	"path/filepath"
	"testing"
)

// The store snapshot round-trips through a locked atomic write; reloads
// reject relative paths, trailing documents, unknown fields and foreign
// schema versions.
func TestStoreSaveLoadRoundTrip(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "state", "store.json")

	store := NewStore()
	if err := store.Save(path); err != nil {
		t.Fatal(err)
	}
	loaded, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if loaded == nil {
		t.Fatal("empty load")
	}
	// A second Save on the loaded store sees an identical on-disk digest.
	if err := loaded.Save(path); err != nil {
		t.Fatal(err)
	}
	// A foreign writer between saves trips the conflict fence.
	stale := NewStore()
	if err := stale.Save(filepath.Join(dir, "state", "other.json")); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(`{"schema":1,"conflict":true}`), 0600); err != nil {
		t.Fatal(err)
	}
	if err := stale.Save(path); err == nil {
		t.Fatal("stale save overwritten foreign bytes")
	}

	if _, err := Load("relative/store.json"); err == nil {
		t.Fatal("relative store path accepted")
	}
	if _, err := Load(filepath.Join(dir, "missing.json")); err == nil {
		t.Fatal("missing store loaded")
	}
	for name, body := range map[string]string{
		"trailing": `{"schema":1} {}`,
		"unknown":  `{"schema":1,"bogus_field":[]}`,
		"schema":   `{"schema":42}`,
	} {
		p := filepath.Join(dir, name+".json")
		if err := os.WriteFile(p, []byte(body), 0600); err != nil {
			t.Fatal(err)
		}
		if _, err := Load(p); err == nil {
			t.Fatalf("%s snapshot accepted", name)
		}
	}
}
