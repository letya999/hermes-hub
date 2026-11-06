package toolhub

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/letya999/hermes-hub/internal/identity"
	"github.com/letya999/hermes-hub/internal/toolhub/api"
	"github.com/letya999/hermes-hub/internal/vault"
)

// proposalEnv extends the writer gateway with the control surface the
// confirmation endpoint needs: a control token, a control plane clock and
// the enrolled-actor token map the proof validates against.
func proposalEnv(t *testing.T, g *Gateway, auths map[string]identity.Envelope) {
	t.Helper()
	g.ControlToken = "ctl-secret"
	g.Control = &ControlPlane{Store: g.Store}
	g.Tokens = map[string]identity.Envelope{}
	for user, auth := range auths {
		g.Tokens["tok-"+user] = auth
	}
}

// confirmProposal posts one HumanConfirmation proof through the real
// /v1/confirmations handler — the same bytes the communication callback
// forwards after a verified owner tap.
func confirmProposal(t *testing.T, g *Gateway, token string, proof api.HumanConfirmation) int {
	t.Helper()
	body, err := json.Marshal(proof)
	if err != nil {
		t.Fatal(err)
	}
	req := httptest.NewRequest(http.MethodPost, "/v1/confirmations", bytes.NewReader(body))
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	g.serveHumanConfirmation(rec, req)
	return rec.Code
}

// proposalFile loads the durable proposal record from the user's vault.
func proposalFile(t *testing.T, vaults, user, id string) cardProposal {
	t.Helper()
	body, err := os.ReadFile(filepath.Join(vaults, user, "runtime", "memory", "proposals", id+".json"))
	if err != nil {
		t.Fatalf("proposal %s missing: %v", id, err)
	}
	var prop cardProposal
	if err := json.Unmarshal(body, &prop); err != nil {
		t.Fatalf("proposal %s malformed: %v", id, err)
	}
	return prop
}

// ownerProof mints the confirmation exactly the way the delivery path would:
// every bound field taken from the stored proposal, actor set to the owner.
func ownerProof(prop cardProposal, auth identity.Envelope) api.HumanConfirmation {
	return api.HumanConfirmation{
		OnboardingID: prop.ID, Actor: auth, Nonce: prop.Nonce,
		Revision: prop.Revision, ActionDigest: proposalDigest(prop), ExpiresAt: prop.ExpiresAt,
	}
}

// TestMemoryCardProposalRoundTrip is the #519 contract: propose persists a
// durable, owner-bound edit proposal and changes nothing; only a verified
// human confirmation — the same /v1/confirmations proof the channel button
// posts — consumes it once and applies the exact stored writes.
func TestMemoryCardProposalRoundTrip(t *testing.T) {
	store, _, _, auths, _ := memoryCardStore(t, "alice")
	passports, vaults := t.TempDir(), t.TempDir()
	writeMemoryPassportEnv(t, passports, "alice", "dev", true)
	if err := vault.Ensure(filepath.Join(vaults, "alice")); err != nil {
		t.Fatal(err)
	}
	g, _ := memoryCardEnv(t, store, passports, vaults, auths, nil)
	proposalEnv(t, g, auths)
	alice := auths["alice"]

	if _, err := memoryCardCall(t, g, alice, "job-alice", "run-alice-1",
		map[string]any{"op": "fact", "card": "suppliers", "text": "earl grey from twinings"}); err != nil {
		t.Fatal(err)
	}
	cardPath := filepath.Join(vaults, "alice", "memory", "cards", "notes", "suppliers.md")
	before, err := os.ReadFile(cardPath)
	if err != nil {
		t.Fatal(err)
	}

	out, err := memoryCardCall(t, g, alice, "job-alice", "run-alice-1",
		map[string]any{"op": "propose", "inner": "truth", "card": "suppliers", "text": "Twinings supplies the earl grey."})
	if err != nil || out["status"] != "proposal" {
		t.Fatalf("propose: %v %v", out, err)
	}
	id, _ := out["id"].(string)
	if !strings.HasPrefix(id, "memprop-") || !identity.ValidID(id) {
		t.Fatalf("proposal id %q", id)
	}
	prop := proposalFile(t, vaults, "alice", id)
	if prop.PrincipalID != "alice" || prop.Environment != "dev" || prop.Used || prop.Nonce == "" || prop.ExpiresAt.IsZero() {
		t.Fatalf("proposal binding: %+v", prop)
	}
	if len(prop.Writes) != 1 || prop.Writes[0].Path != "memory/cards/notes/suppliers.md" || prop.Writes[0].BaseSHA == "" {
		t.Fatalf("proposal writes: %+v", prop.Writes)
	}
	if !strings.Contains(string(prop.Writes[0].Content), "Twinings supplies the earl grey.") {
		t.Fatalf("proposal carries no exact diff: %s", prop.Writes[0].Content)
	}
	// The proposal changed nothing yet.
	after, _ := os.ReadFile(cardPath)
	if string(after) != string(before) {
		t.Fatalf("propose mutated the card: %s", after)
	}

	// The model cannot confirm its own proposal — there is no apply op.
	out, err = memoryCardCall(t, g, alice, "job-alice", "run-alice-1",
		map[string]any{"op": "apply", "id": id})
	if err != nil || out["status"] != "invalid" {
		t.Fatalf("self-confirm must be invalid: %v %v", out, err)
	}

	// The verified owner action consumes the proposal and applies the writes.
	if code := confirmProposal(t, g, "ctl-secret", ownerProof(prop, alice)); code != http.StatusOK {
		t.Fatalf("owner confirmation: %d", code)
	}
	after, _ = os.ReadFile(cardPath)
	if !strings.Contains(string(after), "Twinings supplies the earl grey.") {
		t.Fatalf("confirmed edit missing: %s", after)
	}
	// Replay is denied and changes nothing.
	if code := confirmProposal(t, g, "ctl-secret", ownerProof(prop, alice)); code != http.StatusForbidden {
		t.Fatalf("replay must deny: %d", code)
	}
}

// TestMemoryCardProposalDenials is the #519 deny matrix: a foreign user's
// proof, a copied id, a forged digest, an expired proposal and a stale base
// each deny the confirmation without touching memory.
func TestMemoryCardProposalDenials(t *testing.T) {
	store, _, _, auths, _ := memoryCardStore(t, "alice", "bob")
	passports, vaults := t.TempDir(), t.TempDir()
	writeMemoryPassportEnv(t, passports, "alice", "dev", true)
	writeMemoryPassportEnv(t, passports, "bob", "dev", true)
	for _, user := range []string{"alice", "bob"} {
		if err := vault.Ensure(filepath.Join(vaults, user)); err != nil {
			t.Fatal(err)
		}
	}
	g, _ := memoryCardEnv(t, store, passports, vaults, auths, nil)
	proposalEnv(t, g, auths)
	alice, bob := auths["alice"], auths["bob"]

	if _, err := memoryCardCall(t, g, alice, "job-alice", "run-alice-1",
		map[string]any{"op": "fact", "card": "suppliers", "text": "earl grey from twinings"}); err != nil {
		t.Fatal(err)
	}
	cardPath := filepath.Join(vaults, "alice", "memory", "cards", "notes", "suppliers.md")
	before, err := os.ReadFile(cardPath)
	if err != nil {
		t.Fatal(err)
	}
	out, err := memoryCardCall(t, g, alice, "job-alice", "run-alice-1",
		map[string]any{"op": "propose", "inner": "truth", "card": "suppliers", "text": "forged by tests"})
	if err != nil || out["status"] != "proposal" {
		t.Fatalf("propose: %v %v", out, err)
	}
	prop := proposalFile(t, vaults, "alice", out["id"].(string))
	unchanged := func(t *testing.T, what string) {
		t.Helper()
		got, _ := os.ReadFile(cardPath)
		if string(got) != string(before) {
			t.Fatalf("%s mutated memory: %s", what, got)
		}
	}

	// A copied id under the wrong principal resolves the wrong vault — the
	// foreign user's proof finds no proposal there and denies.
	if code := confirmProposal(t, g, "ctl-secret", ownerProof(prop, bob)); code != http.StatusForbidden {
		t.Fatalf("foreign user confirmation: %d", code)
	}
	unchanged(t, "foreign confirmation")
	// A forged proof — wrong nonce, wrong digest — denies on the owner too.
	forged := ownerProof(prop, alice)
	forged.Nonce = "deadbeefdeadbeef"
	if code := confirmProposal(t, g, "ctl-secret", forged); code != http.StatusForbidden {
		t.Fatalf("forged nonce: %d", code)
	}
	forged = ownerProof(prop, alice)
	forged.ActionDigest = "deadbeef"
	if code := confirmProposal(t, g, "ctl-secret", forged); code != http.StatusForbidden {
		t.Fatalf("forged digest: %d", code)
	}
	unchanged(t, "forged proof")
	// A proposal id that never existed denies.
	missing := ownerProof(prop, alice)
	missing.OnboardingID = "memprop-0000000000000000"
	if code := confirmProposal(t, g, "ctl-secret", missing); code != http.StatusForbidden {
		t.Fatalf("copied id: %d", code)
	}
	unchanged(t, "copied id")

	// An owner edit between propose and confirm makes the stored base stale —
	// the fenced write denies instead of clobbering the newer content.
	out, err = memoryCardCall(t, g, alice, "job-alice", "run-alice-1",
		map[string]any{"op": "propose", "inner": "truth", "card": "suppliers", "text": "stale diff"})
	if err != nil || out["status"] != "proposal" {
		t.Fatalf("propose 2: %v %v", out, err)
	}
	prop2 := proposalFile(t, vaults, "alice", out["id"].(string))
	if _, err := memoryCardCall(t, g, alice, "job-alice", "run-alice-1",
		map[string]any{"op": "fact", "card": "suppliers", "text": "owner added a line meanwhile"}); err != nil {
		t.Fatal(err)
	}
	current, _ := os.ReadFile(cardPath)
	if code := confirmProposal(t, g, "ctl-secret", ownerProof(prop2, alice)); code != http.StatusForbidden {
		t.Fatalf("stale base must deny: %d", code)
	}
	after, _ := os.ReadFile(cardPath)
	if string(after) != string(current) {
		t.Fatalf("stale apply mutated the card: %s", after)
	}

	// An expired proposal denies even the owner's own valid proof.
	out, err = memoryCardCall(t, g, alice, "job-alice", "run-alice-1",
		map[string]any{"op": "propose", "inner": "truth", "card": "suppliers", "text": "expired diff"})
	if err != nil || out["status"] != "proposal" {
		t.Fatalf("propose 3: %v %v", out, err)
	}
	prop3 := proposalFile(t, vaults, "alice", out["id"].(string))
	prop3.ExpiresAt = time.Now().Add(-time.Hour).UTC()
	body, _ := json.Marshal(prop3)
	if err := os.WriteFile(filepath.Join(vaults, "alice", "runtime", "memory", "proposals", prop3.ID+".json"), body, 0o600); err != nil {
		t.Fatal(err)
	}
	if code := confirmProposal(t, g, "ctl-secret", ownerProof(prop3, alice)); code != http.StatusForbidden {
		t.Fatalf("expired proposal must deny: %d", code)
	}
	after, _ = os.ReadFile(cardPath)
	if strings.Contains(string(after), "expired diff") {
		t.Fatalf("expired apply mutated the card: %s", after)
	}
}

// TestMemoryCardProposalRevokedRight is the commit-gate for confirmations:
// the proposal snapshots the admitted authority, and a right revoked between
// propose and the owner tap still denies the mutation — the proposal is not
// consumed, memory is unchanged.
func TestMemoryCardProposalRevokedRight(t *testing.T) {
	store, gov, doc, auths, _ := memoryCardStore(t, "alice")
	passports, vaults := t.TempDir(), t.TempDir()
	writeMemoryPassportEnv(t, passports, "alice", "dev", true)
	if err := vault.Ensure(filepath.Join(vaults, "alice")); err != nil {
		t.Fatal(err)
	}
	g, _ := memoryCardEnv(t, store, passports, vaults, auths, nil)
	proposalEnv(t, g, auths)
	alice := auths["alice"]

	if _, err := memoryCardCall(t, g, alice, "job-alice", "run-alice-1",
		map[string]any{"op": "fact", "card": "suppliers", "text": "earl grey from twinings"}); err != nil {
		t.Fatal(err)
	}
	cardPath := filepath.Join(vaults, "alice", "memory", "cards", "notes", "suppliers.md")
	before, err := os.ReadFile(cardPath)
	if err != nil {
		t.Fatal(err)
	}
	out, err := memoryCardCall(t, g, alice, "job-alice", "run-alice-1",
		map[string]any{"op": "propose", "inner": "truth", "card": "suppliers", "text": "post-revoke diff"})
	if err != nil || out["status"] != "proposal" {
		t.Fatalf("propose: %v %v", out, err)
	}
	prop := proposalFile(t, vaults, "alice", out["id"].(string))

	// Revoke memory.remember before the owner confirms.
	_ = doc
	if err := gov.RevokeRight(api.DeterministicID("rightgrant", "alice", "memory", "remember"), "operator", "test revoke", time.Now()); err != nil {
		t.Fatal(err)
	}
	if code := confirmProposal(t, g, "ctl-secret", ownerProof(prop, alice)); code != http.StatusForbidden {
		t.Fatalf("revoked right must deny the apply: %d", code)
	}
	after, _ := os.ReadFile(cardPath)
	if string(after) != string(before) {
		t.Fatalf("denied apply mutated the card: %s", after)
	}
}

// TestMemoryCardProposalDisabledMemory is the sibling commit gate: memory
// disabled between propose and the owner tap denies the apply.
func TestMemoryCardProposalDisabledMemory(t *testing.T) {
	store, _, _, auths, _ := memoryCardStore(t, "alice")
	passports, vaults := t.TempDir(), t.TempDir()
	writeMemoryPassportEnv(t, passports, "alice", "dev", true)
	if err := vault.Ensure(filepath.Join(vaults, "alice")); err != nil {
		t.Fatal(err)
	}
	g, _ := memoryCardEnv(t, store, passports, vaults, auths, nil)
	proposalEnv(t, g, auths)
	alice := auths["alice"]

	if _, err := memoryCardCall(t, g, alice, "job-alice", "run-alice-1",
		map[string]any{"op": "fact", "card": "suppliers", "text": "earl grey from twinings"}); err != nil {
		t.Fatal(err)
	}
	cardPath := filepath.Join(vaults, "alice", "memory", "cards", "notes", "suppliers.md")
	before, err := os.ReadFile(cardPath)
	if err != nil {
		t.Fatal(err)
	}
	out, err := memoryCardCall(t, g, alice, "job-alice", "run-alice-1",
		map[string]any{"op": "propose", "inner": "truth", "card": "suppliers", "text": "post-disable diff"})
	if err != nil || out["status"] != "proposal" {
		t.Fatalf("propose: %v %v", out, err)
	}
	prop := proposalFile(t, vaults, "alice", out["id"].(string))

	writeMemoryPassportEnv(t, passports, "alice", "dev", false)
	if code := confirmProposal(t, g, "ctl-secret", ownerProof(prop, alice)); code != http.StatusForbidden {
		t.Fatalf("disabled memory must deny the apply: %d", code)
	}
	after, _ := os.ReadFile(cardPath)
	if string(after) != string(before) {
		t.Fatalf("denied apply mutated the card: %s", after)
	}
}

// TestMemoryCardProposalDisableBlocksCommit is the commit-point gate: the
// confirmation resolves enrollment before the per-user lock, then waits —
// a disable landing inside the held lock must still stop the apply.
func TestMemoryCardProposalDisableBlocksCommit(t *testing.T) {
	store, _, _, auths, _ := memoryCardStore(t, "alice")
	passports, vaults := t.TempDir(), t.TempDir()
	writeMemoryPassportEnv(t, passports, "alice", "dev", true)
	if err := vault.Ensure(filepath.Join(vaults, "alice")); err != nil {
		t.Fatal(err)
	}
	g, _ := memoryCardEnv(t, store, passports, vaults, auths, nil)
	proposalEnv(t, g, auths)
	alice := auths["alice"]

	if _, err := memoryCardCall(t, g, alice, "job-alice", "run-alice-1",
		map[string]any{"op": "fact", "card": "suppliers", "text": "earl grey from twinings"}); err != nil {
		t.Fatal(err)
	}
	cardPath := filepath.Join(vaults, "alice", "memory", "cards", "notes", "suppliers.md")
	before, err := os.ReadFile(cardPath)
	if err != nil {
		t.Fatal(err)
	}
	out, err := memoryCardCall(t, g, alice, "job-alice", "run-alice-1",
		map[string]any{"op": "propose", "inner": "truth", "card": "suppliers", "text": "post-disable diff"})
	if err != nil || out["status"] != "proposal" {
		t.Fatalf("propose: %v %v", out, err)
	}
	prop := proposalFile(t, vaults, "alice", out["id"].(string))

	u, err := g.Vaults.Resolve("alice", "dev")
	if err != nil {
		t.Fatal(err)
	}
	// Hold the vault lock: the confirmation resolves (memory still enabled),
	// then blocks at the serialization point — where the disable lands.
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
	result := make(chan int, 1)
	go func() { result <- confirmProposal(t, g, "ctl-secret", ownerProof(prop, alice)) }()
	time.Sleep(200 * time.Millisecond)
	close(release)
	<-done
	if code := <-result; code != http.StatusForbidden {
		t.Fatalf("disable inside the lock must deny the apply: %d", code)
	}
	after, _ := os.ReadFile(cardPath)
	if string(after) != string(before) {
		t.Fatalf("denied apply mutated the card: %s", after)
	}
}

// TestMemoryCardProposalMergeForget drives the remaining existing-memory
// inner ops through the proposal path: a merge's two writes and a forget's
// supersede both land only after the owner confirms.
func TestMemoryCardProposalMergeForget(t *testing.T) {
	store, _, _, auths, _ := memoryCardStore(t, "alice")
	passports, vaults := t.TempDir(), t.TempDir()
	writeMemoryPassportEnv(t, passports, "alice", "dev", true)
	if err := vault.Ensure(filepath.Join(vaults, "alice")); err != nil {
		t.Fatal(err)
	}
	g, _ := memoryCardEnv(t, store, passports, vaults, auths, nil)
	proposalEnv(t, g, auths)
	alice := auths["alice"]

	if _, err := memoryCardCall(t, g, alice, "job-alice", "run-alice-1",
		map[string]any{"op": "fact", "card": "suppliers", "text": "earl grey from twinings"}); err != nil {
		t.Fatal(err)
	}
	if _, err := memoryCardCall(t, g, alice, "job-alice", "run-alice-1",
		map[string]any{"op": "fact", "card": "vendors", "text": "lapsang from jing"}); err != nil {
		t.Fatal(err)
	}

	// Propose a merge — two planned writes, nothing moved yet.
	out, err := memoryCardCall(t, g, alice, "job-alice", "run-alice-1",
		map[string]any{"op": "propose", "inner": "merge", "card": "vendors", "to": "suppliers"})
	if err != nil || out["status"] != "proposal" {
		t.Fatalf("merge propose: %v %v", out, err)
	}
	prop := proposalFile(t, vaults, "alice", out["id"].(string))
	if len(prop.Writes) != 2 {
		t.Fatalf("merge proposal must carry two writes: %+v", prop.Writes)
	}
	srcPath := filepath.Join(vaults, "alice", "memory", "cards", "notes", "vendors.md")
	src, _ := os.ReadFile(srcPath)
	if strings.Contains(string(src), "status: superseded") {
		t.Fatalf("merge proposal already struck the source: %s", src)
	}
	if code := confirmProposal(t, g, "ctl-secret", ownerProof(prop, alice)); code != http.StatusOK {
		t.Fatalf("merge confirmation: %d", code)
	}
	dst, _ := os.ReadFile(filepath.Join(vaults, "alice", "memory", "cards", "notes", "suppliers.md"))
	src, _ = os.ReadFile(srcPath)
	if !strings.Contains(string(dst), "lapsang") || !strings.Contains(string(src), "status: superseded") {
		t.Fatalf("confirmed merge incomplete: dst=%s src=%s", dst, src)
	}

	// Propose a forget — the card keeps its active status until confirmed.
	out, err = memoryCardCall(t, g, alice, "job-alice", "run-alice-1",
		map[string]any{"op": "propose", "inner": "forget", "card": "suppliers"})
	if err != nil || out["status"] != "proposal" {
		t.Fatalf("forget propose: %v %v", out, err)
	}
	prop = proposalFile(t, vaults, "alice", out["id"].(string))
	if code := confirmProposal(t, g, "ctl-secret", ownerProof(prop, alice)); code != http.StatusOK {
		t.Fatalf("forget confirmation: %d", code)
	}
	dst, _ = os.ReadFile(filepath.Join(vaults, "alice", "memory", "cards", "notes", "suppliers.md"))
	if !strings.Contains(string(dst), "status: superseded") {
		t.Fatalf("confirmed forget left card active: %s", dst)
	}

	// An entry-id forget confirms through the strike sidecar.
	out, err = memoryCardCall(t, g, alice, "job-alice", "run-alice-1",
		map[string]any{"op": "propose", "inner": "forget", "id": "turn-9"})
	if err != nil || out["status"] != "proposal" {
		t.Fatalf("forget-id propose: %v %v", out, err)
	}
	prop = proposalFile(t, vaults, "alice", out["id"].(string))
	if code := confirmProposal(t, g, "ctl-secret", ownerProof(prop, alice)); code != http.StatusOK {
		t.Fatalf("forget-id confirmation: %d", code)
	}
	sidecar, err := os.ReadFile(filepath.Join(vaults, "alice", "runtime", "memory", "forget.jsonl"))
	if err != nil || !strings.Contains(string(sidecar), `"turn-9"`) {
		t.Fatalf("confirmed forget-id missed the strike: %v %s", err, sidecar)
	}
}

// TestMemoryCardProposalRetryConverges: a transport or model retry of one
// admitted propose replays the journaled result — same proposal id, one
// durable record — instead of minting a second proposal.
func TestMemoryCardProposalRetryConverges(t *testing.T) {
	store, _, _, auths, _ := memoryCardStore(t, "alice")
	passports, vaults := t.TempDir(), t.TempDir()
	writeMemoryPassportEnv(t, passports, "alice", "dev", true)
	if err := vault.Ensure(filepath.Join(vaults, "alice")); err != nil {
		t.Fatal(err)
	}
	g, _ := memoryCardEnv(t, store, passports, vaults, auths, nil)
	proposalEnv(t, g, auths)
	alice := auths["alice"]

	if _, err := memoryCardCall(t, g, alice, "job-alice", "run-alice-1",
		map[string]any{"op": "fact", "card": "suppliers", "text": "earl grey from twinings"}); err != nil {
		t.Fatal(err)
	}
	args := map[string]any{"op": "propose", "inner": "truth", "card": "suppliers", "text": "retry diff"}
	out, err := memoryCardCall(t, g, alice, "job-alice", "run-alice-1", args)
	if err != nil || out["status"] != "proposal" {
		t.Fatalf("propose: %v %v", out, err)
	}
	retry, err := memoryCardCall(t, g, alice, "job-alice", "run-alice-1", args)
	if err != nil || retry["status"] != "proposal" {
		t.Fatalf("propose retry: %v %v", retry, err)
	}
	if retry["id"] != out["id"] {
		t.Fatalf("retry minted a second proposal: %v vs %v", retry["id"], out["id"])
	}
	entries, err := os.ReadDir(filepath.Join(vaults, "alice", "runtime", "memory", "proposals"))
	if err != nil || len(entries) != 1 {
		t.Fatalf("expected exactly one proposal record: %v %d", err, len(entries))
	}
}
