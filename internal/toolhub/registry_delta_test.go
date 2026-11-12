package toolhub

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"path/filepath"
	"testing"
	"time"

	"github.com/letya999/hermes-hub/internal/toolhub/api"
)

func TestCatalogEnableNoopDoesNotWriteHistory(t *testing.T) {
	s, auth, _, _ := managedStore(t)
	d := api.CLICatalogDefinitions()[1]
	if err := s.RegisterDefinition(d); err != nil {
		t.Fatal(err)
	}
	if err := s.PromoteToCatalog(d.DefinitionID, d.Version, "operator"); err != nil {
		t.Fatal(err)
	}
	before := s.capabilityProfiles[auth.CapabilityProfile].Revision
	if _, err := s.EnableCatalog(auth, d.DefinitionID, d.Version, "operator", true); err != nil {
		t.Fatal(err)
	}
	if got := s.capabilityProfiles[auth.CapabilityProfile].Revision; got != before+1 {
		t.Fatalf("profile advanced %d times", got-before)
	}
	path := filepath.Join(t.TempDir(), "store.db")
	if err := s.Save(path); err != nil {
		t.Fatal(err)
	}
	s.path = path
	t.Cleanup(func() { _ = s.CloseDB() })
	changes, digest := len(s.capabilityChanges), s.diskDigest
	var totalBefore, totalAfter int64
	if err := s.db.QueryRow(`SELECT total_changes()`).Scan(&totalBefore); err != nil {
		t.Fatal(err)
	}
	for range 100 {
		if _, err := s.EnableCatalog(auth, d.DefinitionID, d.Version, "operator", false); err != nil {
			t.Fatal(err)
		}
	}
	if err := s.db.QueryRow(`SELECT total_changes()`).Scan(&totalAfter); err != nil {
		t.Fatal(err)
	}
	if totalBefore != totalAfter || len(s.capabilityChanges) != changes || s.diskDigest != digest {
		t.Fatal("no-op catalog enable wrote state")
	}
}

func TestStoreDBLegacyDigestMigratesWithoutHistoryRewrite(t *testing.T) {
	s := NewStore()
	path := filepath.Join(t.TempDir(), "store.db")
	if err := s.Save(path); err != nil {
		t.Fatal(err)
	}
	state, err := s.buildSnapshotLocked()
	if err != nil {
		t.Fatal(err)
	}
	body, err := json.MarshalIndent(state, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	digest := sha256.Sum256(body)
	if _, err := s.db.Exec(`UPDATE meta SET v=? WHERE k='digest'`, hex.EncodeToString(digest[:])); err != nil {
		t.Fatal(err)
	}
	if _, err := s.db.Exec(`DELETE FROM meta WHERE k='digest_format'`); err != nil {
		t.Fatal(err)
	}
	if err := s.CloseDB(); err != nil {
		t.Fatal(err)
	}
	s, err = Load(path)
	if err != nil {
		t.Fatalf("legacy digest rejected: %v", err)
	}
	t.Cleanup(func() { _ = s.CloseDB() })
	o := m13P3Onboarding(aliceAuth(), "migration", "github", "https://github.com/example/mcp", api.PhaseAwaitingCreds)
	if err := s.PutOnboarding(o); err != nil {
		t.Fatal(err)
	}
	loaded, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = loaded.CloseDB() })
	if _, err := loaded.OnboardingFor(aliceAuth(), o.OnboardingID); err != nil {
		t.Fatal(err)
	}
	if _, err := s.db.Exec(`UPDATE ent SET body=replace(CAST(body AS TEXT), 'awaiting-credentials', 'awaiting-confirm') WHERE collection='onboarding'`); err != nil {
		t.Fatal(err)
	}
	if _, err := Load(path); !errors.Is(err, api.ErrConflict) {
		t.Fatalf("row tampering escaped digest: %v", err)
	}
}

func TestOnboardingAndJournalRollbackTogether(t *testing.T) {
	s, auth := NewStore(), aliceAuth()
	o := m13P3Onboarding(auth, "atomic", "github", "https://github.com/example/mcp", api.PhaseAwaitingCreds)
	if err := s.PutOnboarding(o); err != nil {
		t.Fatal(err)
	}
	c := &ControlPlane{Store: s, Now: time.Now}
	op := c.newConnectorOperation(auth, api.OpIntentInstall, "github", connectorInstallSteps)
	op.OnboardingID = o.OnboardingID
	if err := s.PutOperation(op); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "store.db")
	if err := s.Save(path); err != nil {
		t.Fatal(err)
	}
	s.path = path
	t.Cleanup(func() { _ = s.CloseDB() })
	if _, err := s.db.Exec(`CREATE TRIGGER deny_onboarding BEFORE UPDATE ON ent WHEN NEW.collection='onboarding' BEGIN SELECT RAISE(ABORT, 'injected persistence failure'); END`); err != nil {
		t.Fatal(err)
	}
	o.Revision++
	o.Phase = api.PhaseAwaitingConfirm
	if err := s.PutOnboarding(o); err == nil {
		t.Fatal("injected transaction failure disappeared")
	}
	current, _ := s.OnboardingFor(auth, o.OnboardingID)
	journal, _ := s.OperationFor(auth, op.OperationID)
	if current.Phase != api.PhaseAwaitingCreds || journal.State != api.OpStateAwaitingUser {
		t.Fatal("failed transaction leaked next state into memory")
	}
	loaded, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = loaded.CloseDB() })
	current, _ = loaded.OnboardingFor(auth, o.OnboardingID)
	journal, _ = loaded.OperationFor(auth, op.OperationID)
	if current.Phase != api.PhaseAwaitingCreds || journal.State != api.OpStateAwaitingUser {
		t.Fatal("state and journal did not roll back together")
	}
}

func TestStoreDBSmallMutationPreservesLargeHistory(t *testing.T) {
	s, auth, _, profile := managedStore(t)
	// Valid immutable historical profiles exceed a single mutation's budget.
	profile.Reason = string(bytes.Repeat([]byte("historical reason "), 24))
	for i := range 200 {
		profile.Revision += uint64(i + 1)
		if err := putProfile(t, s, profile); err != nil {
			t.Fatal(err)
		}
	}
	path := filepath.Join(t.TempDir(), "store.db")
	if err := s.Save(path); err != nil {
		t.Fatal(err)
	}
	s.path = path
	t.Cleanup(func() { _ = s.CloseDB() })
	// A SQL trigger detects even a rewrite of identical historical bytes.
	for _, query := range []string{
		`CREATE TRIGGER history_no_delete BEFORE DELETE ON listrec BEGIN SELECT RAISE(ABORT, 'history deleted'); END`,
		`CREATE TRIGGER history_no_update BEFORE UPDATE ON listrec BEGIN SELECT RAISE(ABORT, 'history changed'); END`,
	} {
		if _, err := s.db.Exec(query); err != nil {
			t.Fatal(err)
		}
	}
	t.Setenv("HUB_TOOLHUB_STATE_MAX_NEXT_WRITE_BYTES", "131072")
	s.storage = nil
	o := api.Onboarding{Schema: api.SchemaVersion, OnboardingID: "ob-small", PrincipalID: auth.PrincipalID, ContextID: auth.ContextID, RuntimeID: auth.RuntimeID, PolicyVersion: auth.PolicyVersion, Mode: api.OnboardingSelfInstall, Phase: api.PhaseFailed, Revision: 1, CreatedAt: time.Now().UTC()}
	if err := s.PutOnboarding(o); err != nil {
		t.Fatalf("small mutation charged for history: %v", err)
	}
	loaded, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = loaded.CloseDB() })
	if _, err := loaded.OnboardingFor(auth, o.OnboardingID); err != nil {
		t.Fatal(err)
	}
	if len(loaded.capabilityChanges) != len(s.capabilityChanges) {
		t.Fatal("history lost")
	}
	var historyBytes int64
	if err := s.db.QueryRow(`SELECT sum(length(body)) FROM listrec`).Scan(&historyBytes); err != nil || historyBytes <= 131072 {
		t.Fatal("fixture did not exceed mutation budget")
	}
}
