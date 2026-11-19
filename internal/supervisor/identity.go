package supervisor

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"

	contract "github.com/letya999/hermes-hub/contracts/identity/v1"
	"github.com/letya999/hermes-hub/internal/identity"
	"github.com/letya999/hermes-hub/internal/stack"
)

func (m *Manager) identityClient() contract.Client {
	if m.cfg.Identity != nil {
		return m.cfg.Identity
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.ownedIdentity
}

func (m *Manager) deactivateIdentity(w http.ResponseWriter, r *http.Request) {
	fail := func(status int, err error) {
		writeJSON(w, status, map[string]string{"error": err.Error()})
	}
	if r.Header.Get("Origin") != "" || !m.adminAuthorized(r) || m.cfg.AdminAuth == m.cfg.IdentityLinkAuth {
		fail(http.StatusForbidden, contract.ErrForbidden)
		return
	}
	if r.Method != http.MethodPost {
		fail(http.StatusMethodNotAllowed, contract.ErrInvalid)
		return
	}
	var request struct {
		PrincipalID string `json:"principal_id"`
		Environment string `json:"environment"`
	}
	d := json.NewDecoder(http.MaxBytesReader(w, r.Body, 4096))
	d.DisallowUnknownFields()
	if d.Decode(&request) != nil || d.Decode(new(any)) != io.EOF || !identity.ValidID(request.PrincipalID) || request.Environment != m.environment() {
		fail(http.StatusBadRequest, contract.ErrInvalid)
		return
	}
	registry, ok := m.identityClient().(interface {
		DeactivateHumanAsOperator(context.Context, string) error
	})
	if !ok {
		fail(http.StatusServiceUnavailable, contract.ErrUnavailable)
		return
	}
	if err := registry.DeactivateHumanAsOperator(r.Context(), request.PrincipalID); err != nil {
		if errors.Is(err, contract.ErrForbidden) {
			fail(http.StatusForbidden, contract.ErrForbidden)
		} else {
			fail(http.StatusServiceUnavailable, contract.ErrUnavailable)
		}
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "inactive"})
}

func (m *Manager) identityTemplate(ctx context.Context, binding Binding) (contract.AgentTemplate, string, error) {
	client := m.identityClient()
	if client == nil {
		return contract.AgentTemplate{}, "", contract.ErrUnavailable
	}
	subject := binding.SubjectID
	if subject == "" && !strings.HasPrefix(binding.ScopeID, "group:") {
		subject = binding.PrincipalID
	}
	if !strings.HasPrefix(binding.ScopeID, "group:") && subject != binding.PrincipalID {
		return contract.AgentTemplate{}, "", contract.ErrForbidden
	}
	actor := binding.AgentID
	if actor == "" {
		actor = "agent:" + subject + ":default"
	}
	value, err := contract.NewInProc(client).Template(ctx, contract.TemplateRequest{Subject: subject, Actor: actor, Space: binding.ContextID})
	if err != nil {
		return value, "", err
	}
	org := binding.OrganizationID
	if org == "personal" {
		org = ""
	}
	if value.OrganizationID != org {
		return value, "", contract.ErrForbidden
	}
	wantKind := "private"
	if strings.HasPrefix(binding.ScopeID, "group:") {
		wantKind = "shared"
	}
	if strings.HasPrefix(binding.ScopeID, "organization:") {
		wantKind = "organization"
	}
	if value.SpaceKind != wantKind {
		return value, "", contract.ErrForbidden
	}
	body, err := json.Marshal(value)
	if err != nil {
		return value, "", contract.ErrUnavailable
	}
	digest := sha256.Sum256(body)
	return value, "identity-" + hex.EncodeToString(digest[:]), nil
}

func (m *Manager) applyIdentityTemplate(binding Binding, settings stack.Settings) (stack.Settings, error) {
	value, revision, err := m.identityTemplate(context.Background(), binding)
	if err != nil {
		return settings, err
	}
	if binding.IdentityRevision != "" && revision != binding.IdentityRevision {
		return settings, contract.ErrConflict
	}
	settings.IdentityRestrictions = &value.Restrictions
	settings.IdentityRevision = revision
	return settings, nil
}
