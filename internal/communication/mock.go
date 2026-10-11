package communication

import (
	"encoding/json"
	"errors"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/letya999/hermes-hub/internal/identity"
)

// The mock channel is the local acceptance bench: real pipeline, zero
// external APIs. Inbound goes through the same task binding and queue as a
// channel message; outbound deliveries are captured durably under
// spool/mock-sent for inspection instead of being sent anywhere.

// handleMockMessage injects one user message for the calling principal.
// client_id is the caller's idempotency handle — repeats never enqueue twice.
func (g *Gateway) handleMockMessage(w http.ResponseWriter, r *http.Request) {
	caller, ok := g.authorizeControl(r)
	if !ok {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}
	var in struct {
		ChatID   int64  `json:"chat_id"`
		Text     string `json:"text"`
		ClientID string `json:"client_id"`
	}
	if err := decodeJSON(r, &in); err != nil {
		http.Error(w, "invalid mock message", http.StatusBadRequest)
		return
	}
	owner, err := g.mockLinkOwner(in.ChatID)
	if err != nil {
		http.Error(w, "link failed", http.StatusInternalServerError)
		return
	}
	if owner != "" && owner != caller.PrincipalID {
		http.Error(w, "forbidden", http.StatusForbidden)
		return
	}
	user := g.user(caller.PrincipalID)
	if owner == "" {
		if err := g.mockLinkClaim(in.ChatID, caller.PrincipalID); err != nil {
			http.Error(w, "link failed", http.StatusInternalServerError)
			return
		}
	}
	task, err := g.spool.ResolveTask(user, in.ChatID, 0, true)
	if err != nil {
		http.Error(w, "task failed", http.StatusInternalServerError)
		return
	}
	key := "mock:" + caller.PrincipalID + ":" + in.ClientID
	if in.ClientID == "" {
		key += time.Now().UTC().Format("150405.000000000")
	}
	id := "mock-" + in.ClientID
	if in.ClientID == "" {
		id = "mock-" + newID()
	}
	if err := g.enqueueChannelJob(r.Context(), user, task, "mock", id, key, in.ChatID, 0, in.Text, in.ChatID); err != nil {
		http.Error(w, "enqueue failed", http.StatusInternalServerError)
		return
	}
	writeJSON(w, map[string]any{"job_id": id, "task_id": task.TaskID})
}

// handleMockOutbox lists the deliveries this principal's mock channel
// captured — the durable record of what the agent "sent".
func (g *Gateway) handleMockOutbox(w http.ResponseWriter, r *http.Request) {
	caller, ok := g.authorizeControl(r)
	if !ok {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}
	entries, err := os.ReadDir(filepath.Join(g.spool.root, "mock-sent"))
	out := []Delivery{}
	if err == nil {
		for _, e := range entries {
			if e.IsDir() || !strings.HasSuffix(e.Name(), ".json") {
				continue
			}
			b, err := os.ReadFile(filepath.Join(g.spool.root, "mock-sent", e.Name()))
			if err != nil {
				continue
			}
			var rec struct {
				Principal string    `json:"principal"`
				Delivery  *Delivery `json:"delivery"`
			}
			if err := json.Unmarshal(b, &rec); err != nil {
				continue
			}
			if rec.Principal == caller.PrincipalID && rec.Delivery != nil {
				out = append(out, *rec.Delivery)
			}
		}
	}
	writeJSON(w, out)
}

// A mock chat belongs to exactly one principal, like a Telegram DM belongs
// to one account; the binding under spool/mock-links survives restarts.
func (g *Gateway) mockLinkOwner(chatID int64) (string, error) {
	b, err := os.ReadFile(filepath.Join(g.spool.root, "mock-links", strconv.FormatInt(chatID, 10)+".json"))
	if errors.Is(err, os.ErrNotExist) {
		return "", nil
	}
	if err != nil {
		return "", err
	}
	var link struct {
		Principal string `json:"principal"`
	}
	if err := json.Unmarshal(b, &link); err != nil {
		return "", err
	}
	return link.Principal, nil
}

func (g *Gateway) mockLinkClaim(chatID int64, principal string) error {
	return atomicJSON(filepath.Join(g.spool.root, "mock-links", strconv.FormatInt(chatID, 10)+".json"), map[string]string{"principal": principal})
}

// captureMock persists a completed delivery as the channel's durable record.
// Claim/complete still go through the real outbox, so retries and dedupe are
// exercised exactly like an external channel.
func (g *Gateway) captureMock(d *Delivery) error {
	owner, err := g.mockLinkOwner(d.ChatID)
	if err != nil {
		return err
	}
	return atomicJSON(filepath.Join(g.spool.root, "mock-sent", spoolFileID(d.ID)+".json"), map[string]any{"principal": owner, "delivery": d})
}

func (user User) mockEnvelope(chatID int64) identity.Envelope {
	runtimeID, policy := user.RuntimeID, user.PolicyVersion
	if runtimeID == "" {
		runtimeID = user.ID
	}
	if policy == "" {
		policy = envOr("HUB_POLICY_VERSION", "policy-1")
	}
	return identity.MockEnvelope(user.ID, chatID, runtimeID, policy)
}
