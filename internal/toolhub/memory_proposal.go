package toolhub

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/letya999/hermes-hub/internal/identity"
	"github.com/letya999/hermes-hub/internal/memory"
	"github.com/letya999/hermes-hub/internal/toolhub/api"
	"github.com/letya999/hermes-hub/internal/vault"
)

// cardProposal is the durable, owner-bound edit proposal of ADR-0095 D6
// (#519): it lives inside the user's own vault, carries the exact planned
// writes against the recorded base revisions, a single-use nonce and an
// expiry, and only a verified human confirmation consumes it.
type cardProposal struct {
	ID          string             `json:"id"`
	PrincipalID string             `json:"principal_id"`
	Environment string             `json:"environment"`
	Revision    uint64             `json:"revision"`
	Nonce       string             `json:"nonce"`
	ExpiresAt   time.Time          `json:"expires_at"`
	Used        bool               `json:"used"`
	SourceRef   string             `json:"source_ref"`
	InnerOp     string             `json:"inner"`
	Writes      []cardPlannedWrite `json:"writes"`
	StrikeID    string             `json:"strike_id,omitempty"`
	Authority   proposalAuthority  `json:"authority"`
}

// proposalAuthority snapshots the admission that created the proposal so the
// confirmation-time recheck verifies exactly the admitted authority — not
// whatever a later caller claims.
type proposalAuthority struct {
	Binding                   api.ToolBinding    `json:"binding"`
	Definition                api.ToolDefinition `json:"definition"`
	SelectedToolName          string             `json:"selected_tool"`
	CapabilityProfileID       string             `json:"capability_profile_id,omitempty"`
	CapabilityProfileRevision uint64             `json:"capability_profile_revision,omitempty"`
	CapabilityPolicyID        string             `json:"capability_policy_id,omitempty"`
	CapabilityPolicyRevision  uint64             `json:"capability_policy_revision,omitempty"`
}

func (a proposalAuthority) effective() api.EffectiveBinding {
	return api.EffectiveBinding{
		Binding: a.Binding, Definition: a.Definition, SelectedToolName: a.SelectedToolName,
		CapabilityProfileID: a.CapabilityProfileID, CapabilityProfileRevision: a.CapabilityProfileRevision,
		CapabilityPolicyID: a.CapabilityPolicyID, CapabilityPolicyRevision: a.CapabilityPolicyRevision,
	}
}

// cardPlannedWrite is one exact diff: the full new content for a path and
// the base content hash it was rendered against. The governed writer's
// WantSHA check makes a stale base deny the apply.
type cardPlannedWrite struct {
	Path    string `json:"path"`
	Content []byte `json:"content"`
	BaseSHA string `json:"base_sha"`
}

// proposalDigest binds the verified confirmation to this exact proposal —
// the owner, the inner op and the exact writes it may apply.
func proposalDigest(p cardProposal) string {
	canonical, _ := json.Marshal(struct {
		Owner  string             `json:"owner"`
		Env    string             `json:"env"`
		Rev    uint64             `json:"rev"`
		Inner  string             `json:"inner"`
		Writes []cardPlannedWrite `json:"writes"`
		Strike string             `json:"strike,omitempty"`
	}{p.PrincipalID, p.Environment, p.Revision, p.InnerOp, p.Writes, p.StrikeID})
	sum := sha256.Sum256(canonical)
	return hex.EncodeToString(sum[:])
}

// proposalPath confines the proposal id below runtime/memory/proposals/.
func proposalPath(root, id string) string {
	if !strings.HasPrefix(id, "memprop-") || strings.ContainsAny(id, "/\\.") {
		return ""
	}
	return filepath.Join(root, "runtime", "memory", "proposals", id+".json")
}

// saveProposal lands the record atomically: a temp write renamed into place,
// so a crash mid-write never leaves a half-parsed proposal a later
// confirmation could consume.
func saveProposal(root string, p cardProposal) error {
	path := proposalPath(root, p.ID)
	if path == "" {
		return errors.New("proposal id outside the proposal namespace")
	}
	body, err := json.Marshal(p)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, body, 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}

func loadProposal(root, id string) (cardProposal, error) {
	path := proposalPath(root, id)
	if path == "" {
		return cardProposal{}, api.ErrUnauthorized
	}
	body, err := os.ReadFile(path)
	if err != nil {
		return cardProposal{}, api.ErrUnauthorized
	}
	var p cardProposal
	if err := json.Unmarshal(body, &p); err != nil {
		return cardProposal{}, api.ErrUnauthorized
	}
	return p, nil
}

// planTruth renders the truth op's write without applying it — propose
// stores the result, the direct op applies it.
func planTruth(root string, arguments map[string]any) ([]cardPlannedWrite, error) {
	card := strings.TrimSpace(api.ArgString(arguments, "card"))
	text := strings.TrimSpace(api.ArgString(arguments, "text"))
	if card == "" || text == "" {
		return nil, errors.New("card and text required")
	}
	rel := cardRel(card)
	cur, err := os.ReadFile(filepath.Join(root, filepath.FromSlash(rel)))
	if err != nil {
		return nil, errors.New("unknown card")
	}
	content := replaceSection(string(cur), "## Compiled Truth", text+"\n")
	return []cardPlannedWrite{{Path: rel, Content: []byte(content), BaseSHA: shaOf(root, rel)}}, nil
}

// planMerge renders the merge op's two writes — the folded target first,
// the struck source second — without applying them.
func planMerge(root string, arguments map[string]any) ([]cardPlannedWrite, error) {
	src, dst := cardFind(root, api.ArgString(arguments, "card")), cardFind(root, api.ArgString(arguments, "to"))
	if src == "" || dst == "" || src == dst {
		return nil, errors.New("card and to must resolve to two different cards")
	}
	srcBytes, err := os.ReadFile(filepath.Join(root, filepath.FromSlash(src)))
	if err != nil {
		return nil, err
	}
	dstBytes, err := os.ReadFile(filepath.Join(root, filepath.FromSlash(dst)))
	if err != nil {
		return nil, err
	}
	body := cardBody(string(srcBytes))
	if body == "" {
		return nil, errors.New("source card carries no body to merge")
	}
	merged := strings.TrimRight(string(dstBytes), "\n") + "\n\n" + body
	struck := strings.Replace(string(srcBytes), "status: active", "status: superseded", 1)
	return []cardPlannedWrite{
		{Path: dst, Content: []byte(merged), BaseSHA: shaOf(root, dst)},
		{Path: src, Content: []byte(struck), BaseSHA: shaOf(root, src)},
	}, nil
}

// planForgetCard renders the forget op's card supersede without applying it.
func planForgetCard(root, card string) ([]cardPlannedWrite, error) {
	rel := cardFind(root, card)
	if rel == "" {
		return nil, errors.New("unknown card")
	}
	cur, err := os.ReadFile(filepath.Join(root, filepath.FromSlash(rel)))
	if err != nil {
		return nil, errors.New("unknown card")
	}
	content := strings.Replace(string(cur), "status: active", "status: superseded", 1)
	if content == string(cur) {
		return nil, errors.New("card carries no active status to strike")
	}
	return []cardPlannedWrite{{Path: rel, Content: []byte(content), BaseSHA: shaOf(root, rel)}}, nil
}

// cardPropose persists an edit proposal for one existing-memory inner op and
// returns its id. Nothing mutates: only the owner confirmation path consumes
// the proposal.
func (g *Gateway) cardPropose(ctx context.Context, root string, auth identity.Envelope, corr api.CallCorrelation, effective api.EffectiveBinding, arguments map[string]any) (map[string]any, error) {
	inner := strings.TrimSpace(api.ArgString(arguments, "inner"))
	var writes []cardPlannedWrite
	var strikeID string
	var err error
	switch inner {
	case "truth":
		writes, err = planTruth(root, arguments)
	case "merge":
		writes, err = planMerge(root, arguments)
	case "forget":
		if id := strings.TrimSpace(api.ArgString(arguments, "id")); id != "" {
			strikeID = id
		} else {
			writes, err = planForgetCard(root, api.ArgString(arguments, "card"))
		}
	default:
		return cardInvalid("propose", []cardFieldError{{Field: "inner", Rule: "one of truth|merge|forget"}}), nil
	}
	if err != nil {
		return cardInvalid("propose", []cardFieldError{{Field: "card", Rule: err.Error()}}), nil
	}
	if inner == "forget" && strikeID == "" && len(writes) == 0 {
		return cardInvalid("propose", []cardFieldError{{Field: "id", Rule: "id or card required"}}), nil
	}
	prop := cardProposal{
		ID: "memprop-" + api.RandomNonce()[:16], PrincipalID: auth.PrincipalID,
		Environment: auth.Environment, Revision: 1, Nonce: api.RandomNonce(),
		ExpiresAt: g.Control.now().Add(g.Control.ttl()), SourceRef: cardSourceRef(corr),
		InnerOp: inner, Writes: writes, StrikeID: strikeID,
		Authority: proposalAuthority{
			Binding: effective.Binding, Definition: effective.Definition, SelectedToolName: effective.SelectedToolName,
			CapabilityProfileID: effective.CapabilityProfileID, CapabilityProfileRevision: effective.CapabilityProfileRevision,
			CapabilityPolicyID: effective.CapabilityPolicyID, CapabilityPolicyRevision: effective.CapabilityPolicyRevision,
		},
	}
	if err := saveProposal(root, prop); err != nil {
		return nil, err
	}
	// The owner confirmation rides the existing verified human-action lane —
	// the same proof/button/callback machinery onboarding uses. Delivery is
	// best-effort: an unconfigured channel leaves a durable proposal.
	target := strikeID
	if len(writes) > 0 {
		target = writes[0].Path
	}
	label := inner + " " + target
	api.DeliverMemoryConfirmation(ctx, auth, api.HumanConfirmation{
		OnboardingID: prop.ID, Actor: auth, Nonce: prop.Nonce, Revision: prop.Revision,
		ActionDigest: proposalDigest(prop), ExpiresAt: prop.ExpiresAt,
	}, label)
	return map[string]any{"status": "proposal", "op": "propose", "id": prop.ID}, nil
}

// consumeMemoryProposal applies a proposal on its verified human
// confirmation — the /v1/confirmations endpoint routes the proposal id
// namespace here. Denials are uniform.
func (g *Gateway) consumeMemoryProposal(proof api.HumanConfirmation) error {
	u, err := g.Vaults.Resolve(proof.Actor.PrincipalID, proof.Actor.Environment)
	if err != nil {
		return api.ErrUnauthorized
	}
	return u.Locked(func(root string) error {
		prop, err := loadProposal(root, proof.OnboardingID)
		if err != nil {
			return api.ErrUnauthorized
		}
		now := g.Control.now()
		if prop.Used || prop.Nonce == "" || prop.Nonce != proof.Nonce || prop.PrincipalID != proof.Actor.PrincipalID ||
			proof.Revision != prop.Revision || !now.Before(prop.ExpiresAt) || !proof.ExpiresAt.Equal(prop.ExpiresAt) ||
			proof.ActionDigest == "" || proof.ActionDigest != proposalDigest(prop) {
			return api.ErrUnauthorized
		}
		// Commit rechecks the live enrollment — the resolver's earlier check
		// predates this lock, so a disable landed while waiting still stops
		// the mutation.
		if err := memoryEnrollment(g.PassportRoot, prop.PrincipalID, prop.Environment); err != nil {
			cardJournalAppend(root, "apply", prop.ID, prop.SourceRef, prop.ID, "denied", nil)
			return api.ErrUnauthorized
		}
		// Commit rechecks the authority that admitted the proposal — a
		// revocation landed between propose and the owner tap still denies
		// the mutation, before the nonce is spent.
		if err := g.Store.ReverifyEffective(prop.Authority.effective()); err != nil {
			cardJournalAppend(root, "apply", prop.ID, prop.SourceRef, prop.ID, "denied", nil)
			return api.ErrUnauthorized
		}
		// Consume first: a crash after the mark can never replay the proof;
		// a failed write leaves the honest denial, not a re-armed proposal.
		prop.Used = true
		if err := saveProposal(root, prop); err != nil {
			return errors.New("proposal consume persist failed")
		}
		if prop.StrikeID != "" {
			if err := memory.OpenForgetSet(filepath.Join(root, "runtime", "memory")).Strike(prop.StrikeID); err != nil {
				cardJournalAppend(root, "apply", prop.ID, prop.SourceRef, prop.ID, "denied", nil)
				return api.ErrUnauthorized
			}
		}
		for _, w := range prop.Writes {
			if _, err := vault.NewWriter(root).Apply(memory.WriteOp{Path: w.Path, Content: w.Content, WantSHA: w.BaseSHA, Actor: memory.ActorAgent}); err != nil {
				cardJournalAppend(root, "apply", prop.ID, prop.SourceRef, prop.ID, "denied", nil)
				return api.ErrUnauthorized
			}
		}
		cardJournalAppend(root, "apply", prop.ID, prop.SourceRef, prop.ID, "ok", map[string]any{"status": "ok", "op": "apply", "id": prop.ID})
		return nil
	})
}
