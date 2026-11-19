package toolhub

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/letya999/hermes-hub/internal/identity"
	"github.com/letya999/hermes-hub/internal/memory"
	"github.com/letya999/hermes-hub/internal/memory/night"
	"github.com/letya999/hermes-hub/internal/passportview"
	"github.com/letya999/hermes-hub/internal/toolhub/api"
	"github.com/letya999/hermes-hub/internal/vault"
	"github.com/letya999/hermes-hub/shared/locale"
)

func TestNightRunChecksRightAgainAfterModel(t *testing.T) {
	store, auth, policy, profile := managedStore(t)
	rule := api.RightRule{Capability: "memory", Operation: "dreaming"}
	policy.RightCeiling = append(policy.RightCeiling, rule)
	profile.Rights = append(profile.Rights, rule)
	profile.PassportRevision = "r-0123456789abcdef"
	store.mu.Lock()
	store.capabilityPolicies[policy.PolicyID] = policy
	store.capabilityProfiles[profile.ProfileID] = profile
	store.mu.Unlock()
	store.RightsDoc = api.RightsDoc{Release: "test", DefinitionID: "hub-agent-rights", Capabilities: map[string]api.RightCapability{
		"memory": {Description: "fixture", Operations: map[string]api.RightOperation{
			"dreaming": {Subject: "self", Scope: "none", Issuers: []string{"operator"}, Effect: "write"},
		}},
	}}
	gov := NewGovernanceStore(filepath.Join(t.TempDir(), "governance.json"))
	store.RightsLedger = gov.Load
	passports, vaults := t.TempDir(), t.TempDir()
	writeMemoryPassportEnv(t, passports, "alice", "dev", true)
	vaultRoot := filepath.Join(vaults, "alice")
	if err := vault.Ensure(vaultRoot); err != nil {
		t.Fatal(err)
	}
	daily, err := vault.OpenDaily(vaultRoot, time.UTC)
	if err != nil {
		t.Fatal(err)
	}
	if err := daily.Append(memory.Entry{ID: "turn-1", Kind: memory.KindInteractive, Provenance: memory.ProvenanceOwner, Audience: memory.AudiencePrivate, Body: "I prefer cedar tea", ObservedAt: time.Date(2026, 11, 5, 12, 0, 0, 0, time.UTC)}, "telegram_bot"); err != nil {
		t.Fatal(err)
	}
	resolver, err := NewVaultResolver(vaults, passports)
	if err != nil {
		t.Fatal(err)
	}
	e := &projectionEndpoint{gateway: &Gateway{Store: store, Tokens: map[string]identity.Envelope{"synthetic": auth}, PassportRoot: passports, Vaults: resolver}}
	plan, err := e.NightSchedule(auth)
	if err != nil || !plan.Enabled || plan.LocalTime != "04:00" {
		t.Fatalf("plan=%+v err=%v", plan, err)
	}
	calls := 0
	relay := func(context.Context, string, string) (string, error) {
		calls++
		return `{"summary":"tea","facts":[{"card":"notes/tea","fact":"prefers cedar tea","quote":"I prefer cedar tea"}]}`, nil
	}
	run := func() (string, error) {
		return e.nightRunWithRelay(context.Background(), auth, "2026-11-05", plan, locale.LangEN, nil, relay)
	}
	if _, err := run(); err == nil || calls != 0 {
		t.Fatalf("ungranted model wake: calls=%d err=%v", calls, err)
	}
	grantID := "rightgrant-alice-dreaming-1"
	mintRight(t, gov, store.RightsDoc, api.NewRightGrant(grantID, "alice", "memory", "dreaming", "", "fixture", "operator", time.Now().Add(time.Hour)))
	if decision, err := store.Decide(auth, "memory", "dreaming", "", time.Now()); err != nil || !decision.Allow {
		t.Fatalf("grant not effective: %+v %v", decision, err)
	}
	relay = func(context.Context, string, string) (string, error) {
		calls++
		if err := gov.RevokeRight(grantID, "operator", "fixture revoke", time.Now()); err != nil {
			t.Fatal(err)
		}
		return `{"summary":"tea","facts":[{"card":"notes/tea","fact":"prefers cedar tea","quote":"I prefer cedar tea"}]}`, nil
	}
	if _, err := run(); err == nil || calls != 1 {
		t.Fatalf("revoked commit: calls=%d err=%v", calls, err)
	}
	if _, err := os.Stat(filepath.Join(vaultRoot, "runtime", "memory", "night", "2026-11-05.json")); !os.IsNotExist(err) {
		t.Fatalf("revoked vault mutation: %v", err)
	}
	mintRight(t, gov, store.RightsDoc, api.NewRightGrant("rightgrant-alice-dreaming-2", "alice", "memory", "dreaming", "", "fixture", "operator", time.Now().Add(time.Hour)))
	journal := filepath.Join(passports, "alice", "passports", passportview.ApplyJournalFile)
	relay = func(context.Context, string, string) (string, error) {
		calls++
		if err := os.WriteFile(journal, []byte("{}\n"), 0600); err != nil {
			t.Fatal(err)
		}
		return `{"summary":"tea","facts":[{"card":"notes/tea","fact":"prefers cedar tea","quote":"I prefer cedar tea"}]}`, nil
	}
	if _, err := run(); err == nil || calls != 2 {
		t.Fatalf("interrupted apply admitted a dreaming commit: calls=%d err=%v", calls, err)
	}
	if _, err := os.Stat(filepath.Join(vaultRoot, "runtime", "memory", "night", "2026-11-05.json")); !os.IsNotExist(err) {
		t.Fatalf("mixed activation mutated the vault: %v", err)
	}
	if err := os.Remove(journal); err != nil {
		t.Fatal(err)
	}
	relay = func(context.Context, string, string) (string, error) {
		calls++
		return `{"summary":"tea","facts":[{"card":"notes/tea","fact":"prefers cedar tea","quote":"I prefer cedar tea"}]}`, nil
	}
	digest, err := run()
	if err != nil || digest == "" || calls != 3 {
		t.Fatalf("authorized run: digest=%q calls=%d err=%v", digest, calls, err)
	}
	if !night.DayComplete(vaultRoot, "2026-11-05") {
		t.Fatal("day did not complete")
	}
	if _, err := run(); err != nil || calls != 3 {
		t.Fatalf("completed day repeated model: calls=%d err=%v", calls, err)
	}
}
