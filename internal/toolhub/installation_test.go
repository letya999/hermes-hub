package toolhub

import (
	"path/filepath"
	"testing"
)

// The `.initialized` marker lives inside the same volume as the store, so a
// whole-volume loss erases it together with the data and a recreated empty
// volume looks like a first boot. An installation identity persisted inside
// the snapshot and witnessed on an independent filesystem location detects
// that replacement: a store that does not carry the witnessed installation ID
// is foreign, not a fresh install.
func TestInstallationWitnessDetectsVolumeReplacement(t *testing.T) {
	volume := t.TempDir()
	witnessDir := t.TempDir()
	witnessPath := filepath.Join(witnessDir, "toolhub-installation")
	storePath := filepath.Join(volume, "toolhub", "store.json")

	first := NewStore()
	if err := first.Save(storePath); err != nil {
		t.Fatal(err)
	}
	loaded, err := Load(storePath)
	if err != nil {
		t.Fatal(err)
	}
	if err := loaded.VerifyInstallationWitness(witnessPath); err != nil {
		t.Fatalf("first pairing must write the independent witness: %v", err)
	}
	if err := loaded.VerifyInstallationWitness(witnessPath); err != nil {
		t.Fatalf("same installation must verify against its witness: %v", err)
	}

	// Whole-volume loss: the operator recreates an empty volume and the
	// first boot would silently re-initialize an empty registry — losing
	// every confirmed onboarding and credential reference. A fresh store on
	// the new volume carries a different installation identity.
	replacement := t.TempDir()
	replacementPath := filepath.Join(replacement, "toolhub", "store.json")
	if err := NewStore().Save(replacementPath); err != nil {
		t.Fatal(err)
	}
	reloaded, err := Load(replacementPath)
	if err != nil {
		t.Fatal(err)
	}
	if err := reloaded.VerifyInstallationWitness(witnessPath); err == nil {
		t.Fatal("a store from a replaced volume verified against the surviving witness")
	}
}

// A store snapshot written before installation identity existed carries no
// ID; verification binds a generated identity that the next Save persists,
// so the pairing stays stable across the upgrade instead of flapping.
func TestInstallationWitnessAdoptsLegacyStore(t *testing.T) {
	volume := t.TempDir()
	witnessPath := filepath.Join(t.TempDir(), "toolhub-installation")
	storePath := filepath.Join(volume, "store.json")
	if err := NewStore().Save(storePath); err != nil {
		t.Fatal(err)
	}
	loaded, err := Load(storePath)
	if err != nil {
		t.Fatal(err)
	}
	loaded.installationID = ""
	if err := loaded.VerifyInstallationWitness(witnessPath); err != nil {
		t.Fatalf("legacy adoption must write the witness: %v", err)
	}
	if err := loaded.Save(storePath); err != nil {
		t.Fatal(err)
	}
	reloaded, err := Load(storePath)
	if err != nil {
		t.Fatal(err)
	}
	if err := reloaded.VerifyInstallationWitness(witnessPath); err != nil {
		t.Fatalf("adopted identity changed after save: %v", err)
	}
}
