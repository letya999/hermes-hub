package toolhub

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
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
	RequestID   string             `json:"request_id,omitempty"`
	PrincipalID string             `json:"principal_id"`
	Environment string             `json:"environment"`
	Revision    uint64             `json:"revision"`
	Nonce       string             `json:"nonce"`
	ExpiresAt   time.Time          `json:"expires_at"`
	Used        bool               `json:"used"`
	State       string             `json:"state,omitempty"`
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
	f, err := os.OpenFile(tmp, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0o600)
	if err != nil {
		return err
	}
	if _, err = f.Write(body); err == nil {
		err = f.Sync()
	}
	if closeErr := f.Close(); err == nil {
		err = closeErr
	}
	if err != nil {
		_ = os.Remove(tmp)
		return err
	}
	if err := os.Rename(tmp, path); err != nil {
		_ = os.Remove(tmp)
		return err
	}
	return nil
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
	cur, baseSHA, err := vault.NewWriter(root).Read(rel)
	if err != nil || baseSHA == "" {
		return nil, errors.New("unknown card")
	}
	content := replaceSection(string(cur), "## Compiled Truth", text+"\n")
	return []cardPlannedWrite{{Path: rel, Content: []byte(content), BaseSHA: baseSHA}}, nil
}

// planMerge renders the merge op's two writes — the folded target first,
// the struck source second — without applying them.
func planMerge(root string, arguments map[string]any) ([]cardPlannedWrite, error) {
	src, dst := cardFind(root, api.ArgString(arguments, "card")), cardFind(root, api.ArgString(arguments, "to"))
	if src == "" || dst == "" || src == dst {
		return nil, errors.New("card and to must resolve to two different cards")
	}
	srcBytes, srcSHA, err := vault.NewWriter(root).Read(src)
	if err != nil {
		return nil, err
	}
	dstBytes, dstSHA, err := vault.NewWriter(root).Read(dst)
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
		{Path: dst, Content: []byte(merged), BaseSHA: dstSHA},
		{Path: src, Content: []byte(struck), BaseSHA: srcSHA},
	}, nil
}

// planForgetCard renders the forget op's card supersede without applying it.
func planForgetCard(root, card string) ([]cardPlannedWrite, error) {
	rel := cardFind(root, card)
	if rel == "" {
		return nil, errors.New("unknown card")
	}
	cur, baseSHA, err := vault.NewWriter(root).Read(rel)
	if err != nil || baseSHA == "" {
		return nil, errors.New("unknown card")
	}
	content := strings.Replace(string(cur), "status: active", "status: superseded", 1)
	if content == string(cur) {
		return nil, errors.New("card carries no active status to strike")
	}
	return []cardPlannedWrite{{Path: rel, Content: []byte(content), BaseSHA: baseSHA}}, nil
}

// cardPropose persists an edit proposal for one existing-memory inner op and
// returns its id. Nothing mutates: only the owner confirmation path consumes
// the proposal.
func (g *Gateway) cardPropose(ctx context.Context, root string, auth identity.Envelope, corr api.CallCorrelation, effective api.EffectiveBinding, requestID string, arguments map[string]any) (map[string]any, error) {
	id := "memprop-" + requestID[:16]
	if _, err := os.Stat(proposalPath(root, id)); err == nil {
		prior, err := loadProposal(root, id)
		if err != nil || prior.RequestID != requestID || prior.PrincipalID != auth.PrincipalID || prior.Environment != auth.Environment {
			return nil, api.ErrUnauthorized
		}
		deliverMemoryProposal(ctx, auth, prior)
		return map[string]any{"status": "proposal", "op": "propose", "id": id}, nil
	} else if !errors.Is(err, os.ErrNotExist) {
		return nil, err
	}
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
		ID: id, RequestID: requestID, PrincipalID: auth.PrincipalID,
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
	deliverMemoryProposal(ctx, auth, prop)
	return map[string]any{"status": "proposal", "op": "propose", "id": prop.ID}, nil
}

// Delivery can be retried after a saved proposal loses its response; the
// proposal id and proof stay stable, so a duplicate button cannot re-arm it.
func deliverMemoryProposal(ctx context.Context, auth identity.Envelope, prop cardProposal) {
	target := prop.StrikeID
	if len(prop.Writes) > 0 {
		target = prop.Writes[0].Path
	}
	label := prop.InnerOp + " " + target
	api.DeliverMemoryConfirmation(ctx, auth, api.HumanConfirmation{
		OnboardingID: prop.ID, Actor: auth, Nonce: prop.Nonce, Revision: prop.Revision,
		ActionDigest: proposalDigest(prop), ExpiresAt: prop.ExpiresAt,
	}, label)
}

// consumeMemoryProposal applies a proposal on its verified human
// confirmation — the /v1/confirmations endpoint routes the proposal id
// namespace here. Denials are uniform.
func (g *Gateway) consumeMemoryProposal(proof api.HumanConfirmation) error {
	u, err := g.Vaults.resolveForProposal(proof.Actor.PrincipalID, proof.Actor.Environment)
	if err != nil {
		return api.ErrUnauthorized
	}
	return u.Locked(func(root string) error {
		prop, err := loadProposal(root, proof.OnboardingID)
		if err != nil {
			return api.ErrUnauthorized
		}
		now := g.Control.now()
		if prop.State == "complete" || (prop.State != "" && prop.State != "applying") || (prop.State == "applying" && !prop.Used) ||
			prop.Nonce == "" || prop.Nonce != proof.Nonce || prop.PrincipalID != proof.Actor.PrincipalID ||
			prop.Environment != proof.Actor.Environment || prop.Authority.Binding.ContextID != proof.Actor.ContextID ||
			prop.Authority.Binding.RuntimeID != proof.Actor.RuntimeID || prop.Authority.Binding.PolicyVersion != proof.Actor.PolicyVersion ||
			proof.Revision != prop.Revision || !proof.ExpiresAt.Equal(prop.ExpiresAt) ||
			proof.ActionDigest == "" || proof.ActionDigest != proposalDigest(prop) {
			return api.ErrUnauthorized
		}
		if prop.State == "" && !prop.Used {
			if !now.Before(prop.ExpiresAt) || memoryEnrollment(g.PassportRoot, prop.PrincipalID, prop.Environment) != nil {
				return proposalDenied(root, prop)
			}
			if err := g.Store.Reload(); err != nil {
				return err
			}
			if err := g.Store.ReverifyEffective(prop.Authority.effective()); err != nil {
				return proposalDenied(root, prop)
			}
			writer := vault.NewWriter(root)
			for _, write := range prop.Writes {
				_, digest, err := writer.Read(write.Path)
				if err != nil || digest != write.BaseSHA {
					return proposalDenied(root, prop)
				}
			}
			// The durable applying state is the transaction's commit point.
			// Recovery finishes this already-authorized action after a crash.
			prop.Used, prop.State = true, "applying"
			if err := saveProposal(root, prop); err != nil {
				return err
			}
		}
		if prop.State == "" && prop.Used { // interrupted record from the prior format
			prop.State = "applying"
			if err := saveProposal(root, prop); err != nil {
				return err
			}
		}
		return finishMemoryProposal(root, &prop, g.proposalAfterWrite)
	})
}

func proposalDenied(root string, prop cardProposal) error {
	if err := cardJournalAppend(root, "apply", prop.ID, prop.SourceRef, prop.ID, "denied", nil); err != nil {
		return err
	}
	return api.ErrUnauthorized
}

func finishMemoryProposal(root string, prop *cardProposal, afterWrite func(int) error) error {
	writer := vault.NewWriter(root)
	for i, write := range prop.Writes {
		_, currentSHA, err := writer.Read(write.Path)
		if err != nil {
			return err
		}
		newSHA := sha256.Sum256(write.Content)
		afterSHA := hex.EncodeToString(newSHA[:])
		op := memory.WriteOp{Path: write.Path, Content: write.Content, WantSHA: write.BaseSHA, Actor: memory.ActorAgent}
		switch currentSHA {
		case afterSHA:
			if err := writer.RecoverApplied(op); err != nil {
				return err
			}
		case write.BaseSHA:
			if _, err := writer.Apply(op); err != nil {
				return err
			}
		default:
			return fmt.Errorf("pending proposal target changed: %w", memory.ErrStaleHash)
		}
		if afterWrite != nil {
			if err := afterWrite(i); err != nil {
				return err
			}
		}
	}
	if prop.StrikeID != "" {
		forget := memory.OpenForgetSet(filepath.Join(root, "runtime", "memory"))
		if !forget.Set()[prop.StrikeID] {
			if err := forget.Strike(prop.StrikeID); err != nil {
				return err
			}
		}
	}
	if _, ok := cardJournalResult(root, prop.ID); !ok {
		if err := cardJournalAppend(root, "apply", prop.ID, prop.SourceRef, prop.ID, "ok", map[string]any{"status": "ok", "op": "apply", "id": prop.ID}); err != nil {
			return err
		}
	}
	prop.State = "complete"
	return saveProposal(root, *prop)
}

// recoverMemoryProposals completes confirmed operations before ToolHub serves
// requests. Pending proposals have no verified human action and stay inert.
func (g *Gateway) recoverMemoryProposals() error {
	if g.Vaults == nil {
		return nil
	}
	for principal, root := range g.Vaults.bindings {
		entries, err := os.ReadDir(filepath.Join(root, "runtime", "memory", "proposals"))
		if errors.Is(err, os.ErrNotExist) {
			continue
		}
		if err != nil {
			return err
		}
		for _, entry := range entries {
			if !strings.HasPrefix(entry.Name(), "memprop-") || !strings.HasSuffix(entry.Name(), ".json") {
				continue
			}
			id := strings.TrimSuffix(entry.Name(), ".json")
			prop, err := loadProposal(root, id)
			if err != nil {
				return err
			}
			if prop.State == "complete" || !prop.Used && prop.State == "" {
				continue
			}
			if prop.ID != id || prop.PrincipalID != principal || !prop.Used || prop.State != "applying" && prop.State != "" {
				return fmt.Errorf("invalid consumed memory proposal %s", id)
			}
			u, err := g.Vaults.resolveForProposal(principal, prop.Environment)
			if err != nil {
				return err
			}
			if err := u.Locked(func(root string) error {
				current, err := loadProposal(root, id)
				if err != nil || current.ID != id || current.PrincipalID != principal || !current.Used {
					return fmt.Errorf("memory proposal changed during recovery: %s", id)
				}
				return finishMemoryProposal(root, &current, nil)
			}); err != nil {
				return err
			}
		}
	}
	return nil
}
