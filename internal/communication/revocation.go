package communication

import (
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/letya999/hermes-hub/internal/identity"
)

// PrincipalRevocation is the durable per-stage result returned by the
// service-control revocation boundary. Counts name records moved or marked,
// never deleted evidence.
type PrincipalRevocation struct {
	Jobs       int `json:"jobs,omitempty"`
	Deliveries int `json:"deliveries,omitempty"`
}

// RevokePrincipalJobs durably removes one principal/context's queued and
// claimed work. Unadmitted records become terminal; admitted/claimed records
// remain uncertain until the owning runtime reports the real outcome.
func (s *Spool) RevokePrincipalJobs(principalID, contextID string) (int, error) {
	if !identity.ValidID(principalID) || !identity.ValidID(contextID) {
		return 0, errors.New("invalid revocation scope")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	count := 0
	for _, dir := range []string{"pending", "running"} {
		names, err := s.fsys.ReadDir(filepath.Join(s.root, dir))
		if err != nil {
			return count, err
		}
		for _, name := range names {
			if !strings.HasSuffix(name, ".json") {
				continue
			}
			oldPath := filepath.Join(s.root, dir, name)
			body, err := s.fsys.ReadFile(oldPath)
			if err != nil {
				return count, err
			}
			var job Job
			if json.Unmarshal(body, &job) != nil || job.PrincipalID != principalID || job.ContextID != contextID {
				continue
			}
			mapping, err := s.loadMappingLocked(job.ID)
			if errors.Is(err, os.ErrNotExist) {
				mapping = mappingFromJob(job, job.CreatedAt.UTC())
				mapping.JobID = job.ID
			} else if err != nil {
				return count, err
			}
			admitted := mapping.RunID != "" && mapping.SessionID != "" && mapping.RuntimeGeneration != ""
			mapping.CancelRequested = true
			mapping.UpdatedAt = time.Now().UTC()
			claimed := admitted || dir == "running"
			if claimed {
				if !terminalStatus(mapping.Status) {
					mapping.Status = "uncertain"
					if mapping.UncertainSince.IsZero() {
						mapping.UncertainSince = mapping.UpdatedAt
					}
					mapping.LastKnownEvent = "run.unknown"
					mapping.TerminalAt = time.Time{}
				}
			} else if !terminalStatus(mapping.Status) {
				mapping.Status = "cancelled"
				mapping.TerminalAt = mapping.UpdatedAt
				mapping.LastKnownEvent = "job.cancelled"
			}
			if err := s.writeMappingLocked(mapping); err != nil {
				return count, err
			}
			if terminalStatus(mapping.Status) {
				if err := s.settleReservationLocked(job.ID); err != nil {
					return count, err
				}
			}
			if !claimed {
				s.releaseInFlightLocked(job.ID)
				if err := s.fsys.Rename(oldPath, filepath.Join(s.root, "failed", name)); err != nil {
					return count, err
				}
			} else if dir == "pending" {
				// An admitted pending record is a requeued observation. Moving it to
				// running keeps the execution lane closed while its durable binding
				// remains available for reconciliation and evidence.
				if err := s.fsys.Rename(oldPath, filepath.Join(s.root, "running", name)); err != nil {
					return count, err
				}
			}
			count++
		}
	}
	return count, nil
}

// RevokePrincipalDeliveries moves every live outbox record owned by the
// principal/context to superseded. An in-flight sender may already have made
// the provider call; the durable file records the intended terminal state.
func (s *Spool) RevokePrincipalDeliveries(principalID, contextID string) (int, error) {
	if !identity.ValidID(principalID) || !identity.ValidID(contextID) {
		return 0, errors.New("invalid revocation scope")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	count := 0
	for _, dir := range []string{filepath.Join("outbox", "pending"), filepath.Join("outbox", "sending")} {
		names, err := s.fsys.ReadDir(filepath.Join(s.root, dir))
		if err != nil {
			return count, err
		}
		for _, name := range names {
			if !strings.HasSuffix(name, ".json") {
				continue
			}
			oldPath := filepath.Join(s.root, dir, name)
			body, err := s.fsys.ReadFile(oldPath)
			if err != nil {
				return count, err
			}
			var delivery Delivery
			if json.Unmarshal(body, &delivery) != nil || !s.deliveryOwnedLocked(delivery, principalID, contextID) {
				continue
			}
			if err := s.fsys.Rename(oldPath, filepath.Join(s.root, "outbox", "superseded", name)); err != nil {
				return count, err
			}
			count++
		}
	}
	return count, nil
}

// deliveryOwnedLocked resolves a live delivery's owning scope through its
// exact durable bindings: a job/approval reference — or the status-message id
// convention — reads the admission mapping; a standalone record matches only
// the stamped (principal, context) pair. An unresolvable binding falls back
// to the owner pair, and a record with no binding and no pair never matches.
func (s *Spool) deliveryOwnedLocked(delivery Delivery, principalID, contextID string) bool {
	for _, jobID := range []string{delivery.JobID, delivery.ApprovalJobID, statusDeliveryJobID(delivery.ID)} {
		if jobID == "" {
			continue
		}
		if mapping, err := s.loadMappingLocked(jobID); err == nil {
			return mapping.PrincipalID == principalID && mapping.ContextID == contextID
		}
	}
	return delivery.OwnerPrincipalID == principalID && delivery.OwnerContextID == contextID
}

// servePrincipalRevocation is the service-only bulk boundary used by the
// operator revocation coordinator. The existing service bearer, not the
// target principal, authorizes the stage.
func (g *Gateway) servePrincipalRevocation(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	if r.Header.Get("Origin") != "" {
		http.Error(w, "browser origin forbidden", http.StatusForbidden)
		return
	}
	token := strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")
	if !g.serviceControlTokenAccepted(token) {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}
	var body struct {
		PrincipalID string `json:"principal_id"`
		ContextID   string `json:"context_id"`
		Stage       string `json:"stage"`
	}
	d := json.NewDecoder(http.MaxBytesReader(w, r.Body, 64<<10))
	d.DisallowUnknownFields()
	if err := d.Decode(&body); err != nil || d.Decode(new(any)) != io.EOF || !identity.ValidID(body.PrincipalID) || !identity.ValidID(body.ContextID) || body.Stage != "jobs" && body.Stage != "delivery" {
		http.Error(w, "invalid revocation request", http.StatusBadRequest)
		return
	}
	var result PrincipalRevocation
	var err error
	switch body.Stage {
	case "jobs":
		result.Jobs, err = g.spool.RevokePrincipalJobs(body.PrincipalID, body.ContextID)
	case "delivery":
		result.Deliveries, err = g.spool.RevokePrincipalDeliveries(body.PrincipalID, body.ContextID)
	}
	if err != nil {
		http.Error(w, "revocation failed", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(result)
}
