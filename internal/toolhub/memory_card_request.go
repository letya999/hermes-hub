package toolhub

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"

	"github.com/letya999/hermes-hub/internal/identity"
	"github.com/letya999/hermes-hub/internal/memory"
	"github.com/letya999/hermes-hub/internal/toolhub/api"
	"github.com/letya999/hermes-hub/internal/vault"
)

// cardRequest is a per-vault write intent. The full desired bytes and base
// hash are durable before Writer sees them; retries finish the same effect.
type cardRequest struct {
	ID          string           `json:"id"`
	PrincipalID string           `json:"principal_id"`
	Environment string           `json:"environment"`
	Op          string           `json:"op"`
	SourceRef   string           `json:"source_ref"`
	Plan        cardPlannedWrite `json:"plan"`
	Result      map[string]any   `json:"result"`
	Applied     bool             `json:"applied"`
	Journaled   bool             `json:"journaled"`
}

func cardRequestPath(root, id string) (string, error) {
	if len(id) != 32 {
		return "", api.ErrInvalid
	}
	if _, err := hex.DecodeString(id); err != nil {
		return "", api.ErrInvalid
	}
	return filepath.Join(root, "runtime", "memory", "requests", id+".json"), nil
}

func loadCardRequest(root, id string) (cardRequest, error) {
	path, err := cardRequestPath(root, id)
	if err != nil {
		return cardRequest{}, err
	}
	body, err := os.ReadFile(path)
	if err != nil {
		return cardRequest{}, err
	}
	var request cardRequest
	if err := json.Unmarshal(body, &request); err != nil {
		return cardRequest{}, err
	}
	return request, nil
}

func saveCardRequest(root string, request cardRequest) error {
	path, err := cardRequestPath(root, request.ID)
	if err != nil {
		return err
	}
	body, err := json.Marshal(request)
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

func applyCardRequest(root string, auth identity.Envelope, corr api.CallCorrelation, op, id string, arguments map[string]any) (map[string]any, error) {
	request, err := loadCardRequest(root, id)
	if errors.Is(err, os.ErrNotExist) {
		if prior, ok := cardJournalResult(root, id); ok {
			return prior, nil // older completed request, before intents existed
		}
		var plan cardPlannedWrite
		var result map[string]any
		if op == "remember" {
			plan, result, err = cardRememberPlan(root, arguments)
		} else {
			plan, result, err = cardFactPlan(root, arguments)
		}
		if err != nil {
			return nil, err
		}
		if result["status"] != "ok" {
			return result, cardJournalAppend(root, op, "", cardSourceRef(corr), id, "invalid", result)
		}
		request = cardRequest{ID: id, PrincipalID: auth.PrincipalID, Environment: auth.Environment,
			Op: op, SourceRef: cardSourceRef(corr), Plan: plan, Result: result}
		if err := saveCardRequest(root, request); err != nil {
			return nil, err
		}
	} else if err != nil {
		return nil, err
	}
	if request.ID != id || request.PrincipalID != auth.PrincipalID || request.Environment != auth.Environment || request.Op != op ||
		request.Plan.Path == "" || request.Result["status"] != "ok" {
		return nil, api.ErrUnauthorized
	}
	writer := vault.NewWriter(root)
	if !request.Applied {
		_, currentSHA, err := writer.Read(request.Plan.Path)
		if err != nil {
			return nil, err
		}
		after := sha256.Sum256(request.Plan.Content)
		afterSHA := hex.EncodeToString(after[:])
		write := memory.WriteOp{Path: request.Plan.Path, Content: request.Plan.Content,
			WantSHA: request.Plan.BaseSHA, Actor: memory.ActorAgent}
		switch currentSHA {
		case request.Plan.BaseSHA:
			if _, err := writer.Apply(write); err != nil {
				return nil, err
			}
		case afterSHA:
			if err := writer.RecoverApplied(write); err != nil {
				return nil, err
			}
		default:
			return nil, fmt.Errorf("%w: pending memory request target changed", memory.ErrStaleHash)
		}
		request.Applied = true
		if err := saveCardRequest(root, request); err != nil {
			return nil, err
		}
	}
	if !request.Journaled {
		if _, ok := cardJournalResult(root, id); !ok {
			if err := cardJournalAppend(root, op, request.Plan.Path, request.SourceRef, id, "ok", request.Result); err != nil {
				return nil, err
			}
		}
		request.Journaled = true
		if err := saveCardRequest(root, request); err != nil {
			return nil, err
		}
	}
	return request.Result, nil
}
