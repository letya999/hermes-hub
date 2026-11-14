package toolhub

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/letya999/hermes-hub/internal/toolhub/api"
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

func TestInitializedMissingStoreRefusesEmptyLoad(t *testing.T) {
	path := filepath.Join(t.TempDir(), "toolhub", "store.json")
	if err := NewStore().Save(path); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	if _, err := Load(path); !os.IsNotExist(err) {
		t.Fatalf("missing initialized store loaded: %v", err)
	}
	if err := NewStore().Save(path); err == nil {
		t.Fatal("initialized store was silently replaced by an empty one")
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatal("refused reinitialization changed missing store", err)
	}
}

func TestFailedOnboardingSaveDoesNotExposeUncommittedNonce(t *testing.T) {
	path := filepath.Join(t.TempDir(), "toolhub", "store.json")
	store := NewStore()
	record := api.Onboarding{Schema: api.SchemaVersion, OnboardingID: "onboard-retry", PrincipalID: "alice", ContextID: "alice", RuntimeID: "runtime", PolicyVersion: "policy-1", Mode: api.OnboardingSelfInstall, Stage: api.StagePending, Gate: &api.Gate{Kind: api.GateConfirmSetup, Nonce: "old-nonce", Expires: time.Time{}}, Revision: 1}
	if err := store.PutOnboarding(record); err != nil {
		t.Fatal(err)
	}
	if err := store.Save(path); err != nil {
		t.Fatal(err)
	}
	store, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	marker := path + ".initialized"
	if err := os.Remove(marker); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(marker, 0700); err != nil {
		t.Fatal(err)
	}
	changed := record
	changed.Revision++
	changed.ArmGate(api.GateConfirmSetup, "uncommitted-nonce", time.Time{})
	if err := store.PutOnboarding(changed); err == nil {
		t.Fatal("failed save reported success")
	}
	visible, err := store.OnboardingFor(aliceAuth(), record.OnboardingID)
	if err != nil || visible.Revision != record.Revision || visible.GateNonce(api.GateConfirmSetup) != record.GateNonce(api.GateConfirmSetup) {
		t.Fatalf("uncommitted nonce exposed: %+v %v", visible, err)
	}
	newRecord := record
	newRecord.OnboardingID = "onboard-new"
	if err := store.PutOnboarding(newRecord); err == nil {
		t.Fatal("new onboarding save failure reported success")
	}
	if _, err := store.OnboardingFor(aliceAuth(), newRecord.OnboardingID); err == nil {
		t.Fatal("new uncommitted onboarding exposed")
	}
}

func TestOnboardingDirectorySyncFailureKeepsReplacedSnapshotVisible(t *testing.T) {
	path := filepath.Join(t.TempDir(), "toolhub", "store.json")
	store := NewStore()
	record := api.Onboarding{Schema: api.SchemaVersion, OnboardingID: "onboard-retry", PrincipalID: "alice", ContextID: "alice", RuntimeID: "runtime", PolicyVersion: "policy-1", Mode: api.OnboardingSelfInstall, Stage: api.StagePending, Gate: &api.Gate{Kind: api.GateConfirmSetup, Nonce: "old-nonce", Expires: time.Time{}}, Revision: 1}
	if err := store.PutOnboarding(record); err != nil {
		t.Fatal(err)
	}
	if err := store.Save(path); err != nil {
		t.Fatal(err)
	}
	store, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	store.syncDirectory = func(string) error { return os.ErrPermission }
	changed := record
	changed.Revision++
	changed.ArmGate(api.GateConfirmSetup, "replaced-nonce", time.Time{})
	if err := store.PutOnboarding(changed); err == nil {
		t.Fatal("directory sync failure was lost")
	}
	visible, err := store.OnboardingFor(aliceAuth(), record.OnboardingID)
	if err != nil || visible.Revision != changed.Revision || visible.GateNonce(api.GateConfirmSetup) != changed.GateNonce(api.GateConfirmSetup) {
		t.Fatalf("memory diverged after snapshot replacement: %+v %v", visible, err)
	}
	disk, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	visible, err = disk.OnboardingFor(aliceAuth(), record.OnboardingID)
	if err != nil || visible.Revision != changed.Revision || visible.GateNonce(api.GateConfirmSetup) != changed.GateNonce(api.GateConfirmSetup) {
		t.Fatalf("snapshot replacement missing: %+v %v", visible, err)
	}
}

func TestConfirmationClaimSaveFailureKeepsMemoryAndDiskAligned(t *testing.T) {
	path := filepath.Join(t.TempDir(), "toolhub", "store.json")
	store := NewStore()
	record := api.Onboarding{Schema: api.SchemaVersion, OnboardingID: "onboard-claim", PrincipalID: "alice", ContextID: "alice", RuntimeID: "runtime", PolicyVersion: "policy-1", Mode: api.OnboardingSelfInstall, Stage: api.StagePending, Gate: &api.Gate{Kind: api.GateConfirmSetup, Nonce: "nonce", Expires: time.Time{}}, Revision: 1}
	if err := store.PutOnboarding(record); err != nil {
		t.Fatal(err)
	}
	if err := store.Save(path); err != nil {
		t.Fatal(err)
	}
	store, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	marker := path + ".initialized"
	if err := os.Remove(marker); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(marker, 0700); err != nil {
		t.Fatal(err)
	}
	expected := record
	expected.Error = "stale caller copy"
	if _, err := store.ConsumeConfirmation(expected); err == nil {
		t.Fatal("failed claim reported success")
	}
	visible, err := store.OnboardingFor(aliceAuth(), record.OnboardingID)
	if err != nil || visible.ConfirmationUsed || visible.Revision != record.Revision || visible.Error != record.Error {
		t.Fatalf("uncommitted claim visible: %+v %v", visible, err)
	}
	if err := os.Remove(marker); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(marker, nil, 0600); err != nil {
		t.Fatal(err)
	}
	store.syncDirectory = func(string) error { return os.ErrPermission }
	if _, err := store.ConsumeConfirmation(record); err == nil {
		t.Fatal("directory sync failure was lost")
	}
	visible, err = store.OnboardingFor(aliceAuth(), record.OnboardingID)
	if err != nil || !visible.ConfirmationUsed || visible.Revision != record.Revision+1 {
		t.Fatalf("replaced claim missing in memory: %+v %v", visible, err)
	}
	disk, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	visible, err = disk.OnboardingFor(aliceAuth(), record.OnboardingID)
	if err != nil || !visible.ConfirmationUsed || visible.Revision != record.Revision+1 {
		t.Fatalf("replaced claim missing on disk: %+v %v", visible, err)
	}
}

func TestSavedStoreMissingSnapshotRefusesRewrite(t *testing.T) {
	path := filepath.Join(t.TempDir(), "toolhub", "store.json")
	store := NewStore()
	if err := store.Save(path); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	if err := store.Save(path); err == nil {
		t.Fatal("same Store instance recreated missing initialized snapshot")
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

func TestSaveDirectorySyncFailureVisible(t *testing.T) {
	store := NewStore()
	store.syncDirectory = func(string) error { return os.ErrPermission }
	if err := store.Save(filepath.Join(t.TempDir(), "store.json")); err == nil {
		t.Fatal("directory sync failure was lost")
	}
}

func TestInitializedMarkerDirectorySyncFailureVisible(t *testing.T) {
	store := NewStore()
	calls := 0
	store.syncDirectory = func(string) error {
		calls++
		if calls == 2 {
			return os.ErrPermission
		}
		return nil
	}
	if err := store.Save(filepath.Join(t.TempDir(), "store.json")); err == nil || calls != 2 {
		t.Fatalf("marker directory sync failure was lost: %v, calls=%d", err, calls)
	}
}

func TestStoreRejectsNonFileInitializedMarker(t *testing.T) {
	path := filepath.Join(t.TempDir(), "toolhub", "store.json")
	store := NewStore()
	if err := store.Save(path); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(path + ".initialized"); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(path+".initialized", 0700); err != nil {
		t.Fatal(err)
	}
	if err := store.Save(path); err == nil {
		t.Fatal("non-file initialized marker accepted")
	}
}
