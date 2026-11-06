package toolhub

import (
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/letya999/hermes-hub/internal/identity"
	"github.com/letya999/hermes-hub/internal/memory"
	"github.com/letya999/hermes-hub/internal/toolhub/api"
	"github.com/letya999/hermes-hub/internal/vault"
)

// serveMemoryCapture is the single accepted-turn/completed-run fact writer
// (SPEC-0098 M4). Only the communication control-plane bearer plus
// X-Hub-Principal may select an enrolled identity.
func (g *Gateway) serveMemoryCapture(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	if r.Header.Get("Origin") != "" {
		http.Error(w, "browser origins forbidden", http.StatusForbidden)
		return
	}
	// A model bearer cannot mint a transport fact. Only the communication
	// service bearer may select an enrolled principal.
	principal := r.Header.Get("X-Hub-Principal")
	token := strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")
	ok := strings.HasPrefix(r.Header.Get("Authorization"), "Bearer ") && g.ControlToken != "" && subtle.ConstantTimeCompare([]byte(token), []byte(g.ControlToken)) == 1 && identity.ValidID(principal)
	if !ok {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}
	var auth identity.Envelope
	for attempt := 0; attempt < 2; attempt++ {
		ambiguous := false
		g.mu.Lock()
		for _, candidate := range g.Tokens {
			if candidate.PrincipalID == principal {
				if auth.PrincipalID != "" && (auth.Environment != candidate.Environment || auth.ContextID != candidate.ContextID || auth.RuntimeID != candidate.RuntimeID) {
					ambiguous = true
					break
				}
				auth = candidate
			}
		}
		g.mu.Unlock()
		if ambiguous {
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		if auth.PrincipalID != "" || !g.reloadTokens() {
			break
		}
	}
	if auth.PrincipalID == "" {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, (2<<20)+(8<<10))
	decoder := json.NewDecoder(r.Body)
	decoder.DisallowUnknownFields()
	var ev api.CaptureEvent
	if err := decoder.Decode(&ev); err != nil {
		http.Error(w, "invalid capture event", http.StatusBadRequest)
		return
	}
	var extra any
	if err := decoder.Decode(&extra); err != io.EOF {
		http.Error(w, "invalid capture event", http.StatusBadRequest)
		return
	}
	status, code := g.memoryCapture(r, auth, &ev)
	if code != http.StatusOK {
		http.Error(w, status, code)
		return
	}
	if g.Audit != nil {
		g.Audit("memory.capture", map[string]string{
			"principal": auth.PrincipalID, "kind": ev.Kind, "status": status,
		})
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]string{"status": status})
}

// memoryCapture resolves the authenticated principal's vault through the
// server-rendered bindings and commits the entry — kind, provenance and
// audience are classified from the event's fields, never trusted from the
// wire. Duplicate event IDs dedup durably inside the vault writer.
func (g *Gateway) memoryCapture(r *http.Request, auth identity.Envelope, ev *api.CaptureEvent) (string, int) {
	deny := func() (string, int) { return "capture denied", http.StatusForbidden }
	if ev.Producer != "communication" || ev.UserID != auth.PrincipalID || ev.PrincipalID != auth.PrincipalID ||
		ev.JobID == "" || ev.EventID != ev.Kind+"-"+ev.JobID ||
		(ev.Kind != "turn" && ev.Kind != "reply") || len(ev.Body) > 2<<20 ||
		ev.ObservedAt.IsZero() || ev.ObservedAt.After(time.Now().Add(5*time.Minute)) {
		return deny()
	}
	if g.Runs == nil {
		return "capture unavailable", http.StatusServiceUnavailable
	}
	record, err := g.Runs.Lookup(r.Context(), ev.JobID)
	if err != nil {
		return "capture unavailable", http.StatusServiceUnavailable
	}
	if record.JobID != ev.JobID || record.UserID != auth.PrincipalID ||
		record.Envelope.PrincipalID != auth.PrincipalID || record.Envelope.ContextID != auth.ContextID ||
		record.Envelope.RuntimeID != auth.RuntimeID ||
		record.Environment != auth.Environment || record.ScopeID != ev.ScopeID ||
		record.Trigger != ev.Trigger || record.Channel != ev.Channel ||
		record.Sensitive != ev.Sensitive || record.Untrusted != ev.Untrusted ||
		record.Assignment != ev.Assignment || record.Probe != ev.Probe ||
		!safeCaptureField(ev.JobID) || !safeCaptureField(ev.Channel) {
		return deny()
	}
	if ev.Sensitive {
		return "skipped", http.StatusOK
	}
	if ev.Kind == "reply" {
		if record.Status == "running" || record.Status == "uncertain" {
			return "capture unavailable", http.StatusServiceUnavailable
		}
		if record.Status != "completed" || ev.RunID == "" || ev.RunID != record.RunID ||
			captureHash(ev.Body) != record.ResponseSHA256 {
			return deny()
		}
	} else if ev.RunID != "" || captureHash(ev.Body) != record.TextSHA256 {
		return deny()
	}
	decision, err := g.Store.Decide(auth, "memory", "remember", "", time.Now())
	if err != nil {
		if errors.Is(err, api.ErrUnauthorized) {
			return deny()
		}
		return "capture unavailable", http.StatusServiceUnavailable
	}
	if !decision.Allow {
		return deny()
	}
	if g.Vaults == nil {
		return "capture unavailable", http.StatusServiceUnavailable
	}
	u, err := g.Vaults.Resolve(auth.PrincipalID, auth.Environment)
	if err != nil {
		if errors.Is(err, api.ErrUnauthorized) {
			return deny()
		}
		return "capture unavailable", http.StatusServiceUnavailable
	}
	provenance := memory.ProvenanceForTurn(ev.Untrusted, ev.Trigger)
	if ev.Kind == "reply" {
		provenance = memory.ProvenanceAgent
	}
	entry := memory.Entry{
		ID: ev.EventID, Kind: memory.KindForTrigger(ev.Trigger, ev.Assignment, ev.Probe), Provenance: provenance,
		Audience: memory.AudienceOf(ev.ScopeID), SourceRef: "job:" + ev.JobID,
		Body: ev.Body, ObservedAt: ev.ObservedAt,
	}
	if err := u.Locked(func(root string) error {
		if err := memoryEnrollment(g.PassportRoot, auth.PrincipalID, auth.Environment); err != nil {
			return err
		}
		decision, err := g.Store.Decide(auth, "memory", "remember", "", time.Now())
		if err != nil {
			return err
		}
		if !decision.Allow {
			return api.ErrUnauthorized
		}
		if err := memoryEnrollment(g.PassportRoot, auth.PrincipalID, auth.Environment); err != nil {
			return err
		}
		daily, err := vault.OpenDaily(root, time.Local)
		if err != nil {
			return err
		}
		return daily.Append(entry, ev.Channel)
	}); err != nil {
		if errors.Is(err, api.ErrUnauthorized) {
			return deny()
		}
		return "capture commit failed", http.StatusServiceUnavailable
	}
	return "captured", http.StatusOK
}

func captureHash(body string) string {
	sum := sha256.Sum256([]byte(body))
	return hex.EncodeToString(sum[:])
}

func safeCaptureField(s string) bool {
	if s == "" || len(s) > 256 {
		return false
	}
	for _, r := range s {
		if r != '-' && r != '_' && r != '.' && (r < '0' || r > '9') && (r < 'A' || r > 'Z') && (r < 'a' || r > 'z') {
			return false
		}
	}
	return true
}
