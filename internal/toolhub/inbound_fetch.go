package toolhub

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"path"
	"path/filepath"
	"strings"
	"time"

	"github.com/letya999/hermes-hub/internal/identity"
	"github.com/letya999/hermes-hub/internal/inboundfile"
	"github.com/letya999/hermes-hub/internal/toolhub/api"
)

// serveInboundFetch is the pre-run transport path for a staged owner file.
// inbox_fetch is internal and therefore deliberately absent from MCP tools/list.
func (g *Gateway) serveInboundFetch(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost || r.Header.Get("Origin") != "" {
		http.Error(w, "forbidden", http.StatusForbidden)
		return
	}
	token, ok := g.authenticate(r.Header.Get("Authorization"))
	if !ok {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}
	projection, err := g.projectionFor(token)
	if err != nil || projection.auth.PrincipalID != projection.auth.ContextID || projection.auth.CapabilityProfile == "" {
		http.Error(w, "forbidden", http.StatusForbidden)
		return
	}
	var body struct {
		ID string `json:"id"`
	}
	r.Body = http.MaxBytesReader(w, r.Body, 4096)
	decoder := json.NewDecoder(r.Body)
	decoder.DisallowUnknownFields()
	if decoder.Decode(&body) != nil || !validInboundFetchID(body.ID) {
		http.Error(w, "invalid inbound id", http.StatusBadRequest)
		return
	}
	var extra any
	if decoder.Decode(&extra) != io.EOF {
		http.Error(w, "invalid request", http.StatusBadRequest)
		return
	}
	auth := projection.auth
	activation, err := g.requireActivated(auth)
	if err != nil {
		http.Error(w, "forbidden", http.StatusForbidden)
		return
	}
	var result api.BackendResult
	err = g.authorizeInboundFetch(auth, func(effective api.EffectiveBinding, tool api.ToolSpec) error {
		if activation.revision != "" && effective.CapabilityProfileRevision != activation.profileRevision {
			return api.ErrUnauthorized
		}
		if err := g.checkActivePassport(auth, activation.revision); err != nil {
			return err
		}
		projected := api.ProjectedTool{BindingID: effective.Binding.ToolBindingID, Tool: tool}
		fields := auditFields(auth, projected, effective, "admitted")
		fields["job_id"], fields["tool_call_id"] = body.ID, api.NewToolCallID()
		if g.AuditWrite == nil || g.audit("admit", fields) != nil {
			return errors.New("inbound audit unavailable")
		}
		var callErr error
		if g.InboxRoots == "" {
			return errors.New("inbound control mount unavailable")
		}
		var materialized map[string]any
		materialized, callErr = inboundfile.MaterializeInbound(r.Context(), filepath.Join(g.InboxRoots, auth.PrincipalID),
			auth.PrincipalID, g.CommunicationURL, g.ControlToken, body.ID)
		result.Structured = materialized
		fields["outcome"] = "ok"
		fields["execution"] = "succeeded"
		if callErr != nil {
			fields["outcome"] = "backend-error"
			fields["execution"] = "unknown"
			_ = g.audit("uncertain", fields)
			return callErr
		}
		if result.IsError {
			fields["outcome"] = "backend-error"
			fields["execution"] = "failed"
		}
		return g.audit("allow", fields)
	})
	if errors.Is(err, api.ErrUnauthorized) {
		http.Error(w, "forbidden", http.StatusForbidden)
		return
	}
	if err != nil || result.IsError {
		http.Error(w, "inbound fetch failed", http.StatusBadGateway)
		return
	}
	data, ok := result.Structured.(map[string]any)
	rel, okPath := data["path"].(string)
	if !ok || !okPath || path.Clean(rel) != rel || (rel != "inbox/people/"+body.ID+"/files" && !strings.HasPrefix(rel, "inbox/people/"+body.ID+"/files/")) {
		http.Error(w, "invalid inbound receipt", http.StatusBadGateway)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]string{"path": rel})
}

func validInboundFetchID(id string) bool {
	if len(id) < 4 || len(id) > 32 || !strings.HasPrefix(id, "in-") {
		return false
	}
	for _, c := range id[3:] {
		if c < '0' || c > '9' {
			return false
		}
	}
	return true
}

// authorizeInboundFetch pins the current owner binding and workspace grant
// while the private executor materializes one staged file.
func (g *Gateway) authorizeInboundFetch(auth identity.Envelope, call func(api.EffectiveBinding, api.ToolSpec) error) error {
	if err := g.Store.Reload(); err != nil {
		return err
	}
	g.Store.mu.RLock()
	defer g.Store.mu.RUnlock()
	release, err := g.Store.lockCapabilityAdmission()
	if err != nil {
		return err
	}
	defer release()
	profile, policy, err := g.Store.capabilityProfileLocked(auth)
	if err != nil {
		return err
	}
	for _, selection := range profile.Selections {
		if selection.DefinitionID != "hub-agent-tools" {
			continue
		}
		for _, binding := range g.Store.bindings {
			if binding.DefinitionID != selection.DefinitionID || binding.DefinitionVersion != selection.DefinitionVersion || binding.ConnectionID != selection.ConnectionID {
				continue
			}
			effective, err := g.Store.resolveLocked(auth, binding.ToolBindingID)
			if err != nil || api.DefinitionDigest(effective.Definition) != selection.ImplementationDigest {
				continue
			}
			for _, tool := range effective.Definition.Tools {
				if tool.Name != "inbox_fetch" || tool.Audience != api.AudienceInternal || tool.CapabilityID != "inbox.fetch" || tool.Executor != "handler:files" {
					continue
				}
				if len(g.Store.rightsMissingLocked(auth, profile, tool, time.Now().UTC())) != 0 {
					return api.ErrUnauthorized
				}
				effective.CapabilityID = tool.CapabilityID
				effective.SelectedToolName = tool.Name
				effective.CapabilityProfileID, effective.CapabilityProfileRevision = profile.ProfileID, profile.Revision
				effective.CapabilityPolicyID, effective.CapabilityPolicyRevision = policy.PolicyID, policy.Revision
				effective.ImplementationDigest = selection.ImplementationDigest
				return call(effective, tool)
			}
		}
	}
	return fmt.Errorf("%w: internal inbox capability", api.ErrUnauthorized)
}
