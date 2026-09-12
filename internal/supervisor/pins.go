package supervisor

import (
	"context"
	"encoding/json"
	"errors"
	hubruntime "github.com/letya999/hermes-hub/internal/runtime"
	"net/http"
	"time"
)

func pinKey(request hubruntime.ExecuteRequest) string {
	return runtimeKey(Binding{PrincipalID: request.PrincipalID, ContextID: request.ContextID, RuntimeMode: "gateway"})
}

// SetPin records an explicit operator hold independent of a runtime generation.
// Removing a pin does not cancel jobs, release their leases or delete user data.
func (m *Manager) SetPin(request hubruntime.ExecuteRequest, enabled bool) error {
	binding, err := m.bindingFor(request)
	if err != nil {
		return err
	}
	if request.ActorID != request.PrincipalID {
		return errors.New("pin actor mismatch")
	}
	key := runtimeKey(binding)
	coordinator := m.lockFor("recovery:" + key)
	coordinator.Lock()
	defer coordinator.Unlock()
	lock := m.lockFor(key)
	lock.Lock()
	defer lock.Unlock()
	m.mu.Lock()
	defer m.mu.Unlock()
	previous, exists := m.pins[key]
	if exists && previous.Envelope != request.Envelope {
		return errors.New("pin ownership mismatch")
	}
	request.Text = ""
	if enabled {
		m.pins[key] = request
	} else {
		delete(m.pins, key)
	}
	if err := m.persistLocked(); err != nil {
		if exists {
			m.pins[key] = previous
		} else {
			delete(m.pins, key)
		}
		return err
	}
	return nil
}

func (m *Manager) pinHTTP(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeJSON(w, 405, map[string]string{"error": "method not allowed"})
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, 256*1024)
	defer r.Body.Close()
	var payload struct {
		hubruntime.ExecuteRequest
		Enabled *bool `json:"enabled"`
	}
	if json.NewDecoder(r.Body).Decode(&payload) != nil || payload.Enabled == nil {
		writeJSON(w, 400, map[string]string{"error": "invalid pin"})
		return
	}
	if err := m.SetPin(payload.ExecuteRequest, *payload.Enabled); err != nil {
		writeJSON(w, 409, map[string]string{"error": err.Error()})
		return
	}
	writeJSON(w, 200, map[string]bool{"enabled": *payload.Enabled})
}

func (m *Manager) ReconcilePins(ctx context.Context) {
	m.mu.Lock()
	requests := make([]hubruntime.ExecuteRequest, 0, len(m.pins))
	for _, request := range m.pins {
		requests = append(requests, request)
	}
	m.mu.Unlock()
	for _, request := range requests {
		binding, err := m.bindingFor(request)
		if err != nil {
			continue
		}
		key := runtimeKey(binding)
		coordinator := m.lockFor("recovery:" + key)
		coordinator.Lock()
		lock := m.lockFor(key)
		lock.Lock()
		m.mu.Lock()
		saved, exists := m.pins[key]
		entry := m.items[key]
		needed := exists && saved.Envelope == request.Envelope && (entry == nil || entry.restored || entry.State == Stopped || recoverableRuntime(entry.Runtime))
		if needed && entry != nil && recoverableRuntime(entry.Runtime) {
			entry.restored = true
		}
		m.mu.Unlock()
		lock.Unlock()
		if needed {
			callCtx, cancel := context.WithTimeout(ctx, 130*time.Second)
			lease, _, err := m.Acquire(callCtx, binding, LeaseLifecycle)
			if err == nil {
				_ = m.ReleaseLease(lease.ID)
			}
			cancel()
		}
		coordinator.Unlock()
	}
}
