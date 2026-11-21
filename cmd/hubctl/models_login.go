package main

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"sort"

	brokerv1 "github.com/letya999/credential-broker/api/v1"
	brokeridentity "github.com/letya999/credential-broker/identity"
	"github.com/letya999/hermes-hub/internal/credentialbroker"
)

// modelsLoginOnboardingID fits the broker's 40-char id budget: the readable
// "models-login-<service>-<kind>" when valid, else a deterministic hash form
// so long service names still enroll idempotently.
func modelsLoginOnboardingID(service, kind string) string {
	base := "models-login-" + service + "-" + kind
	if brokeridentity.ValidID(base) {
		return base
	}
	sum := sha256.Sum256([]byte(base))
	return "models-login-" + hex.EncodeToString(sum[:])[:16]
}

type modelsLoginGrant struct {
	GrantID          string `json:"grant_id,omitempty"`
	AuthorizationURL string `json:"authorization_url,omitempty"`
}

// modelsLoginProxy is the proxy_access key enrolled twice: once for the
// instance init companion (renders cliproxy client-auth) and once for
// model-relay (hop bearer) — the broker pins each dedicated grant to one
// workload identity, so the same credential takes two grants.
type modelsLoginProxy struct {
	Init             modelsLoginGrant `json:"init"`
	Relay            modelsLoginGrant `json:"relay"`
	AuthorizationURL string           `json:"authorization_url,omitempty"`
}

type modelsLoginReport struct {
	Session     modelsLoginGrant `json:"session"`
	ProxyAccess modelsLoginProxy `json:"proxy_access"`
}

type modelsBrokerClient interface {
	CreateRequest(ctx context.Context, in brokerv1.CreateRequest) (brokerv1.Request, error)
	Grant(ctx context.Context, credentialID string, in brokerv1.GrantRequest) (brokerv1.Grant, error)
}

// modelsLogin enrolls a cliproxy service's OAuth session and proxy_access
// key into Credential Broker: it mints an idempotent request per contract
// ref, grants each ready credential to its consumers — the session to the
// instance workload, the key to the instance's key slot and to the relay
// hop — records the grant ids in generated/model-grants.json and removes
// plaintext session files from the instance bind. control signs under the
// init companion's policy, relay under model-relay's: a grant pins its
// grantor's policy, so each consumer's lease needs its own dedicated grant.
func modelsLogin(ctx context.Context, dir, service string, control, relay modelsBrokerClient) (modelsLoginReport, error) {
	eff, err := effectiveSpec(ctx, dir)
	if err != nil {
		return modelsLoginReport{}, fmt.Errorf("models login: %w", err)
	}
	svc := eff.Models.Services[service]
	if svc.Auth.Upstream == nil || svc.Auth.ProxyAccess == nil {
		return modelsLoginReport{}, fmt.Errorf("models login: service %s is not a cliproxy session service", service)
	}
	u, err := url.Parse(svc.Endpoint)
	if err != nil || u.Hostname() == "" {
		return modelsLoginReport{}, fmt.Errorf("models login: service %s has no instance endpoint", service)
	}
	instance := u.Hostname()
	owner := eff.Header.Metadata.Space
	var grantMap map[string]map[string]map[string]string
	grantPath := filepath.Join(dir, "generated", "model-grants.json")
	if raw, err := os.ReadFile(grantPath); err == nil {
		_ = json.Unmarshal(raw, &grantMap)
	}
	enrollmentService := func(ref, contractID string) string {
		var candidates []string
		for name, refs := range grantMap {
			other := eff.Models.Services[name]
			if refs[ref] == nil || (contractID == "cliproxy-session" && (other.Auth.Upstream == nil || other.Auth.Upstream.Ref != ref)) || (contractID == "model-api-key" && (other.Auth.ProxyAccess == nil || other.Auth.ProxyAccess.Ref != ref)) {
				continue
			}
			candidates = append(candidates, name)
		}
		sort.Strings(candidates)
		if len(candidates) > 0 {
			return candidates[0]
		}
		return service
	}
	var report modelsLoginReport
	enroll := func(contractID, ref, kind string, out *modelsLoginGrant) (string, error) {
		requestService := enrollmentService(ref, contractID)
		request, err := control.CreateRequest(ctx, brokerv1.CreateRequest{
			ContractID: contractID, ContractRevision: 1, ConnectionID: ref,
			OnboardingID:   modelsLoginOnboardingID(requestService, kind),
			IdempotencyKey: "models-login:" + requestService + ":" + kind, OwnerKind: "user",
		})
		if err != nil {
			return "", fmt.Errorf("models login: %s request: %w", kind, err)
		}
		if request.Status != "ready" || request.CredentialID == "" {
			out.AuthorizationURL = request.AuthorizationURL
			return "", nil
		}
		return request.CredentialID, nil
	}
	grant := func(credentialID, contractID, kind, binding string, client modelsBrokerClient, out *modelsLoginGrant) error {
		// The slot is part of the key: moving a service between instances
		// re-grants the same credential without colliding with the old slot.
		idempotencyKey := "models-login:grant:" + service + ":" + kind + ":" + binding
		g, err := client.Grant(ctx, credentialID, brokerv1.GrantRequest{
			ContractID: contractID, ContractRevision: 1,
			PrincipalID: owner, ContextID: owner, RuntimeID: owner,
			BindingID: binding, WorkloadID: binding,
			Execution: "dedicated", IdempotencyKey: idempotencyKey,
		})
		if err != nil {
			return fmt.Errorf("models login: %s grant: %w", kind, err)
		}
		out.GrantID = g.ID
		return nil
	}
	sessionCred, err := enroll("cliproxy-session", svc.Auth.Upstream.Ref, "session", &report.Session)
	if err != nil {
		return report, err
	}
	keyCred, err := enroll("model-api-key", svc.Auth.ProxyAccess.Ref, "proxy_access", &report.ProxyAccess.Init)
	if err != nil {
		return report, err
	}
	if sessionCred != "" {
		if err := grant(sessionCred, "cliproxy-session", "session", credentialbroker.CliproxySessionBinding(instance, svc.Auth.Upstream.Ref), control, &report.Session); err != nil {
			return report, err
		}
	}
	if keyCred != "" {
		if err := grant(keyCred, "model-api-key", "proxy_access", credentialbroker.CliproxyKeyBinding(instance, svc.Auth.ProxyAccess.Ref), control, &report.ProxyAccess.Init); err != nil {
			return report, err
		}
		if err := grant(keyCred, "model-api-key", "proxy_access_relay", credentialbroker.ModelRelayBinding(owner, service), relay, &report.ProxyAccess.Relay); err != nil {
			return report, err
		}
	}
	if report.Session.GrantID == "" || report.ProxyAccess.Init.GrantID == "" || report.ProxyAccess.Relay.GrantID == "" {
		return report, nil
	}
	if grantMap == nil {
		grantMap = map[string]map[string]map[string]string{}
	}
	grantMap[service] = map[string]map[string]string{
		svc.Auth.Upstream.Ref:    {"init": report.Session.GrantID},
		svc.Auth.ProxyAccess.Ref: {"init": report.ProxyAccess.Init.GrantID, "relay": report.ProxyAccess.Relay.GrantID},
	}
	out, err := json.MarshalIndent(grantMap, "", "  ")
	if err != nil {
		return report, fmt.Errorf("models login: %w", err)
	}
	if err := os.WriteFile(grantPath, out, 0600); err != nil {
		return report, fmt.Errorf("models login: %w", err)
	}
	// Every seeded plaintext session file plus the api-key seed in the
	// instance bind is retired material once the grants exist.
	bind := filepath.Join(dir, "cliproxy", instance)
	if instance == "cliproxy-"+owner {
		bind = filepath.Join(dir, "cliproxy")
	}
	stale, err := filepath.Glob(filepath.Join(bind, "auths", "*.json"))
	if err != nil {
		return report, fmt.Errorf("models login: %w", err)
	}
	stale = append(stale, filepath.Join(bind, "api-key"))
	for _, path := range stale {
		if err := os.Remove(path); err != nil && !errors.Is(err, os.ErrNotExist) {
			return report, fmt.Errorf("models login: remove %s: %w", filepath.Base(path), err)
		}
	}
	return report, nil
}
