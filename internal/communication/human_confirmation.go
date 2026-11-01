package communication

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"path/filepath"
	"strings"
	"time"

	"github.com/letya999/hermes-hub/internal/identity"
	"github.com/letya999/hermes-hub/internal/locale"
	"github.com/letya999/hermes-hub/internal/toolhub/api"
)

type humanCallback struct {
	Proof   api.HumanConfirmation `json:"proof"`
	Channel string                `json:"channel"`
	ChatID  int64                 `json:"chat_id"`
}

func (s *Spool) humanConfirmationButton(proof api.HumanConfirmation, channel string, chatID int64) (DeliveryButton, error) {
	record := humanCallback{proof, channel, chatID}
	body, _ := json.Marshal(record)
	sum := sha256.Sum256(body)
	id := hex.EncodeToString(sum[:12])
	if err := s.writeJSON(filepath.Join(s.root, "callbacks", "human-"+spoolFileID("h:"+id)+".json"), record); err != nil {
		return DeliveryButton{}, err
	}
	lang := locale.Fallback()
	if s.langOf != nil {
		lang = s.langOf(proof.Actor.PrincipalID)
	}
	return DeliveryButton{Text: locale.T(lang, "chat.confirm-button"), Data: "h:" + id}, nil
}

func (g *Gateway) confirmHumanCallback(ctx context.Context, caller identity.Envelope, channel string, chatID int64, data string) error {
	denied := errors.New("human confirmation unavailable")
	path := filepath.Join(g.spool.root, "callbacks", "human-"+spoolFileID(data)+".json")
	body, err := g.spool.fsys.ReadFile(path)
	if err != nil {
		return denied
	}
	var record humanCallback
	if err != nil || len(body) > 64<<10 || json.Unmarshal(body, &record) != nil {
		return denied
	}
	a := record.Proof.Actor
	if !g.now().Before(record.Proof.ExpiresAt) {
		_ = g.spool.removeOwned(path)
		return denied
	}
	if caller.Validate(a.PrincipalID, a.ContextID, a.RuntimeID, a.PolicyVersion) != nil || record.Channel != channel || record.ChatID != chatID {
		return denied
	}
	record.Proof.Actor = caller
	body, _ = json.Marshal(record.Proof)
	callCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(callCtx, http.MethodPost, strings.TrimRight(g.config.ToolHubURL, "/")+"/v1/confirmations", bytes.NewReader(body))
	if err != nil {
		return denied
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+g.config.ControlAuth)
	response, err := http.DefaultClient.Do(req)
	if err != nil {
		return denied
	}
	defer response.Body.Close()
	_, _ = io.Copy(io.Discard, io.LimitReader(response.Body, 4096))
	if response.StatusCode != http.StatusOK {
		return denied
	}
	return g.spool.removeOwned(path)
}
