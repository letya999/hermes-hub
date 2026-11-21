package toolhub

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/letya999/hermes-hub/internal/identity"
	"github.com/letya999/hermes-hub/internal/toolhub/api"
	"github.com/letya999/hermes-hub/internal/vault"
)

// memoryCardAdmit enrolls one principal's memory_card selection end-to-end:
// binding, profile selection and issued memory.remember + tools.use rights —
// the shape a rendered managed space carries. Shared gov/doc let several
// principals ride one store.
func memoryCardAdmit(t *testing.T, store *Store, gov *GovernanceStore, doc api.RightsDoc,
	definition api.ToolDefinition, digest, user string, rememberRight bool) (identity.Envelope, api.CapabilitySelection) {
	t.Helper()
	auth := identity.TelegramEnvelope(user, 7, "runtime", "policy-1")
	auth.CapabilityProfile, auth.Environment, auth.Generation, auth.Organization = user+"-default", "dev", 1, "example"
	binding := api.ToolBinding{Schema: api.SchemaVersion, PrincipalID: auth.PrincipalID, ContextID: auth.ContextID,
		RuntimeID: auth.RuntimeID, DefinitionID: definition.DefinitionID, DefinitionVersion: definition.Version,
		PolicyVersion: auth.PolicyVersion, WorkloadClass: api.PerUser, Status: api.ActiveStatus, Revision: 1, ProjectionRevision: 1}
	binding.ToolBindingID = api.DeterministicBindingID(binding.PrincipalID, binding.ContextID, binding.RuntimeID, binding.DefinitionID, binding.DefinitionVersion, "", "")
	if err := store.PutBinding(binding); err != nil {
		t.Fatal(err)
	}
	selection := api.CapabilitySelection{CapabilityID: "memory.write", DefinitionID: definition.DefinitionID,
		DefinitionVersion: definition.Version, ImplementationDigest: digest, ToolName: "memory_card", Name: "memory_card"}
	profile := api.CapabilityProfile{Schema: api.SchemaVersion, ProfileID: auth.CapabilityProfile, PrincipalID: auth.PrincipalID,
		ContextID: auth.ContextID, RuntimeID: auth.RuntimeID, Environment: auth.Environment, Generation: auth.Generation,
		PolicyVersion: auth.PolicyVersion, PolicyID: "org-default", PolicyRevision: 2,
		PassportRevision: "r-0123456789abcdef",
		Revision:         1, IssuedBy: "operator", IssuedAt: time.Now().UTC(), Reason: "Reviewed synthetic fixture", Status: api.ActiveStatus,
		Selections: []api.CapabilitySelection{selection}}
	if err := api.Confirm(&profile, "", time.Unix(1700000000, 0).UTC()); err != nil {
		t.Fatal(err)
	}
	if err := store.PutCapabilityProfile(profile); err != nil {
		t.Fatal(err)
	}
	allow := map[string][]string{}
	grants := []struct{ capability, operation string }{{"tools", "use"}}
	if rememberRight {
		grants = append(grants, struct{ capability, operation string }{"memory", "remember"})
	}
	for _, grant := range grants {
		right := api.NewRightGrant(api.DeterministicID("rightgrant", user, grant.capability, grant.operation),
			user, grant.capability, grant.operation, "", "fixture", "platform", time.Now().Add(time.Hour))
		if err := api.Confirm(&right, right.GrantedBy, time.Now()); err != nil {
			t.Fatal(err)
		}
		if err := gov.GrantRight(right, doc, time.Now()); err != nil {
			t.Fatal(err)
		}
		allow[grant.capability] = append(allow[grant.capability], grant.operation)
	}
	if err := DeriveRights(store, gov, doc, user, allow, "dev", auth.CapabilityProfile, "policy-1", time.Now()); err != nil {
		t.Fatal(err)
	}
	return auth, selection
}

// memoryCardStore builds the store/policy/rights triple a two-user writer
// matrix needs — the real reviewed contract path, only the principals vary.
func memoryCardStore(t *testing.T, users ...string) (*Store, *GovernanceStore, api.RightsDoc, map[string]identity.Envelope, map[string]api.CapabilitySelection) {
	t.Helper()
	store := NewStore()
	definition := agentDefinition()
	definition.Tools = []api.ToolSpec{{
		Name: "memory_card", CapabilityID: "memory.write", Requires: []string{"memory.remember"},
		Effect: api.WriteEffect, Audience: api.AudienceModel, Executor: "runtime:toolhub",
		Description: "Memory write fixture.", Uses: []api.CapabilityUse{{Action: "write", Resource: "memory"}},
	}}
	if err := store.RegisterDefinition(definition); err != nil {
		t.Fatal(err)
	}
	digest := api.DefinitionDigest(definition)
	rule := api.CapabilityRule{CapabilityID: "memory.write", ImplementationDigest: digest,
		Action: "write", Resource: "memory", Limits: api.CapabilityLimits{OutputBytes: 8192, TimeoutSeconds: 10}}
	policy := api.CapabilityPolicy{Schema: api.SchemaVersion, PolicyID: "org-default", Organization: "example",
		Members: users, Revision: 2,
		IssuedBy: "operator", IssuedAt: time.Now().UTC(), Reason: "Reviewed synthetic fixture", Status: api.ActiveStatus,
		Ceiling: []api.CapabilityRule{rule}, Defaults: []api.CapabilityRule{rule},
		RightCeiling: []api.RightRule{{Capability: "tools", Operation: "use"}, {Capability: "memory", Operation: "remember"}}}
	if err := api.Confirm(&policy, "", time.Unix(1700000000, 0).UTC()); err != nil {
		t.Fatal(err)
	}
	if err := store.PutCapabilityPolicy(policy); err != nil {
		t.Fatal(err)
	}
	doc := api.RightsDoc{Release: "fixture", DefinitionID: "hub-agent-rights", Capabilities: map[string]api.RightCapability{
		"tools":  {Operations: map[string]api.RightOperation{"use": {Subject: "self", Scope: "none", Issuers: []string{"platform"}, PEP: []string{"test"}, Effect: "read", Description: "fixture"}}},
		"memory": {Operations: map[string]api.RightOperation{"remember": {Subject: "self", Scope: "none", Issuers: []string{"platform"}, PEP: []string{"test"}, Effect: "write", Description: "fixture"}}},
	}}
	store.RightsDoc = doc
	gov := NewGovernanceStore(filepath.Join(t.TempDir(), "governance.json"))
	store.RightsLedger = gov.Load
	auths, selections := map[string]identity.Envelope{}, map[string]api.CapabilitySelection{}
	for _, user := range users {
		auth, selection := memoryCardAdmit(t, store, gov, doc, definition, digest, user, true)
		auths[user], selections[user] = auth, selection
	}
	return store, gov, doc, auths, selections
}

// cardTripwire fails if a memory_card dispatch ever reaches the configured
// agent backend — the writer is ToolHub-owned, never in-runtime.
type cardTripwire struct {
	t     *testing.T
	calls int
}

func (b *cardTripwire) Call(context.Context, api.EffectiveBinding, api.ToolSpec, map[string]any) (api.BackendResult, error) {
	b.calls++
	b.t.Error("memory_card reached the in-runtime agent backend — ToolHub must execute it")
	return api.BackendResult{}, api.ErrIsolation
}

// memoryCardEnv wires a writer gateway for the enrolled users: real vault
// bindings + passports, a fake lifecycle authority carrying each user's live
// private run, the durable audit fence and the backend tripwire.
func memoryCardEnv(t *testing.T, store *Store, passports, vaults string, auths map[string]identity.Envelope, extra map[string]api.RunContext) (*Gateway, *cardTripwire) {
	t.Helper()
	resolver, err := NewVaultResolver(vaults, dirTreeReader{root: passports})
	if err != nil {
		t.Fatal(err)
	}
	records := map[string]api.RunContext{}
	for user, env := range auths {
		record := api.RunContext{JobID: "job-" + user, RunID: "run-" + user + "-1", SessionID: "session-1",
			RuntimeGeneration: "gen-3", CurrentGeneration: "gen-3",
			Environment: env.Environment, Status: "running",
			ScopeID: "user:" + env.PrincipalID, Trigger: "message", Channel: "telegram_bot",
			Envelope: identity.Envelope{Schema: env.Schema, PrincipalID: env.PrincipalID,
				ContextID: env.ContextID, RuntimeID: env.RuntimeID, PolicyVersion: env.PolicyVersion}}
		records[record.JobID] = record
	}
	for id, record := range extra {
		records[id] = record
	}
	tripwire := &cardTripwire{t: t}
	g := &Gateway{Store: store, Spawn: dirTreeReader{root: passports}, Vaults: resolver,
		authorizeAssignmentRun: func(auth identity.Envelope, id string, revision uint64) error {
			if auth.PrincipalID != "alice" || id != "asg-owner" || revision != 1 {
				return api.ErrUnauthorized
			}
			return nil
		},
		AuditWrite: func(string, map[string]string) error { return nil },
		Runs: runAuthorityFunc(func(_ context.Context, jobID string) (api.RunContext, error) {
			record, ok := records[jobID]
			if !ok {
				return api.RunContext{}, errors.New("unknown job")
			}
			return record, nil
		}),
		Backend: tripwire}
	return g, tripwire
}

func memoryCardCall(t *testing.T, g *Gateway, auth identity.Envelope, jobID, runID string, args map[string]any) (map[string]any, error) {
	t.Helper()
	ctx := api.WithCallCorrelation(t.Context(), api.HeaderCorrelation(jobID, runID))
	res, err := g.CallAuthorized(ctx, auth, "memory_card", args)
	if err != nil {
		return nil, err
	}
	if res == nil {
		return nil, errors.New("nil result")
	}
	out, _ := res.StructuredContent.(map[string]any)
	if out == nil && res.IsError && len(res.Content) > 0 {
		return nil, errors.New("call errored")
	}
	return out, nil
}

func TestAssignmentMemoryCardRememberAndRevoke(t *testing.T) {
	store, gov, _, auths, _ := memoryCardStore(t, "alice", "bob")
	passports, vaults := t.TempDir(), t.TempDir()
	for _, user := range []string{"alice", "bob"} {
		writeMemoryPassportEnv(t, passports, user, "dev", true)
		if err := vault.Ensure(filepath.Join(vaults, user)); err != nil {
			t.Fatal(err)
		}
	}
	auth := auths["alice"]
	assignment := api.RunContext{JobID: "job-assignment", RunID: "run-assignment-1", SessionID: "session-1",
		RuntimeGeneration: "gen-3", CurrentGeneration: "gen-3", Environment: "dev", Status: "running",
		ScopeID: "user:alice", Trigger: "cron", Assignment: true, UserID: "alice",
		AssignmentID: "asg-owner", AssignmentPlanRevision: 1,
		Envelope: identity.Envelope{Schema: auth.Schema, PrincipalID: auth.PrincipalID, ContextID: auth.ContextID,
			RuntimeID: auth.RuntimeID, PolicyVersion: auth.PolicyVersion}}
	g, tripwire := memoryCardEnv(t, store, passports, vaults, auths, map[string]api.RunContext{assignment.JobID: assignment})
	if out, err := memoryCardCall(t, g, auth, assignment.JobID, assignment.RunID,
		map[string]any{"op": "remember", "name": "Assignment Fact", "text": "owner confirmed synthetic assignment fact"}); err != nil || out["status"] != "ok" {
		t.Fatalf("active assignment remember: %v %v", out, err)
	}
	card := filepath.Join(vaults, "alice", "memory", "cards", "notes", "inbox", "assignment-fact.md")
	if body, err := os.ReadFile(card); err != nil || !strings.Contains(string(body), "synthetic assignment fact") {
		t.Fatalf("assignment fact missing from owner vault: %v", err)
	}
	if err := gov.RevokeRight(api.DeterministicID("rightgrant", "alice", "memory", "remember"), "operator", "fixture revoke", time.Now()); err != nil {
		t.Fatal(err)
	}
	if _, err := memoryCardCall(t, g, auth, assignment.JobID, assignment.RunID,
		map[string]any{"op": "remember", "name": "Revoked Assignment Fact", "text": "must never land"}); err == nil {
		t.Fatal("revoked assignment still wrote memory")
	}
	if _, err := os.Stat(filepath.Join(vaults, "alice", "memory", "cards", "notes", "inbox", "revoked-assignment-fact.md")); !os.IsNotExist(err) {
		t.Fatalf("revoked assignment changed vault: %v", err)
	}
	if tripwire.calls != 0 {
		t.Fatal("assignment writer escaped to the agent backend")
	}
}

// TestMemoryCardWriterRoundTrip is the #518 contract: the cataloged
// memory_card executes inside ToolHub — exact private-run admission, the
// verified envelope's vault, the governed writer and the journaled audit —
// never the in-runtime backend or a caller-chosen path.
func TestMemoryCardWriterRoundTrip(t *testing.T) {
	store, _, _, auths, _ := memoryCardStore(t, "alice")
	passports, vaults := t.TempDir(), t.TempDir()
	writeMemoryPassportEnv(t, passports, "alice", "dev", true)
	if err := vault.Ensure(filepath.Join(vaults, "alice")); err != nil {
		t.Fatal(err)
	}
	g, tripwire := memoryCardEnv(t, store, passports, vaults, auths, nil)
	alice := auths["alice"]

	out, err := memoryCardCall(t, g, alice, "job-alice", "run-alice-1",
		map[string]any{"op": "remember", "text": "we picked the earl grey supplier", "name": "Tea Supplier", "tags": []any{"tea", "vendors"}})
	if err != nil {
		t.Fatalf("remember errored: %v", err)
	}
	if out["status"] != "ok" {
		t.Fatalf("remember status %v", out)
	}
	rel, _ := out["path"].(string)
	if !strings.HasPrefix(rel, "memory/cards/notes/inbox/") || !strings.HasSuffix(rel, "tea-supplier.md") {
		t.Fatalf("remember path %q", rel)
	}
	body, err := os.ReadFile(filepath.Join(vaults, "alice", filepath.FromSlash(rel)))
	if err != nil {
		t.Fatalf("remembered card missing: %v", err)
	}
	if !strings.Contains(string(body), "earl grey supplier") || !strings.Contains(string(body), "tags: [tea, vendors]") {
		t.Fatalf("card body missing the remembered line or tags: %s", body)
	}
	if tripwire.calls != 0 {
		t.Fatalf("writer dispatched to the in-runtime backend %d times", tripwire.calls)
	}
	// The journal binds the mutation to the authorizing run.
	journal, err := os.ReadFile(filepath.Join(vaults, "alice", "runtime", "memory", "writes.jsonl"))
	if err != nil {
		t.Fatalf("write journal missing: %v", err)
	}
	var row struct {
		Op        string `json:"op"`
		Status    string `json:"status"`
		SourceRef string `json:"source_ref"`
		RequestID string `json:"request_id"`
	}
	if err := json.Unmarshal([]byte(strings.TrimSpace(string(journal))), &row); err != nil {
		t.Fatalf("journal row malformed: %v", err)
	}
	if row.Op != "remember" || row.Status != "ok" || row.SourceRef != "job-alice/run-alice-1" || row.RequestID == "" {
		t.Fatalf("journal row = %+v", row)
	}

	// An unnamed remember derives its slug from the text itself.
	out, err = memoryCardCall(t, g, alice, "job-alice", "run-alice-1",
		map[string]any{"op": "remember", "text": "Oat Milk Latte Ratio Tweaked Again"})
	if err != nil || out["status"] != "ok" {
		t.Fatalf("unnamed remember: %v %v", out, err)
	}
	rel, _ = out["path"].(string)
	if !strings.HasPrefix(rel, "memory/cards/notes/inbox/oat-milk-latte-ratio") {
		t.Fatalf("text-derived slug: %q", rel)
	}
	// A repeat remember on the same slug appends a second dated line.
	out, err = memoryCardCall(t, g, alice, "job-alice", "run-alice-1",
		map[string]any{"op": "remember", "text": "delivery moved to fridays", "name": "Tea Supplier"})
	if err != nil || out["status"] != "ok" {
		t.Fatalf("repeat remember: %v %v", out, err)
	}
	rel, _ = out["path"].(string)
	body, _ = os.ReadFile(filepath.Join(vaults, "alice", filepath.FromSlash(rel)))
	if !strings.Contains(string(body), "earl grey supplier") || !strings.Contains(string(body), "delivery moved to fridays") {
		t.Fatalf("append lost or missed a line: %s", body)
	}
	if got := strings.Count(string(body), "\n- "); got != 2 {
		t.Fatalf("append produced %d lines, want 2: %s", got, body)
	}
	// Text without latin characters still lands under a deterministic slug.
	out, err = memoryCardCall(t, g, alice, "job-alice", "run-alice-1",
		map[string]any{"op": "remember", "text": "купили чай оптом"})
	if err != nil || out["status"] != "ok" {
		t.Fatalf("cyrillic remember: %v %v", out, err)
	}
	rel, _ = out["path"].(string)
	if !strings.HasPrefix(rel, "memory/cards/notes/inbox/note-") {
		t.Fatalf("hash-fallback slug: %q", rel)
	}
}

func TestAssignmentRememberUsesCurrentRight(t *testing.T) {
	store, gov, _, auths, _ := memoryCardStore(t, "alice")
	passports, vaults := t.TempDir(), t.TempDir()
	writeMemoryPassportEnv(t, passports, "alice", "dev", true)
	if err := vault.Ensure(filepath.Join(vaults, "alice")); err != nil {
		t.Fatal(err)
	}
	alice := auths["alice"]
	record := api.RunContext{JobID: "job-assignment", RunID: "run-assignment", RuntimeGeneration: "gen-3", CurrentGeneration: "gen-3",
		Environment: alice.Environment, Status: "running", ScopeID: "user:alice", Trigger: "cron", Assignment: true, UserID: "alice",
		AssignmentID: "asg-owner", AssignmentPlanRevision: 1,
		Envelope: identity.Envelope{Schema: alice.Schema, PrincipalID: alice.PrincipalID, ContextID: alice.ContextID, RuntimeID: alice.RuntimeID, PolicyVersion: alice.PolicyVersion}}
	g, _ := memoryCardEnv(t, store, passports, vaults, auths, map[string]api.RunContext{record.JobID: record})
	args := map[string]any{"op": "remember", "text": "assignment found a blue token", "name": "Assignment Token"}
	if out, err := memoryCardCall(t, g, alice, record.JobID, record.RunID, args); err != nil || out["status"] != "ok" {
		t.Fatalf("owner assignment remember: out=%v err=%v", out, err)
	}
	if err := gov.RevokeRight(api.DeterministicID("rightgrant", "alice", "memory", "remember"), "operator", "fixture revoke", time.Now()); err != nil {
		t.Fatal(err)
	}
	if out, err := memoryCardCall(t, g, alice, record.JobID, record.RunID,
		map[string]any{"op": "remember", "text": "must not land", "name": "Revoked Assignment Token"}); err == nil || out != nil {
		t.Fatalf("revoked assignment wrote memory: out=%v err=%v", out, err)
	}
	if _, err := os.Stat(filepath.Join(vaults, "alice", "memory", "cards", "notes", "inbox", "revoked-assignment-token.md")); !os.IsNotExist(err) {
		t.Fatalf("revoked assignment card exists: %v", err)
	}
}

// TestMemoryCardFactAndProposeGate drives the additive op and the D6 gate:
// fact appends dated evidence lines directly, while truth/merge/forget —
// edits to existing memory — answer invalid and point at propose, which is
// the only mutation path a verified owner confirmation opens.
func TestMemoryCardFactAndProposeGate(t *testing.T) {
	store, _, _, auths, _ := memoryCardStore(t, "alice")
	passports, vaults := t.TempDir(), t.TempDir()
	writeMemoryPassportEnv(t, passports, "alice", "dev", true)
	if err := vault.Ensure(filepath.Join(vaults, "alice")); err != nil {
		t.Fatal(err)
	}
	g, _ := memoryCardEnv(t, store, passports, vaults, auths, nil)
	alice := auths["alice"]

	// fact appends a dated evidence line and creates the named card.
	out, err := memoryCardCall(t, g, alice, "job-alice", "run-alice-1",
		map[string]any{"op": "fact", "card": "suppliers", "text": "earl grey from twinings", "evidence": "turn-9"})
	if err != nil || out["status"] != "ok" {
		t.Fatalf("fact: %v %v", out, err)
	}
	card := filepath.Join(vaults, "alice", "memory", "cards", "notes", "suppliers.md")
	cardBody, err := os.ReadFile(card)
	if err != nil || !strings.Contains(string(cardBody), "twinings") || !strings.Contains(string(cardBody), "turn-9") {
		t.Fatalf("fact card: %v %s", err, cardBody)
	}
	// Edits to existing memory are proposal-gated (ADR-0095 D6): direct
	// truth/merge/forget return the corrected call shape and write nothing.
	for _, args := range []map[string]any{
		{"op": "truth", "card": "suppliers", "text": "Twinings is the earl grey supplier."},
		{"op": "merge", "card": "suppliers", "to": "vendors"},
		{"op": "forget", "id": "turn-9"},
		{"op": "forget", "card": "suppliers"},
	} {
		out, err = memoryCardCall(t, g, alice, "job-alice", "run-alice-1", args)
		if err != nil || out["status"] != "invalid" {
			t.Fatalf("direct %v must be invalid: %v %v", args["op"], out, err)
		}
	}
	cardBody, _ = os.ReadFile(card)
	if strings.Contains(string(cardBody), "Twinings is the earl grey supplier.") || strings.Contains(string(cardBody), "status: superseded") {
		t.Fatalf("direct edit mutated the card: %s", cardBody)
	}
	if _, err := os.Stat(filepath.Join(vaults, "alice", "runtime", "memory", "forget.jsonl")); !os.IsNotExist(err) {
		t.Fatalf("direct forget wrote the strike sidecar: %v", err)
	}
}

// TestMemoryCardRetryConverges is the retry-safe contract: a transport or
// model retry reissues the identical call under the same job, deriving the
// same request_id — the journaled result is returned and no second write
// lands. A fresh ToolCallID never defeats the dedupe.
func TestMemoryCardRetryConverges(t *testing.T) {
	store, _, _, auths, _ := memoryCardStore(t, "alice")
	passports, vaults := t.TempDir(), t.TempDir()
	writeMemoryPassportEnv(t, passports, "alice", "dev", true)
	if err := vault.Ensure(filepath.Join(vaults, "alice")); err != nil {
		t.Fatal(err)
	}
	g, _ := memoryCardEnv(t, store, passports, vaults, auths, nil)
	alice := auths["alice"]

	args := map[string]any{"op": "remember", "text": "earl grey supplier chosen", "name": "Tea Supplier"}
	out, err := memoryCardCall(t, g, alice, "job-alice", "run-alice-1", args)
	if err != nil || out["status"] != "ok" {
		t.Fatalf("first call: %v %v", out, err)
	}
	rel, _ := out["path"].(string)
	// Identical retry — new ToolCallID, same job/op/arguments.
	retry, err := memoryCardCall(t, g, alice, "job-alice", "run-alice-1", args)
	if err != nil || retry["status"] != "ok" {
		t.Fatalf("retry: %v %v", retry, err)
	}
	if retry["path"] != rel {
		t.Fatalf("retry diverged: %v vs %v", retry["path"], rel)
	}
	body, err := os.ReadFile(filepath.Join(vaults, "alice", filepath.FromSlash(rel)))
	if err != nil {
		t.Fatal(err)
	}
	if got := strings.Count(string(body), "\n- "); got != 1 {
		t.Fatalf("retry double-wrote: %d lines in %s", got, body)
	}
	// A genuinely different call under the same job is not deduped.
	out, err = memoryCardCall(t, g, alice, "job-alice", "run-alice-1",
		map[string]any{"op": "remember", "text": "delivery moved to fridays", "name": "Tea Supplier"})
	if err != nil || out["status"] != "ok" {
		t.Fatalf("distinct call deduped: %v %v", out, err)
	}
	body, _ = os.ReadFile(filepath.Join(vaults, "alice", filepath.FromSlash(rel)))
	if got := strings.Count(string(body), "\n- "); got != 2 {
		t.Fatalf("distinct call lost: %d lines in %s", got, body)
	}
}

func TestAuditRetryAfterJournalFailureDoesNotDuplicate(t *testing.T) {
	store, _, _, auths, _ := memoryCardStore(t, "alice")
	passports, vaults := t.TempDir(), t.TempDir()
	writeMemoryPassportEnv(t, passports, "alice", "dev", true)
	root := filepath.Join(vaults, "alice")
	if err := vault.Ensure(root); err != nil {
		t.Fatal(err)
	}
	g, _ := memoryCardEnv(t, store, passports, vaults, auths, nil)
	args := map[string]any{"op": "fact", "card": "suppliers", "text": "earl grey supplier chosen"}
	journal := filepath.Join(root, "runtime", "memory", "writes.jsonl")
	if err := os.MkdirAll(journal, 0o700); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 2; i++ {
		if _, err := memoryCardCall(t, g, auths["alice"], "job-alice", "run-alice-1", args); err == nil {
			t.Fatal("unavailable result journal reported success")
		}
	}
	card := filepath.Join(root, "memory", "cards", "notes", "suppliers.md")
	body, err := os.ReadFile(card)
	if err != nil || strings.Count(string(body), "earl grey supplier chosen") != 1 {
		t.Fatalf("journal retry changed card twice: %v %q", err, body)
	}
	if err := os.Remove(journal); err != nil {
		t.Fatal(err)
	}
	restarted, _ := memoryCardEnv(t, store, passports, vaults, auths, nil)
	if out, err := memoryCardCall(t, restarted, auths["alice"], "job-alice", "run-alice-1", args); err != nil || out["status"] != "ok" {
		t.Fatalf("restart did not finish journal: %v %v", out, err)
	}
	body, err = os.ReadFile(card)
	if err != nil || strings.Count(string(body), "earl grey supplier chosen") != 1 {
		t.Fatalf("restart repeated effect: %v %q", err, body)
	}
}

func TestMemoryCardIntentAndCommitRecovery(t *testing.T) {
	store, _, _, auths, _ := memoryCardStore(t, "alice")
	passports, vaults := t.TempDir(), t.TempDir()
	writeMemoryPassportEnv(t, passports, "alice", "dev", true)
	root := filepath.Join(vaults, "alice")
	if err := vault.Ensure(root); err != nil {
		t.Fatal(err)
	}
	g, _ := memoryCardEnv(t, store, passports, vaults, auths, nil)
	args := map[string]any{"op": "fact", "card": "suppliers", "text": "durable intent fact"}
	card := filepath.Join(root, "memory", "cards", "notes", "suppliers.md")
	requests := filepath.Join(root, "runtime", "memory", "requests")
	if err := os.MkdirAll(filepath.Dir(requests), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(requests, []byte("blocked"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := memoryCardCall(t, g, auths["alice"], "job-alice", "run-alice-1", args); err == nil {
		t.Fatal("intent persistence failure reported success")
	}
	if _, err := os.Stat(card); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("intent failure mutated card: %v", err)
	}
	if err := os.Remove(requests); err != nil {
		t.Fatal(err)
	}
	lock := filepath.Join(root, ".git", "index.lock")
	if err := os.WriteFile(lock, []byte("blocked"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := memoryCardCall(t, g, auths["alice"], "job-alice", "run-alice-1", args); err == nil {
		t.Fatal("git commit failure reported success")
	}
	body, err := os.ReadFile(card)
	if err != nil || strings.Count(string(body), "durable intent fact") != 1 {
		t.Fatalf("write-before-commit result: %v %q", err, body)
	}
	if err := os.Remove(lock); err != nil {
		t.Fatal(err)
	}
	restarted, _ := memoryCardEnv(t, store, passports, vaults, auths, nil)
	if out, err := memoryCardCall(t, restarted, auths["alice"], "job-alice", "run-alice-1", args); err != nil || out["status"] != "ok" {
		t.Fatalf("restart did not finish write: %v %v", out, err)
	}
	body, err = os.ReadFile(card)
	if err != nil || strings.Count(string(body), "durable intent fact") != 1 {
		t.Fatalf("restart repeated effect: %v %q", err, body)
	}
	status, err := exec.Command("git", "-C", root, "status", "--porcelain", "--", "memory/cards/notes/suppliers.md").CombinedOutput()
	if err != nil || len(status) != 0 {
		t.Fatalf("recovered write lacks clean git commit: %v %q", err, status)
	}
}

// TestMemoryCardDisableBlocksCommit is the fail-closed write gate: memory is
// re-checked immediately before commit, so a republished disable that lands
// between admission and the vault mutation prevents the write — the card
// never appears and the journal stays silent for it.
func TestMemoryCardDisableBlocksCommit(t *testing.T) {
	store, _, _, auths, _ := memoryCardStore(t, "alice")
	passports, vaults := t.TempDir(), t.TempDir()
	writeMemoryPassportEnv(t, passports, "alice", "dev", true)
	if err := vault.Ensure(filepath.Join(vaults, "alice")); err != nil {
		t.Fatal(err)
	}
	g, _ := memoryCardEnv(t, store, passports, vaults, auths, nil)
	alice := auths["alice"]
	deniedAudited := false
	g.AuditWrite = func(event string, fields map[string]string) error {
		if (event == "reject" || event == "deny") && fields["job_id"] == "job-alice" && fields["hermes_run_id"] == "run-alice-1" {
			deniedAudited = true
		}
		return nil
	}

	u, err := g.Vaults.Resolve("alice", "dev")
	if err != nil {
		t.Fatal(err)
	}
	// Hold alice's vault lock: the in-flight call passes preflight and the
	// resolver's enrollment check while memory is still enabled, then blocks
	// at the per-user serialization point — where the disable lands.
	held, release, done := make(chan struct{}), make(chan struct{}), make(chan struct{})
	go func() {
		defer close(done)
		_ = u.Locked(func(string) error {
			close(held)
			<-release
			writeMemoryPassportEnv(t, passports, "alice", "dev", false)
			return nil
		})
	}()
	<-held
	result := make(chan error, 1)
	go func() {
		out, err := memoryCardCall(t, g, alice, "job-alice", "run-alice-1",
			map[string]any{"op": "remember", "text": "must never land", "name": "Revoked Note"})
		if !errors.Is(err, api.ErrUnauthorized) {
			result <- fmt.Errorf("disabled write: %v %v", out, err)
			return
		}
		result <- nil
	}()
	// The call cannot pass the held lock; give it time to run admission and
	// the pre-lock enrollment check while the passport still reads enabled.
	time.Sleep(200 * time.Millisecond)
	close(release)
	<-done
	if err := <-result; err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(vaults, "alice", "memory", "cards", "notes", "inbox", "revoked-note.md")); !os.IsNotExist(err) {
		t.Fatalf("disabled memory still wrote the card: %v", err)
	}
	journal, _ := os.ReadFile(filepath.Join(vaults, "alice", "runtime", "memory", "writes.jsonl"))
	if strings.Contains(string(journal), "must never land") {
		t.Fatalf("journaled a write the disable should have blocked: %s", journal)
	}
	if len(journal) != 0 || !deniedAudited {
		t.Fatal("pre-dispatch disable must leave vault journal unchanged and audit the rejected run")
	}
}

// TestMemoryCardRevokedRightBlocksCommit is the rights-freshness gate: the
// write re-verifies the admitted authority at the commit point, so the
// memory.remember grant revoked between admission and the vault mutation
// prevents the write.
func TestMemoryCardRevokedRightBlocksCommit(t *testing.T) {
	store, gov, _, auths, _ := memoryCardStore(t, "alice")
	passports, vaults := t.TempDir(), t.TempDir()
	writeMemoryPassportEnv(t, passports, "alice", "dev", true)
	if err := vault.Ensure(filepath.Join(vaults, "alice")); err != nil {
		t.Fatal(err)
	}
	g, _ := memoryCardEnv(t, store, passports, vaults, auths, nil)
	alice := auths["alice"]
	deniedAudited := false
	g.AuditWrite = func(event string, fields map[string]string) error {
		if (event == "reject" || event == "deny") && fields["job_id"] == "job-alice" && fields["hermes_run_id"] == "run-alice-1" {
			deniedAudited = true
		}
		return nil
	}

	u, err := g.Vaults.Resolve("alice", "dev")
	if err != nil {
		t.Fatal(err)
	}
	grantID := api.DeterministicID("rightgrant", "alice", "memory", "remember")
	held, release, done := make(chan struct{}), make(chan struct{}), make(chan struct{})
	go func() {
		defer close(done)
		_ = u.Locked(func(string) error {
			close(held)
			<-release
			if err := gov.RevokeRight(grantID, "operator", "fixture revoke", time.Now()); err != nil {
				t.Errorf("revoke right: %v", err)
			}
			return nil
		})
	}()
	<-held
	result := make(chan error, 1)
	go func() {
		out, err := memoryCardCall(t, g, alice, "job-alice", "run-alice-1",
			map[string]any{"op": "remember", "text": "must never land", "name": "Revoked Note"})
		if !errors.Is(err, api.ErrUnauthorized) && !errors.Is(err, api.ErrNotFound) {
			result <- fmt.Errorf("revoked write: %v %v", out, err)
			return
		}
		result <- nil
	}()
	time.Sleep(200 * time.Millisecond)
	close(release)
	<-done
	if err := <-result; err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(vaults, "alice", "memory", "cards", "notes", "inbox", "revoked-note.md")); !os.IsNotExist(err) {
		t.Fatalf("revoked right still wrote the card: %v", err)
	}
	journal, _ := os.ReadFile(filepath.Join(vaults, "alice", "runtime", "memory", "writes.jsonl"))
	if len(journal) != 0 || !deniedAudited {
		t.Fatal("pre-dispatch revoke must leave vault journal unchanged and audit the rejected run")
	}
}

// TestMemoryCardTwoUserIsolation proves the writer can never cross vaults:
// alice and bob write in parallel, each call resolves only its own vault —
// bob's text never lands in alice's tree and vice versa.
func TestMemoryCardTwoUserIsolation(t *testing.T) {
	store, _, _, auths, _ := memoryCardStore(t, "alice", "bob")
	passports, vaults := t.TempDir(), t.TempDir()
	for _, user := range []string{"alice", "bob"} {
		writeMemoryPassportEnv(t, passports, user, "dev", true)
		if err := vault.Ensure(filepath.Join(vaults, user)); err != nil {
			t.Fatal(err)
		}
	}
	g, _ := memoryCardEnv(t, store, passports, vaults, auths, nil)

	var wg sync.WaitGroup
	results := make([]map[string]any, 2)
	errs := make([]error, 2)
	users := []string{"alice", "bob"}
	texts := []string{"alice earl grey supplier", "bob decaf roaster"}
	names := []string{"Alice Suppliers", "Bob Roasters"}
	for i, user := range users {
		wg.Add(1)
		go func(i int, user string) {
			defer wg.Done()
			results[i], errs[i] = memoryCardCall(t, g, auths[user], "job-"+user, "run-"+user+"-1",
				map[string]any{"op": "remember", "text": texts[i], "name": names[i]})
		}(i, user)
	}
	wg.Wait()
	for i, user := range users {
		if errs[i] != nil || results[i]["status"] != "ok" {
			t.Fatalf("%s write: %v %v", user, results[i], errs[i])
		}
		rel, _ := results[i]["path"].(string)
		body, err := os.ReadFile(filepath.Join(vaults, user, filepath.FromSlash(rel)))
		if err != nil || !strings.Contains(string(body), texts[i]) {
			t.Fatalf("%s vault missing own line: %v %s", user, err, body)
		}
		other := users[1-i]
		if strings.Contains(string(body), texts[1-i]) || strings.HasPrefix(rel, "memory/cards/notes/inbox/"+names[1-i]) {
			t.Fatalf("%s vault carries %s's write: %s", user, other, body)
		}
		if _, err := os.Stat(filepath.Join(vaults, other, filepath.FromSlash(rel))); err == nil {
			t.Fatalf("%s's card appeared in %s's vault: %s", user, other, rel)
		}
	}
}

// TestMemoryCardInvalid is the honest-shape contract: malformed calls answer
// status=invalid with field errors instead of a transport error — the model
// gets the corrected shape back, nothing is written.
func TestMemoryCardInvalid(t *testing.T) {
	store, _, _, auths, _ := memoryCardStore(t, "alice")
	passports, vaults := t.TempDir(), t.TempDir()
	writeMemoryPassportEnv(t, passports, "alice", "dev", true)
	if err := vault.Ensure(filepath.Join(vaults, "alice")); err != nil {
		t.Fatal(err)
	}
	g, _ := memoryCardEnv(t, store, passports, vaults, auths, nil)
	alice := auths["alice"]

	out, err := memoryCardCall(t, g, alice, "job-alice", "run-alice-1",
		map[string]any{"op": "fact", "text": "no card named"})
	if err != nil || out["status"] != "invalid" {
		t.Fatalf("fact without card: %v %v", out, err)
	}
	if errs, _ := out["errors"].([]any); len(errs) == 0 {
		t.Fatalf("invalid carries no field errors: %v", out)
	}
	out, err = memoryCardCall(t, g, alice, "job-alice", "run-alice-1",
		map[string]any{"op": "remember"})
	if err != nil || out["status"] != "invalid" {
		t.Fatalf("remember without text: %v %v", out, err)
	}
	out, err = memoryCardCall(t, g, alice, "job-alice", "run-alice-1",
		map[string]any{"op": "wipe"})
	if err != nil || out["status"] != "invalid" {
		t.Fatalf("unknown op: %v %v", out, err)
	}
	var cards int
	_ = filepath.WalkDir(filepath.Join(vaults, "alice", "memory", "cards"), func(_ string, d os.DirEntry, err error) error {
		if err == nil && !d.IsDir() {
			cards++
		}
		return nil
	})
	if cards != 0 {
		t.Fatalf("invalid call wrote %d cards", cards)
	}
}
