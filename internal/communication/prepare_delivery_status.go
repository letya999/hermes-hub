package communication

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/letya999/hermes-hub/internal/identity"
	"github.com/letya999/hermes-hub/internal/toolhub/api"
)

func prepareDeliveryIdentity(caller identity.Envelope, onboardingID string) string {
	digest := sha256.Sum256([]byte(caller.PrincipalID + "\x00" + caller.ContextID + "\x00" + caller.RuntimeID + "\x00" + onboardingID))
	return hex.EncodeToString(digest[:])
}

// prepareDeliveryStatus reads the durable outbox for one owner's exact notice
// version and reports which channel holds the delivery, so a status answer
// names the surface the notice actually went through.
func (s *Spool) prepareDeliveryStatus(caller identity.Envelope, onboardingID string, version uint64) (string, string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	key := prepareDeliveryIdentity(caller, onboardingID)
	prefix := "prepare-" + onboardingID + "-"
	state, channel := api.PrepareDeliveryUnknown, ""
	for _, dir := range []string{"pending", "sending", "failed", "done"} {
		entries, err := s.fsys.ReadDir(filepath.Join(s.root, "outbox", dir))
		if err != nil {
			return "", "", err
		}
		for _, name := range entries {
			if !strings.HasPrefix(name, prefix) || !strings.HasSuffix(name, ".json") {
				continue
			}
			body, err := s.fsys.ReadFile(filepath.Join(s.root, "outbox", dir, name))
			if err != nil {
				if os.IsNotExist(err) { // A delivery can move while the read is in flight.
					continue
				}
				return "", "", err
			}
			var delivery Delivery
			if err := json.Unmarshal(body, &delivery); err != nil {
				return "", "", err
			}
			if delivery.PrepareKey != key || delivery.PrepareVersion != version {
				continue
			}
			channel = delivery.Channel
			switch dir {
			case "pending", "sending":
				state = api.PrepareDeliveryQueued
			case "failed":
				state = api.PrepareDeliveryUncertain
			case "done":
				return api.PrepareDeliveryDelivered, channel, nil
			}
		}
	}
	return state, channel, nil
}

func (g *Gateway) handlePrepareDeliveryStatus(w http.ResponseWriter, r *http.Request) {
	caller, ok := g.authorizeServiceControl(r)
	if !ok || r.Header.Get("Origin") != "" {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	id := r.URL.Query().Get("onboarding_id")
	version, err := strconv.ParseUint(r.URL.Query().Get("notice_version"), 10, 64)
	if !identity.ValidID(id) || err != nil || version == 0 {
		http.Error(w, "invalid prepare delivery status request", http.StatusBadRequest)
		return
	}
	status, channel, err := g.spool.prepareDeliveryStatus(caller, id, version)
	if err != nil {
		http.Error(w, "prepare delivery status unavailable", http.StatusServiceUnavailable)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]string{"delivery_status": status, "channel": channel})
}
