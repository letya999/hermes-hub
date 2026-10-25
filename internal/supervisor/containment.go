package supervisor

import (
	"context"
	"crypto/subtle"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/letya999/hermes-hub/internal/identity"
)

// AdministrativeHold is lifecycle authority, never an LLM judgement or grant.
type AdministrativeHold struct {
	NoticeID      string    `json:"notice_id,omitempty"`
	NoticePending bool      `json:"notice_pending,omitempty"`
	PrincipalID   string    `json:"principal_id"`
	ContextID     string    `json:"context_id"`
	Active        bool      `json:"active"`
	Epoch         uint64    `json:"epoch"`
	Reason        string    `json:"reason"`
	UpdatedAt     time.Time `json:"updated_at"`
}

func holdKey(principal, context string) string { return principal + ":" + context }

func (m *Manager) operationallyCurrentLocked(record jobRecord) bool {
	hold := m.holds[holdKey(record.Request.PrincipalID, record.Request.ContextID)]
	return !hold.Active && record.OperationalEpoch == hold.Epoch
}

func (m *Manager) administrativeHold(w http.ResponseWriter, r *http.Request) {
	provided := r.Header.Get("Authorization")
	if m.cfg.AdminAuth == "" || m.cfg.AdminAuth == m.cfg.RuntimeAuth || subtle.ConstantTimeCompare([]byte(provided), []byte("Bearer "+m.cfg.AdminAuth)) != 1 {
		writeJSON(w, http.StatusForbidden, map[string]string{"error": "administrative authority required"})
		return
	}
	if r.Method != http.MethodPost && r.Method != http.MethodDelete {
		writeJSON(w, http.StatusMethodNotAllowed, map[string]string{"error": "method not allowed"})
		return
	}
	var request struct {
		PrincipalID   string `json:"principal_id"`
		ContextID     string `json:"context_id"`
		Reason        string `json:"reason"`
		ExpectedEpoch uint64 `json:"expected_epoch"`
	}
	r.Body = http.MaxBytesReader(w, r.Body, 4096)
	d := json.NewDecoder(r.Body)
	d.DisallowUnknownFields()
	if d.Decode(&request) != nil || d.Decode(new(any)) != io.EOF || !identity.ValidID(request.PrincipalID) || !identity.ValidID(request.ContextID) || request.Reason != "manual" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid hold request"})
		return
	}
	m.mu.Lock()
	key := holdKey(request.PrincipalID, request.ContextID)
	previous := m.holds[key]
	if r.Method == http.MethodDelete && (!previous.Active || request.ExpectedEpoch != previous.Epoch) {
		m.mu.Unlock()
		writeJSON(w, http.StatusConflict, map[string]string{"error": "read the current hold epoch before resume"})
		return
	}
	if r.Method == http.MethodDelete {
		for _, entry := range m.items {
			if entry.PrincipalID == request.PrincipalID && entry.ContextID == request.ContextID && entry.State != Stopped {
				m.mu.Unlock()
				writeJSON(w, http.StatusConflict, map[string]string{"error": "containment must complete before resume"})
				return
			}
		}
	}
	hold := AdministrativeHold{PrincipalID: request.PrincipalID, ContextID: request.ContextID, Active: r.Method == http.MethodPost, Epoch: previous.Epoch + 1, Reason: request.Reason, UpdatedAt: m.cfg.Now().UTC()}
	if !hold.Active {
		hold.NoticeID, hold.NoticePending = previous.NoticeID, previous.NoticePending
	}
	if hold.Active {
		if previous.Active {
			hold = previous
		} else {
			hold.NoticeID = fmt.Sprintf("hold-%s-%s-%d", hold.PrincipalID, hold.ContextID, hold.Epoch)
			hold.NoticePending = true
		}
	}
	m.holds[key] = hold
	previousJobs := map[string]jobRecord{}
	if hold.Active {
		for id, record := range m.jobs {
			if record.Request.PrincipalID != hold.PrincipalID || record.Request.ContextID != hold.ContextID || terminalRunStatus(record.Status) {
				continue
			}
			previousJobs[id] = record
			record.CancelRequested, record.Unconfirmed = true, true
			record.Status, record.UnconfirmedSince, record.UpdatedAt = "uncertain", hold.UpdatedAt, hold.UpdatedAt
			record.Response.Text = ""
			m.jobs[id] = record
		}
	}
	if err := m.persistLocked(); err != nil {
		m.holds[key] = previous
		for id, record := range previousJobs {
			m.jobs[id] = record
		}
		m.mu.Unlock()
		writeJSON(w, http.StatusServiceUnavailable, map[string]string{"error": "hold state unavailable"})
		return
	}
	m.mu.Unlock()
	if hold.Active {
		ctx, cancel := context.WithTimeout(context.WithoutCancel(r.Context()), 45*time.Second)
		defer cancel()
		if err := m.containHeldScope(ctx, hold); err != nil {
			writeJSON(w, http.StatusAccepted, map[string]any{"hold": hold, "containment": "pending"})
			return
		}
	}
	writeJSON(w, http.StatusOK, hold)
}

func (m *Manager) containHeldScope(ctx context.Context, hold AdministrativeHold) error {
	m.mu.Lock()
	keys := []string{}
	for key, entry := range m.items {
		if entry.PrincipalID == hold.PrincipalID && entry.ContextID == hold.ContextID && entry.State != Stopped {
			keys = append(keys, key)
		}
	}
	m.mu.Unlock()
	for _, key := range keys {
		lock := m.lockFor(key)
		if !lock.TryLock() {
			return errors.New("resource lifecycle busy")
		}
		m.mu.Lock()
		entry := m.items[key]
		if entry == nil || !m.holds[holdKey(hold.PrincipalID, hold.ContextID)].Active {
			m.mu.Unlock()
			lock.Unlock()
			continue
		}
		snapshot := entry.Runtime
		m.mu.Unlock()
		id, err := m.ownedContainerID(ctx, snapshot)
		if err != nil && !errors.Is(err, ErrRuntimeMissing) {
			lock.Unlock()
			return err
		}
		if err == nil {
			grace, cancel := context.WithTimeout(ctx, 30*time.Second)
			_, _ = m.command(grace, "stop", "--time", "25", id)
			cancel()
		}
		// Re-verify ownership before the bounded forced-removal fallback.
		if err := m.removeOwnedRuntime(ctx, snapshot); err != nil {
			lock.Unlock()
			return err
		}
		m.mu.Lock()
		previousLeases := entry.leases
		entry.State, entry.Leases, entry.IdleDeadline, entry.leases = Stopped, 0, time.Time{}, map[string]Lease{}
		if err := m.persistLocked(); err != nil {
			entry.Runtime, entry.leases = snapshot, previousLeases
			m.mu.Unlock()
			lock.Unlock()
			return err
		}
		m.releaseSlotLocked(key)
		m.mu.Unlock()
		lock.Unlock()
	}
	return nil
}

func (m *Manager) ackHoldNotice(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeJSON(w, 405, map[string]string{"error": "method not allowed"})
		return
	}
	var request struct {
		NoticeID string `json:"notice_id"`
	}
	r.Body = http.MaxBytesReader(w, r.Body, 4096)
	d := json.NewDecoder(r.Body)
	d.DisallowUnknownFields()
	if d.Decode(&request) != nil || d.Decode(new(any)) != io.EOF || request.NoticeID == "" || len(request.NoticeID) > 128 || strings.ContainsAny(request.NoticeID, "/\\\r\n") {
		writeJSON(w, 400, map[string]string{"error": "invalid notice acknowledgement"})
		return
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	for key, hold := range m.holds {
		if hold.NoticeID != request.NoticeID {
			continue
		}
		previous := hold
		hold.NoticePending = false
		m.holds[key] = hold
		if err := m.persistLocked(); err != nil {
			m.holds[key] = previous
			writeJSON(w, 503, map[string]string{"error": "notice state unavailable"})
			return
		}
		writeJSON(w, 200, map[string]bool{"queued": true})
		return
	}
	writeJSON(w, 409, map[string]string{"error": "unknown notice"})
}
