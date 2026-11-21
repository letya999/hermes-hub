package toolhub

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"time"

	contract "github.com/letya999/hermes-hub/contracts/identity/v1"
	task "github.com/letya999/hermes-hub/contracts/task/v1"
	"github.com/letya999/hermes-hub/internal/identity"
	"github.com/letya999/hermes-hub/internal/memory"
	memorysearch "github.com/letya999/hermes-hub/internal/memory/search"
	"github.com/letya999/hermes-hub/internal/toolhub/api"
	"github.com/letya999/hermes-hub/internal/vault"
)

// serveMemoryContext supplies CORE and lane-1 recall to the supervisor after
// it has durably admitted and generation-bound a private job. Managed recall
// requires a control signature over the exact claim and the enrolled space.
func (g *Gateway) serveMemoryContext(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		w.WriteHeader(http.StatusMethodNotAllowed)
		return
	}
	if r.Header.Get("Origin") != "" {
		w.WriteHeader(http.StatusForbidden)
		return
	}
	var auth identity.Envelope
	if g.RunIdentity == nil {
		var ok bool
		auth, ok = g.controlPrincipal(r)
		if !ok {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
	}
	body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, (2<<20)+(8<<10)))
	if err != nil {
		w.WriteHeader(http.StatusBadRequest)
		return
	}
	decoder := json.NewDecoder(bytes.NewReader(body))
	decoder.DisallowUnknownFields()
	var claim api.MemoryContextRequest
	if decoder.Decode(&claim) != nil || decoder.Decode(new(any)) != io.EOF {
		w.WriteHeader(http.StatusBadRequest)
		return
	}
	if !safeCaptureField(claim.JobID) || claim.AdmissionID == "" || len(claim.Text) > 2<<20 || g.Runs == nil {
		w.WriteHeader(http.StatusForbidden)
		return
	}
	record, err := g.Runs.Run(r.Context(), claim.JobID)
	if err != nil {
		w.WriteHeader(http.StatusServiceUnavailable)
		return
	}
	if g.RunIdentity != nil {
		scope := contract.ServiceRequest{Space: record.Envelope.ContextID, Environment: record.Environment, RequestDigest: contract.PolicyDigest(body)}
		if !g.verifyMemoryService(r, contract.MemoryContextServiceAudience, "system:hub-control", scope) {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		projection := &projectionEndpoint{gateway: g}
		auth, err = projection.EnrolledAuth(record.Envelope.PrincipalID)
		if err != nil && g.reloadTokens() {
			auth, err = projection.EnrolledAuth(record.Envelope.PrincipalID)
		}
		if err != nil {
			w.WriteHeader(http.StatusForbidden)
			return
		}
	}
	result, code := g.memoryContext(r, auth, claim, record)
	if code != http.StatusOK {
		w.WriteHeader(code)
		return
	}
	if g.Audit != nil {
		g.Audit("memory.context", map[string]string{"principal": auth.PrincipalID, "job_id": claim.JobID, "status": result.Status})
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(result)
}

func (g *Gateway) memoryContext(r *http.Request, auth identity.Envelope, claim api.MemoryContextRequest, record task.RunRecord) (api.MemoryContextResponse, int) {
	if record.JobID != claim.JobID || record.AdmissionID == "" || record.AdmissionID != claim.AdmissionID ||
		record.TextSHA256 != captureHash(claim.Text) || record.Status != "running" || record.Revoked ||
		record.RuntimeGeneration == "" || record.RuntimeGeneration != record.CurrentGeneration ||
		!api.MayUseOwnerMemory(record) ||
		record.UserID != auth.PrincipalID || record.Environment != auth.Environment ||
		record.Envelope.PrincipalID != auth.PrincipalID || record.Envelope.ContextID != auth.ContextID ||
		record.Envelope.RuntimeID != auth.RuntimeID {
		return api.MemoryContextResponse{}, http.StatusForbidden
	}
	if g.RunIdentity != nil || record.PolicyRevision != "" {
		now := time.Now()
		policy, err := task.AdmissionPolicy(r.Context(), g.RunIdentity, record, now)
		if err != nil || !now.Before(record.Deadline) || !memoryPolicyAllows(policy, record.ScopeID) ||
			!policy.AllowsRight("memory", "read") || !policy.AllowsRight("memory", "search") {
			return api.MemoryContextResponse{}, http.StatusForbidden
		}
		if record.Assignment && (g.authorizeAssignmentRun == nil || g.authorizeAssignmentRun(record.Envelope, record.AssignmentID, record.AssignmentPlanRevision) != nil) {
			return api.MemoryContextResponse{}, http.StatusForbidden
		}
	}
	if err := memoryEnrollment(r.Context(), g.Spawn, auth.PrincipalID, auth.Environment); err != nil {
		return api.MemoryContextResponse{}, http.StatusForbidden
	}
	if code := g.memoryContextRights(auth); code != http.StatusOK {
		return api.MemoryContextResponse{}, code
	}
	if g.Vaults == nil {
		return api.MemoryContextResponse{}, http.StatusServiceUnavailable
	}
	u, err := g.Vaults.Resolve(auth.PrincipalID, auth.Environment)
	if err != nil {
		return api.MemoryContextResponse{}, http.StatusServiceUnavailable
	}
	result := api.MemoryContextResponse{Status: "ok"}
	if err := u.Locked(func(root string) error {
		if err := memoryEnrollment(r.Context(), g.Spawn, auth.PrincipalID, auth.Environment); err != nil {
			return err
		}
		if code := g.memoryContextRights(auth); code != http.StatusOK {
			return &memoryContextDenied{code: code}
		}
		result.Core = vault.CoreBlock(root)
		idx, _, err := memorysearch.NewIndexer(root).Ensure()
		if err != nil {
			result.Status = "degraded"
			result.Recall = memory.RecalledOpen + "\nMemory recall degraded this run; treat recalled context as unavailable.\n" + memory.RecalledClose
			return nil
		}
		result.Recall = memorysearch.Recall(idx, claim.Text, string(memory.AudiencePrivate), time.Now()).Text
		return nil
	}); err != nil {
		if denied, ok := err.(*memoryContextDenied); ok {
			return api.MemoryContextResponse{}, denied.code
		}
		return api.MemoryContextResponse{}, http.StatusServiceUnavailable
	}
	if code := g.memoryContextRights(auth); code != http.StatusOK {
		return api.MemoryContextResponse{}, code
	}
	return result, http.StatusOK
}

type memoryContextDenied struct{ code int }

func (e *memoryContextDenied) Error() string { return "memory context denied" }

// The endpoint returns CORE and recall together, so both path-scoped grants
// must cover the data it reads. A narrow search grant cannot expose the full
// index, and service authority does not substitute for either operation grant.
func (g *Gateway) memoryContextRights(auth identity.Envelope) int {
	if g.Store == nil {
		return http.StatusServiceUnavailable
	}
	for _, right := range []struct{ operation, scope string }{{"read", "memory/CORE.md"}, {"search", "memory"}} {
		decision, err := g.Store.Decide(auth, "memory", right.operation, right.scope, time.Now())
		if err != nil {
			return http.StatusServiceUnavailable
		}
		if !decision.Allow {
			return http.StatusForbidden
		}
	}
	return http.StatusOK
}
