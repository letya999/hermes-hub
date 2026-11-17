package main

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/letya999/hermes-hub/internal/agenttools"
	"github.com/letya999/hermes-hub/internal/catalog"
	"github.com/letya999/hermes-hub/internal/passport"
	"github.com/letya999/hermes-hub/internal/reconcile"
	"github.com/letya999/hermes-hub/internal/storagebudget"
	"github.com/letya999/hermes-hub/internal/toolhub"
	"github.com/letya999/hermes-hub/internal/toolhub/api"
)

func TestApplyJournalReadableBySupervisorGroup(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("Unix file modes")
	}
	dir := t.TempDir()
	if err := writeApplyJournal(dir, applyJournal{}); err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(applyJournalPath(dir))
	if err != nil {
		t.Fatal(err)
	}
	if got := info.Mode().Perm(); got != 0640 {
		t.Fatalf("journal mode=%#o, want 0640", got)
	}
}

func TestRetryApplyRestoreOnlySnapshotConflict(t *testing.T) {
	conflict := errors.Join(storagebudget.ErrUnavailable, api.ErrConflict)
	attempts := 0
	if err := retryApplyRestore(func() error {
		attempts++
		if attempts < 3 {
			return conflict
		}
		return nil
	}); err != nil || attempts != 3 {
		t.Fatalf("snapshot restore retry: attempts=%d err=%v", attempts, err)
	}
	attempts = 0
	if err := retryApplyRestore(func() error { attempts++; return errors.New("not a snapshot conflict") }); err == nil || attempts != 1 {
		t.Fatalf("non-retryable restore: attempts=%d err=%v", attempts, err)
	}
}

func recoveryRevisions(t *testing.T, overrides map[string]string) (string, string, passport.Revision, string) {
	t.Helper()
	root := t.TempDir()
	dir := filepath.Join(root, "spaces", "alice")
	writeManagedSpaceDocs(t, dir, "alice", overrides)
	for path, body := range map[string]string{
		filepath.Join(dir, "secrets.dev.env"):    "",
		filepath.Join(dir, "config", "SOUL.md"):  "# fixture\n",
		filepath.Join(root, "config", "SOUL.md"): "# fixture\n",
	} {
		if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(body), 0600); err != nil {
			t.Fatal(err)
		}
	}
	oldID, err := passport.Current(dir)
	if err != nil {
		t.Fatal(err)
	}
	next := passport.Revision{ID: "r-0123456789abcdee", Provenance: passport.Provenance{Space: "alice", Environment: "dev"}, Docs: map[string][]byte{}}
	for _, name := range passport.Names {
		body, err := os.ReadFile(filepath.Join(dir, "generated", "passports", oldID, name+".yaml"))
		if err != nil {
			t.Fatal(err)
		}
		next.Docs[name] = []byte(strings.ReplaceAll(string(body), oldID, next.ID))
	}
	return root, dir, next, oldID
}

func TestApplyCrashRecoveryAtPublicationBoundaries(t *testing.T) {
	for _, boundary := range []string{"bundle", "pending", "view:stack", "view:config", "view:models", "view:workspace", "view:tools", "view:grants", "view:revision", "prepare", "render", "rights", "activate"} {
		t.Run(boundary, func(t *testing.T) {
			root, dir, next, oldID := recoveryRevisions(t, nil)
			previous := applyStep
			applyStep = func(step string) {
				if step == boundary {
					panic("synthetic process stop")
				}
			}
			t.Cleanup(func() { applyStep = previous })
			stopped := false
			func() {
				defer func() { stopped = recover() != nil }()
				if _, err := applyConfirm(context.Background(), dir, root, "dev", next, oldID, nil, reconcile.Handlers{}); err != nil {
					t.Fatalf("apply failed before %s: %v", boundary, err)
				}
			}()
			if !stopped {
				t.Fatalf("apply did not stop at %s", boundary)
			}
			applyStep = previous
			if err := recoverApply(context.Background(), dir, root, "dev", reconcile.Handlers{}); err != nil {
				t.Fatalf("recover %s: %v", boundary, err)
			}
			if err := recoverApply(context.Background(), dir, root, "dev", reconcile.Handlers{}); err != nil {
				t.Fatalf("repeat recover %s: %v", boundary, err)
			}
			if current, err := passport.Current(dir); err != nil || current != oldID {
				t.Fatalf("recovery current=%q err=%v, want %q", current, err, oldID)
			}
		})
	}
}

func TestApplyCrashRecoveryPreservesRevocation(t *testing.T) {
	root, dir, next, oldID := recoveryRevisions(t, map[string]string{
		"grants": "kind: grants\nallow: {workspace: [read]}\n",
		"tools":  "kind: tools\nbinding: {profile_id: alice-default, generation: 1}\ncore: {files: {via: toolhub}}\n",
	})
	next.Docs["tools"] = []byte(strings.ReplaceAll(string(next.Docs["tools"]), "alice-default", "alice-next"))
	now := time.Now().UTC()
	store := toolhub.NewStore()
	if err := store.RegisterDefinition(agenttools.HubToolsDefinition()); err != nil {
		t.Fatal(err)
	}
	right := api.RightRule{Capability: "workspace", Operation: "read"}
	policy := api.CapabilityPolicy{Schema: api.SchemaVersion, PolicyID: "alice-policy", Members: []string{"alice"}, Revision: 1,
		IssuedBy: "operator", IssuedAt: now, Reason: "fixture", Status: api.ActiveStatus, RightCeiling: []api.RightRule{right}}
	if err := api.Confirm(&policy, "operator", now); err != nil {
		t.Fatal(err)
	}
	if err := store.PutCapabilityPolicy(policy); err != nil {
		t.Fatal(err)
	}
	for _, spec := range []struct {
		id, env, revision string
		rights            []api.RightRule
	}{
		{"alice-default", "dev", oldID, []api.RightRule{right}},
		{"alice-next", "dev", "", nil},
		{"alice-prod", "prod", "r-prod", []api.RightRule{right}},
	} {
		profile := api.CapabilityProfile{Schema: api.SchemaVersion, ProfileID: spec.id, PrincipalID: "alice", ContextID: "alice", RuntimeID: "runtime",
			Environment: spec.env, Generation: 1, PolicyVersion: "policy-1", PassportRevision: spec.revision,
			PolicyID: policy.PolicyID, PolicyRevision: 1, Revision: 1, IssuedBy: "operator", IssuedAt: now, Reason: "fixture", Status: api.ActiveStatus, Rights: spec.rights}
		if err := api.Confirm(&profile, "operator", now); err != nil {
			t.Fatal(err)
		}
		if err := store.PutCapabilityProfile(profile); err != nil {
			t.Fatal(err)
		}
	}
	storePath := filepath.Join(dir, "runtime", "toolhub", "store.json")
	if err := store.Save(storePath); err != nil {
		t.Fatal(err)
	}
	docs, err := catalog.Generate()
	if err != nil {
		t.Fatal(err)
	}
	gov := toolhub.NewGovernanceStore(toolhub.GovernanceStorePath(storePath))
	grant := api.NewRightGrant("rightgrant-alice-recovery", "alice", "workspace", "read", "", "fixture", "operator", now.Add(time.Hour))
	if err := api.Confirm(&grant, "operator", now); err != nil {
		t.Fatal(err)
	}
	if err := gov.GrantRight(grant, docs.Rights, now); err != nil {
		t.Fatal(err)
	}
	previous := applyStep
	applyStep = func(step string) {
		if step == "binding" {
			panic("synthetic process stop after binding switch")
		}
	}
	t.Cleanup(func() { applyStep = previous })
	stopped := false
	func() {
		defer func() { stopped = recover() != nil }()
		if _, err := applyConfirm(context.Background(), dir, root, "dev", next, oldID, nil, reconcile.Handlers{}); err != nil {
			t.Fatal(err)
		}
	}()
	if !stopped {
		t.Fatal("apply did not stop after the binding switch")
	}
	applyStep = previous
	if err := gov.RevokeRight(grant.GrantID, "operator", "fixture", now.Add(time.Minute)); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 2; i++ {
		if err := recoverApply(context.Background(), dir, root, "dev", reconcile.Handlers{}); err != nil {
			t.Fatalf("recovery %d: %v", i, err)
		}
	}
	if current, err := passport.Current(dir); err != nil || current != oldID {
		t.Fatalf("current=%q err=%v", current, err)
	}
	profiles := latestRecoveryProfiles(t, storePath)
	if got := profiles["alice-default"]; got.Status != api.ActiveStatus || got.PassportRevision != oldID || len(got.Rights) != 0 {
		t.Fatalf("old binding restored a revoked right: %+v", got)
	}
	if got := profiles["alice-next"]; got.Status != api.DisabledStatus || len(got.Rights) != 0 {
		t.Fatalf("rejected binding remained active: %+v", got)
	}
	if got := profiles["alice-prod"]; got.Status != api.ActiveStatus || got.Revision != 1 || len(got.Rights) != 1 {
		t.Fatalf("sibling prod profile changed: %+v", got)
	}
	grants, err := gov.RightGrants()
	if err != nil || len(grants) != 1 || grants[0].Status != string(api.RevokedStatus) {
		t.Fatalf("concurrent revoke lost: %+v, %v", grants, err)
	}
	if _, err := applyConfirm(context.Background(), dir, root, "dev", next, oldID, nil, reconcile.Handlers{}); err == nil {
		t.Fatal("reapply silently published the disabled rejected binding")
	}
	if current, err := passport.Current(dir); err != nil || current != oldID {
		t.Fatalf("rejected reapply changed current=%q err=%v", current, err)
	}
}

func latestRecoveryProfiles(t *testing.T, path string) map[string]api.CapabilityProfile {
	t.Helper()
	body, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var snapshot struct {
		Changes []struct {
			Profile *api.CapabilityProfile `json:"profile"`
		} `json:"capability_changes"`
	}
	if err := json.Unmarshal(body, &snapshot); err != nil {
		t.Fatal(err)
	}
	profiles := map[string]api.CapabilityProfile{}
	for _, change := range snapshot.Changes {
		if change.Profile != nil {
			profiles[change.Profile.ProfileID] = *change.Profile
		}
	}
	return profiles
}

func TestApplyCrashRecoveryAfterFirstServiceEffect(t *testing.T) {
	root, dir, next, oldID := recoveryRevisions(t, nil)
	actions := []reconcile.Action{{Kind: reconcile.Recreate, Target: "model-relay"}}
	effects := 0
	handlers := reconcile.Handlers{Recreate: func(string) error {
		effects++
		panic("synthetic process stop after effect")
	}}
	stopped := false
	func() {
		defer func() { stopped = recover() != nil }()
		if _, err := applyConfirm(context.Background(), dir, root, "dev", next, oldID, actions, handlers); err != nil {
			t.Fatal(err)
		}
	}()
	if !stopped || effects != 1 {
		t.Fatalf("first effect did not stop process: stopped=%t effects=%d", stopped, effects)
	}
	if err := recoverApply(context.Background(), dir, root, "dev", reconcile.Handlers{Recreate: func(string) error { return errors.New("service unavailable") }}); err == nil {
		t.Fatal("incomplete service rollback reported success")
	}
	if _, err := passport.Current(dir); err == nil {
		t.Fatal("incomplete rollback left a readable current revision")
	}
	if _, err := os.Stat(applyJournalPath(dir)); err != nil {
		t.Fatal("failed recovery discarded its journal")
	}
	if err := recoverApply(context.Background(), dir, root, "dev", reconcile.Handlers{Recreate: func(string) error { effects++; return nil }}); err != nil {
		t.Fatal(err)
	}
	if effects != 2 {
		t.Fatalf("recovery did not replay the affected service: %d", effects)
	}
	if current, err := passport.Current(dir); err != nil || current != oldID {
		t.Fatalf("current=%q err=%v", current, err)
	}
	if err := recoverApply(context.Background(), dir, root, "dev", reconcile.Handlers{}); err != nil {
		t.Fatalf("repeat recovery: %v", err)
	}
}

func TestApplyCrashRecoveryFirstApplyRefusesUncertainService(t *testing.T) {
	root, dir, next, _ := recoveryRevisions(t, nil)
	if err := passport.RestoreCurrent(dir, "", "dev"); err != nil {
		t.Fatal(err)
	}
	actions := []reconcile.Action{{Kind: reconcile.Recreate, Target: "model-relay"}}
	stopped := false
	func() {
		defer func() { stopped = recover() != nil }()
		if _, err := applyConfirm(context.Background(), dir, root, "dev", next, "", actions,
			reconcile.Handlers{Recreate: func(string) error { panic("synthetic process stop") }}); err != nil {
			t.Fatal(err)
		}
	}()
	if !stopped {
		t.Fatal("first apply did not stop")
	}
	if err := recoverApply(context.Background(), dir, root, "dev", reconcile.Handlers{}); err == nil {
		t.Fatal("uncertain first apply service effect reported recovered")
	}
	if _, err := passport.Current(dir); err == nil {
		t.Fatal("uncertain first apply is readable")
	}
}

func TestApplyCrashRecoveryFirstApplyBeforeEffects(t *testing.T) {
	root, dir, next, _ := recoveryRevisions(t, nil)
	if err := passport.RestoreCurrent(dir, "", "dev"); err != nil {
		t.Fatal(err)
	}
	previous := applyStep
	applyStep = func(step string) {
		if step == "rights" {
			panic("synthetic process stop")
		}
	}
	t.Cleanup(func() { applyStep = previous })
	stopped := false
	func() {
		defer func() { stopped = recover() != nil }()
		if _, err := applyConfirm(context.Background(), dir, root, "dev", next, "", nil, reconcile.Handlers{}); err != nil {
			t.Fatal(err)
		}
	}()
	if !stopped {
		t.Fatal("first apply did not stop")
	}
	applyStep = previous
	for i := 0; i < 2; i++ {
		if err := recoverApply(context.Background(), dir, root, "dev", reconcile.Handlers{}); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := passport.Current(dir); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("first apply left a readable revision: %v", err)
	}
}

// A first apply that stops before activation must not strand the next apply:
// the candidate profile was never live, so recovery leaves it bound and
// active instead of disabling it (`bound profile unavailable` pathology
// seen during manual provisioning).
func TestApplyRecoveryKeepsNeverLiveCandidateProfile(t *testing.T) {
	root, dir, next, _ := recoveryRevisions(t, map[string]string{
		"tools": "kind: tools\nbinding: {profile_id: alice-next, generation: 1}\ncore: {files: {via: toolhub}}\n",
	})
	if err := passport.RestoreCurrent(dir, "", "dev"); err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	store := toolhub.NewStore()
	if err := store.RegisterDefinition(agenttools.HubToolsDefinition()); err != nil {
		t.Fatal(err)
	}
	policy := api.CapabilityPolicy{Schema: api.SchemaVersion, PolicyID: "alice-policy", Members: []string{"alice"}, Revision: 1,
		IssuedBy: "operator", IssuedAt: now, Reason: "fixture", Status: api.ActiveStatus}
	if err := api.Confirm(&policy, "operator", now); err != nil {
		t.Fatal(err)
	}
	if err := store.PutCapabilityPolicy(policy); err != nil {
		t.Fatal(err)
	}
	profile := api.CapabilityProfile{Schema: api.SchemaVersion, ProfileID: "alice-next", PrincipalID: "alice", ContextID: "alice", RuntimeID: "runtime",
		Environment: "dev", Generation: 1, PolicyVersion: "policy-1",
		PolicyID: policy.PolicyID, PolicyRevision: 1, Revision: 1, IssuedBy: "operator", IssuedAt: now, Reason: "fixture", Status: api.ActiveStatus}
	if err := api.Confirm(&profile, "operator", now); err != nil {
		t.Fatal(err)
	}
	if err := store.PutCapabilityProfile(profile); err != nil {
		t.Fatal(err)
	}
	storePath := filepath.Join(dir, "runtime", "toolhub", "store.json")
	if err := store.Save(storePath); err != nil {
		t.Fatal(err)
	}
	previous := applyStep
	applyStep = func(step string) {
		if step == "rights" {
			panic("synthetic process stop")
		}
	}
	t.Cleanup(func() { applyStep = previous })
	stopped := false
	func() {
		defer func() { stopped = recover() != nil }()
		if _, err := applyConfirm(context.Background(), dir, root, "dev", next, "", nil, reconcile.Handlers{}); err != nil {
			t.Fatal(err)
		}
	}()
	if !stopped {
		t.Fatal("first apply did not stop")
	}
	applyStep = previous
	if err := recoverApply(context.Background(), dir, root, "dev", reconcile.Handlers{}); err != nil {
		t.Fatal(err)
	}
	if got := latestRecoveryProfiles(t, storePath)["alice-next"]; got.Status != api.ActiveStatus {
		t.Fatalf("never-live candidate profile was disabled: %+v", got)
	}
}

func TestApplyRecoveryRejectsCorruptAffectedSet(t *testing.T) {
	root, dir, next, oldID := recoveryRevisions(t, nil)
	record := applyJournal{Environment: "dev", Previous: oldID, Revision: next.ID, PreviousProfile: "alice-default",
		NextProfile: "alice-default", Phase: "effects", Actions: []reconcile.Action{{Kind: reconcile.Recreate, Target: "../other-project"}}}
	if err := writeApplyJournal(dir, record); err != nil {
		t.Fatal(err)
	}
	called := false
	if err := recoverApply(context.Background(), dir, root, "dev", reconcile.Handlers{Recreate: func(string) error { called = true; return nil }}); err == nil || called {
		t.Fatalf("corrupt journal reached a service: called=%t err=%v", called, err)
	}
	if current, err := passport.Current(dir); err != nil || current != oldID {
		t.Fatalf("corrupt journal changed current=%q err=%v", current, err)
	}
}

func TestApplyRecoveryRefusesUncertainImageBuild(t *testing.T) {
	root, dir, next, oldID := recoveryRevisions(t, nil)
	record := applyJournal{Environment: "dev", Previous: oldID, Revision: next.ID, PreviousProfile: "alice-default",
		NextProfile: "alice-default", Phase: "effects", Actions: []reconcile.Action{{Kind: reconcile.Rebuild, Target: "model-relay"}}}
	if err := writeApplyJournal(dir, record); err != nil {
		t.Fatal(err)
	}
	if err := recoverApply(context.Background(), dir, root, "dev", reconcile.Handlers{}); err == nil {
		t.Fatal("uncertain image build reported recovered")
	}
	if _, err := passport.Current(dir); err == nil {
		t.Fatal("uncertain image build left a readable current revision")
	}
	if _, err := os.Stat(applyJournalPath(dir)); err != nil {
		t.Fatal("uncertain image build discarded its journal")
	}
}

func TestApplyRecoveryVerifiedImageBuildReplaysEffects(t *testing.T) {
	root, dir, next, oldID := recoveryRevisions(t, nil)
	record := applyJournal{Environment: "dev", Previous: oldID, Revision: next.ID, PreviousProfile: "alice-default",
		NextProfile: "alice-default", Phase: "effects", Actions: []reconcile.Action{
			{Kind: reconcile.Rebuild, Target: "model-relay"},
			{Kind: reconcile.Recreate, Target: "model-relay"},
		}}
	if err := writeApplyJournal(dir, record); err != nil {
		t.Fatal(err)
	}
	recreated := 0
	if err := recoverApplyWithRebuiltImage(context.Background(), dir, root, "dev", reconcile.Handlers{
		Recreate: func(string) error { recreated++; return nil },
	}, true); err != nil {
		t.Fatal(err)
	}
	if current, err := passport.Current(dir); err != nil || current != oldID || recreated != 1 {
		t.Fatalf("verified recovery current=%q recreate=%d err=%v", current, recreated, err)
	}
	if _, err := os.Stat(applyJournalPath(dir)); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("verified recovery retained journal: %v", err)
	}
}
