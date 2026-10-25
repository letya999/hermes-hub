package toolhub

import (
	"encoding/json"
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

// Onboardings written before recipes became content-addressed carry the
// resolution inline under "recipe"; load must re-home it under its digest
// instead of rejecting the store.
func TestStoreLoadMigratesEmbeddedRecipe(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "state", "store.json")
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		t.Fatal(err)
	}
	body := `{"schema":1,"onboardings":[{
		"schema":1,"onboarding_id":"onboard-legacy","principal_id":"telegram-1",
		"context_id":"telegram-1","runtime_id":"telegram-1","policy_version":"policy-1",
		"mode":"self-install","phase":"awaiting-credentials","revision":1,
		"recipe":{"source":{"repository":"https://example.com/x","commit_sha":"deadbeef"},
		"launch":{"artifact":"img:1","transport":"stdio"},"connection":{},"state":"resolved"}
	}]}`
	if err := os.WriteFile(path, []byte(body), 0600); err != nil {
		t.Fatal(err)
	}
	loaded, err := Load(path)
	if err != nil {
		t.Fatalf("embedded recipe store rejected: %v", err)
	}
	if err := loaded.Save(path); err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var saved struct {
		Recipes     map[string]json.RawMessage   `json:"recipes"`
		Onboardings []map[string]json.RawMessage `json:"onboardings"`
	}
	if err := json.Unmarshal(raw, &saved); err != nil {
		t.Fatal(err)
	}
	if len(saved.Onboardings) != 1 || len(saved.Recipes) != 1 {
		t.Fatalf("unexpected snapshot shape: %s", raw)
	}
	onboarding := saved.Onboardings[0]
	if _, ok := onboarding["recipe"]; ok {
		t.Fatalf("embedded recipe survived a save: %s", raw)
	}
	var digest string
	if err := json.Unmarshal(onboarding["recipe_digest"], &digest); err != nil {
		t.Fatalf("recipe_digest missing: %s", raw)
	}
	if _, ok := saved.Recipes[digest]; !ok {
		t.Fatalf("recipe not stored under digest %s: %s", digest, raw)
	}
}
