package toolhub

import (
	"encoding/json"
	"io"
	"net/http"
	"time"

	"github.com/letya999/hermes-hub/internal/identity"
	"github.com/letya999/hermes-hub/internal/memory"
	memorysearch "github.com/letya999/hermes-hub/internal/memory/search"
	"github.com/letya999/hermes-hub/internal/toolhub/api"
	"github.com/letya999/hermes-hub/internal/vault"
)

// serveMemoryContext supplies CORE and lane-1 recall to the supervisor after
// it has durably admitted and generation-bound a private job. The control
// bearer alone cannot select a vault: the job record and enrolled identity
// must agree before resolution.
func (g *Gateway) serveMemoryContext(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		w.WriteHeader(http.StatusMethodNotAllowed)
		return
	}
	if r.Header.Get("Origin") != "" {
		w.WriteHeader(http.StatusForbidden)
		return
	}
	auth, ok := g.controlPrincipal(r)
	if !ok {
		w.WriteHeader(http.StatusUnauthorized)
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, (2<<20)+(8<<10))
	decoder := json.NewDecoder(r.Body)
	decoder.DisallowUnknownFields()
	var claim api.MemoryContextRequest
	if decoder.Decode(&claim) != nil || decoder.Decode(new(any)) != io.EOF {
		w.WriteHeader(http.StatusBadRequest)
		return
	}
	result, code := g.memoryContext(r, auth, claim)
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

func (g *Gateway) memoryContext(r *http.Request, auth identity.Envelope, claim api.MemoryContextRequest) (api.MemoryContextResponse, int) {
	if !safeCaptureField(claim.JobID) || claim.AdmissionID == "" || len(claim.Text) > 2<<20 || g.Runs == nil {
		return api.MemoryContextResponse{}, http.StatusForbidden
	}
	record, err := g.Runs.Run(r.Context(), claim.JobID)
	if err != nil {
		return api.MemoryContextResponse{}, http.StatusServiceUnavailable
	}
	if record.JobID != claim.JobID || record.AdmissionID == "" || record.AdmissionID != claim.AdmissionID ||
		record.TextSHA256 != captureHash(claim.Text) || record.Status != "running" ||
		record.RuntimeGeneration == "" || record.RuntimeGeneration != record.CurrentGeneration ||
		!api.MayUseOwnerMemory(record) ||
		record.UserID != auth.PrincipalID || record.Environment != auth.Environment ||
		record.Envelope.PrincipalID != auth.PrincipalID || record.Envelope.ContextID != auth.ContextID ||
		record.Envelope.RuntimeID != auth.RuntimeID {
		return api.MemoryContextResponse{}, http.StatusForbidden
	}
	if err := memoryEnrollment(g.PassportRoot, auth.PrincipalID, auth.Environment); err != nil {
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
		if err := memoryEnrollment(g.PassportRoot, auth.PrincipalID, auth.Environment); err != nil {
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
