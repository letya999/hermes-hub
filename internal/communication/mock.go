package communication

import (
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

// The mock channel is the local acceptance bench: real pipeline, zero
// external APIs. Inbound goes through the same task binding and queue as a
// channel message; outbound deliveries are captured durably under
// spool/mock-sent for inspection instead of being sent anywhere.

// mockRoot locates mock-channel capture; HUB_MOCK_DIR relocates it out of the
// production spool for e2e/dev runs (SPEC-0053 R2.3).
func (g *Gateway) mockRoot() string {
	if g.config.MockDir != "" {
		return g.config.MockDir
	}
	return g.spool.root
}

// handleMockMessage injects one user message for the calling principal.
// client_id is the caller's idempotency handle — repeats never enqueue twice.
func (g *Gateway) handleMockMessage(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	caller, ok := g.authorizeControl(r)
	if !ok {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}
	var in struct {
		ChatID   int64      `json:"chat_id"`
		Text     string     `json:"text"`
		ClientID string     `json:"client_id"`
		Voice    *mockVoice `json:"voice,omitempty"`
	}
	r.Body = http.MaxBytesReader(w, r.Body, 12<<20)
	defer r.Body.Close()
	decoder := json.NewDecoder(r.Body)
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&in); err != nil {
		http.Error(w, "invalid mock message", http.StatusBadRequest)
		return
	}
	if decoder.Decode(new(any)) != io.EOF {
		http.Error(w, "invalid mock message", http.StatusBadRequest)
		return
	}
	if in.ChatID <= 0 || len(in.ClientID) > 128 || len(in.Text) > 64<<10 || (strings.TrimSpace(in.Text) == "" && in.Voice == nil) {
		http.Error(w, "invalid mock message", http.StatusBadRequest)
		return
	}
	if err := g.mockLinkClaim(in.ChatID, caller.PrincipalID); err != nil {
		http.Error(w, "forbidden", http.StatusForbidden)
		return
	}
	if in.Voice != nil {
		text, err := g.mockVoiceText(r.Context(), in.Voice, in.Text)
		if err != nil {
			http.Error(w, "voice preprocessing failed", http.StatusUnprocessableEntity)
			return
		}
		in.Text = text
	}
	user := g.user(caller.PrincipalID)
	task, err := g.spool.ResolveTask(user, in.ChatID, 0, true)
	if err != nil {
		http.Error(w, "task failed", http.StatusInternalServerError)
		return
	}
	key := "mock:" + caller.PrincipalID + ":" + in.ClientID
	if in.ClientID == "" {
		key += time.Now().UTC().Format("150405.000000000")
	}
	id := fmt.Sprintf("mock-%x", sha256.Sum256([]byte(key)))
	if in.ClientID == "" {
		id = "mock-" + newID()
	}
	// Slash commands run the same dispatcher as a Telegram command — the mock
	// channel is the real pipeline, so /model & co. never reach the agent.
	if strings.HasPrefix(in.Text, "/") {
		route := commandRoute{channel: "mock", chatID: in.ChatID, sender: in.ChatID}
		if err := g.dispatchChannelCommand(r.Context(), user, task, route, id, in.Text); err != nil {
			http.Error(w, "command failed", http.StatusInternalServerError)
			return
		}
		writeJSON(w, map[string]any{"job_id": id, "task_id": task.TaskID, "command": true})
		return
	}
	if err := g.enqueueChannelJob(r.Context(), user, task, "mock", id, key, in.ChatID, 0, in.Text, in.ChatID, ""); err != nil {
		http.Error(w, "enqueue failed", http.StatusInternalServerError)
		return
	}
	writeJSON(w, map[string]any{"job_id": id, "task_id": task.TaskID})
}

// handleMockOutbox lists the deliveries this principal's mock channel
// captured — the durable record of what the agent "sent".
func (g *Gateway) handleMockOutbox(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	caller, ok := g.authorizeControl(r)
	if !ok {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}
	entries, err := os.ReadDir(filepath.Join(g.mockRoot(), "mock-sent"))
	type snapshot struct {
		Delivery
		ArtifactData map[string][]byte `json:"artifact_data,omitempty"`
	}
	out := []snapshot{}
	if err == nil {
		for _, e := range entries {
			if e.IsDir() || !strings.HasSuffix(e.Name(), ".json") {
				continue
			}
			b, err := os.ReadFile(filepath.Join(g.mockRoot(), "mock-sent", e.Name()))
			if err != nil {
				continue
			}
			var rec struct {
				Principal    string            `json:"principal"`
				Delivery     *Delivery         `json:"delivery"`
				ArtifactData map[string][]byte `json:"artifact_data"`
			}
			if err := json.Unmarshal(b, &rec); err != nil {
				continue
			}
			if rec.Principal == caller.PrincipalID && rec.Delivery != nil {
				out = append(out, snapshot{Delivery: *rec.Delivery, ArtifactData: rec.ArtifactData})
			}
		}
	}
	writeJSON(w, out)
}

// A mock chat belongs to exactly one principal, like a Telegram DM belongs
// to one account; the binding under spool/mock-links survives restarts.
func (g *Gateway) mockLinkOwner(chatID int64) (string, error) {
	b, err := os.ReadFile(filepath.Join(g.mockRoot(), "mock-links", strconv.FormatInt(chatID, 10)+".json"))
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
	g.spool.mu.Lock()
	defer g.spool.mu.Unlock()
	owner, err := g.mockLinkOwner(chatID)
	if err != nil {
		return err
	}
	if owner != "" {
		if owner != principal {
			return errors.New("mock chat not owned")
		}
		return nil
	}
	return atomicJSON(filepath.Join(g.mockRoot(), "mock-links", strconv.FormatInt(chatID, 10)+".json"), map[string]string{"principal": principal})
}

// captureMock persists a completed delivery as the channel's durable record.
// Claim/complete still go through the real outbox, so retries and dedupe are
// exercised exactly like an external channel.
func (g *Gateway) captureMock(d *Delivery) error {
	owner, err := g.mockLinkOwner(d.ChatID)
	if err != nil {
		return err
	}
	if owner == "" {
		return errors.New("mock chat not owned")
	}
	artifacts := map[string][]byte{}
	for _, artifact := range d.Artifacts {
		data, err := g.spool.ReadDeliveryBlob(d.ID, artifact.Blob)
		if err != nil {
			return err
		}
		artifacts[artifact.Name] = data
	}
	return atomicJSON(filepath.Join(g.mockRoot(), "mock-sent", spoolFileID(d.ID)+".json"), map[string]any{"principal": owner, "delivery": d, "artifact_data": artifacts})
}
