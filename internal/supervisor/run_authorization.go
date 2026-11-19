package supervisor

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"time"

	contract "github.com/letya999/hermes-hub/contracts/identity/v1"
	task "github.com/letya999/hermes-hub/contracts/task/v1"
	"github.com/letya999/hermes-hub/internal/passportview"
	hubruntime "github.com/letya999/hermes-hub/internal/runtime"
)

func (m *Manager) runPolicy(ctx context.Context, binding Binding) (string, string, error) {
	registry, ok := m.identityClient().(interface {
		ActivePassports(context.Context, string, string) (string, error)
		BindPassports(context.Context, contract.TemplateRequest, string) (contract.SignedPolicy, contract.PolicyBundle, error)
	})
	if !ok {
		return "", "", nil
	}
	base, err := registry.ActivePassports(ctx, binding.ContextID, m.environment())
	if errors.Is(err, contract.ErrNotFound) {
		m.mu.Lock()
		// Serve opens the production registry before accepting requests.
		requiresPublication := m.ownedIdentity != nil
		m.mu.Unlock()
		if !requiresPublication {
			return "", "", nil
		}
	}
	if err != nil {
		return "", "", err
	}
	signed, policy, err := registry.BindPassports(ctx, contract.TemplateRequest{
		Subject: binding.SubjectID, Actor: binding.AgentID, Space: binding.ContextID,
	}, m.environment())
	if err != nil {
		return "", "", err
	}
	if policy.Identity == nil {
		return "", "", contract.ErrUnavailable
	}
	revision, err := policy.Identity.Revision()
	if err != nil || revision != binding.IdentityRevision || policy.PassportDigest != base {
		return "", "", contract.ErrConflict
	}
	if err := passportview.VerifyPolicyProjection(binding.ContextRoot, policy); err != nil {
		return "", "", err
	}
	return signed.Digest, base, nil
}

func (m *Manager) runDeliveryProof(record jobRecord) string {
	if record.PolicyRevision == "" {
		return ""
	}
	mac := hmac.New(sha256.New, []byte(m.cfg.RuntimeAuth))
	_, _ = io.WriteString(mac, "hub-runtime-run-delivery\x00"+record.AdmissionID+"\x00"+executeJobKey(record.Request)+"\x00"+record.Generation)
	return hex.EncodeToString(mac.Sum(nil))
}

func (m *Manager) deliverRunAuthorization(ctx context.Context, event hubruntime.ExecuteResponse, runtime Runtime, runtimeAuth string) error {
	m.mu.Lock()
	bound := m.jobs[event.JobID]
	m.mu.Unlock()
	proof := m.runDeliveryProof(bound)
	if proof == "" {
		return nil
	}
	record, err := m.Run(ctx, event.JobID)
	if err != nil || !record.LiveAt(time.Now()) || record.RunID != event.RunID || record.RuntimeGeneration != runtime.Generation {
		return contract.ErrForbidden
	}
	registry, ok := m.identityClient().(interface {
		IssueRun(context.Context, contract.RunClaims, time.Time, time.Duration) (string, error)
	})
	if !ok {
		return contract.ErrUnavailable
	}
	template, revision, err := m.identityTemplate(ctx, Binding{
		SubjectID: record.ActingIdentity.Subject, AgentID: record.ActingIdentity.Actor,
		OrganizationID: record.ActingIdentity.Organization, ScopeID: record.ScopeID,
		PrincipalID: record.Envelope.PrincipalID, ContextID: record.Envelope.ContextID,
	})
	if err != nil || revision != record.ActingIdentity.TemplateRevision {
		return contract.ErrForbidden
	}
	grant := hubruntime.RunAuthorization{JobID: record.JobID, RunID: record.RunID, Generation: record.RuntimeGeneration, Tokens: map[string]string{}}
	for _, audience := range []string{contract.ToolHubAudience, contract.MemoryAudience, contract.ModelAudience, contract.EgressAudience, contract.CommunicationAudience} {
		token, err := registry.IssueRun(ctx, task.ClaimsForRun(record, audience, template.Restrictions.Mode), record.Deadline, hubruntime.RunBudget())
		if err != nil {
			return err
		}
		grant.Tokens[audience] = token
	}
	body, err := json.Marshal(grant)
	if err != nil {
		return err
	}
	ctx, release := context.WithTimeout(ctx, 5*time.Second)
	defer release()
	req, err := http.NewRequestWithContext(ctx, http.MethodPut, runtime.Address+hubruntime.RunAuthorizationPath, bytes.NewReader(body))
	if err != nil {
		return contract.ErrUnavailable
	}
	req.Header.Set("Authorization", "Bearer "+runtimeAuth)
	req.Header.Set(hubruntime.RunDeliveryHeader, proof)
	req.Header.Set("Content-Type", "application/json")
	// A redirect must never carry the scoped grants or delivery proof to a
	// different endpoint, even on the same hostname.
	client := *m.cfg.HTTP
	client.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	response, err := client.Do(req)
	if err != nil {
		return contract.ErrUnavailable
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusNoContent {
		return contract.ErrUnavailable
	}
	return nil
}
