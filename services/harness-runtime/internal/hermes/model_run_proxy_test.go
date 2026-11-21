package hermes

import (
	"context"
	"crypto/ed25519"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	contract "github.com/letya999/hermes-hub/contracts/identity/v1"
	task "github.com/letya999/hermes-hub/contracts/task/v1"
	"github.com/letya999/hermes-hub/internal/identity"
	"github.com/letya999/hermes-hub/internal/toolhub/api"
)

type modelRunAuthority func(context.Context, string) (task.RunRecord, error)

func (f modelRunAuthority) Run(ctx context.Context, id string) (task.RunRecord, error) {
	return f(ctx, id)
}

type modelPolicyAuthority struct {
	contract.Client
	keys   []contract.PublicSigningKey
	policy contract.SignedPolicy
}

func (a modelPolicyAuthority) SigningKeys(context.Context) ([]contract.PublicSigningKey, error) {
	return a.keys, nil
}

func (a modelPolicyAuthority) Policy(_ context.Context, digest string) (contract.SignedPolicy, error) {
	if digest != a.policy.Digest {
		return contract.SignedPolicy{}, contract.ErrNotFound
	}
	return a.policy, nil
}

type servicePassportsFunc func(context.Context, string, string) (string, error)

func (f servicePassportsFunc) ActiveServicePassports(ctx context.Context, space, environment string) (string, error) {
	return f(ctx, space, environment)
}

func TestModelRelayServiceRequiresExactScopeAndCurrentPolicy(t *testing.T) {
	authority := modelPolicyAuthority{}
	mint := modelFixtureSigner(t, &authority)
	bundle := contract.PolicyBundle{Space: "alice", Environment: "dev", PassportRevision: "r-1", Passports: map[string]json.RawMessage{}}
	for _, name := range []string{"stack", "config", "models", "workspace", "tools", "grants"} {
		bundle.Passports[name] = json.RawMessage(fmt.Sprintf(`{"kind":%q,"metadata":{"space":"alice","environment":"dev","revision":"r-1"},"memory":{"enabled":true},"allow":{"models":["use"],"agents":["monitor_self"],"memory":["dreaming"]}}`, name))
	}
	canonical, digest, err := bundle.Canonical()
	if err != nil {
		t.Fatal(err)
	}
	authority.policy = contract.SignedPolicy{Digest: digest, JWS: mint(contract.SignPolicy, json.RawMessage(canonical))}
	for _, purpose := range []contract.SigningPurpose{contract.SignRun, contract.SignLink} {
		_ = mint(purpose, struct{}{})
	}
	const body = `{"model":"default","messages":[{"role":"user","content":"synthetic review"}]}`
	claims := contract.ServiceClaims{Issuer: contract.RunTokenIssuer, Audience: contract.PersonalModelAudience, Service: contract.PersonalModelPrincipal,
		Expires: time.Now().Add(time.Minute).Unix(), ServiceRequest: contract.ServiceRequest{Space: "alice", Environment: "dev", PolicyRevision: digest, RequestDigest: contract.PolicyDigest([]byte(body))}}
	token := mint(contract.SignService, claims)
	dreaming := claims
	dreaming.Audience, dreaming.Service = contract.DreamingModelAudience, contract.DreamingModelPrincipal
	dreamingToken := mint(contract.SignService, dreaming)
	dreaming.Service = contract.PersonalModelPrincipal
	mixedToken := mint(contract.SignService, dreaming)
	foreign, wrongAudience, expired := claims, claims, claims
	foreign.Space = "bob"
	wrongAudience.Audience = contract.MemoryContextServiceAudience
	expired.Expires = time.Now().Add(-time.Minute).Unix()
	foreignToken, wrongToken, expiredToken := mint(contract.SignService, foreign), mint(contract.SignService, wrongAudience), mint(contract.SignService, expired)
	var active atomic.Value
	active.Store(digest)
	reader := servicePassportsFunc(func(_ context.Context, space, environment string) (string, error) {
		if space != "alice" || environment != "dev" {
			return "", contract.ErrNotFound
		}
		return active.Load().(string), nil
	})
	const readAuth = "synthetic-verifier"
	mux := http.NewServeMux()
	mux.Handle(contract.ServicePassportsPath, contract.ServicePassportsHandler(reader, readAuth, "dev"))
	mux.Handle("/v1/identity/", contract.Handler(authority, readAuth))
	control := httptest.NewServer(mux)
	defer control.Close()
	t.Setenv("HUB_RUN_VERIFY_URL", control.URL)
	t.Setenv("HUB_RUN_VERIFY_AUTH", readAuth)
	t.Setenv("HUB_ENVIRONMENT", "dev")
	t.Setenv("HUB_MODEL_RELAY_AUTH", "synthetic-model")
	var monitorDenied, dreamingDenied atomic.Bool
	decider := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body map[string]string
		if r.URL.Path != "/v1/rights/decide" || r.Header.Get("Authorization") != "Bearer synthetic-decider" ||
			json.NewDecoder(r.Body).Decode(&body) != nil || body["principal_id"] != "alice" || body["environment"] != "dev" {
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		allowed := (body["capability"] == "agents" && body["operation"] == "monitor_self" && body["scope"] == "runtime" && !monitorDenied.Load()) ||
			(body["capability"] == "memory" && body["operation"] == "dreaming" && body["scope"] == "" && !dreamingDenied.Load()) ||
			(body["capability"] == "models" && body["operation"] == "use" && body["scope"] == "default")
		_ = json.NewEncoder(w).Encode(api.RightDecision{Allow: allowed})
	}))
	defer decider.Close()
	t.Setenv("HUB_TOOLHUB_URL", decider.URL)
	t.Setenv("HUB_TOOLHUB_CONTROL", "synthetic-decider")
	var calls atomic.Int32
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		if r.Header.Get(contract.ModelServiceTokenHeader) != "" || r.Header.Get("Authorization") != "" {
			t.Error("model provider received Hub service authority")
		}
		_, _ = io.WriteString(w, `{"choices":[]}`)
	}))
	defer upstream.Close()
	dir := t.TempDir()
	path := filepath.Join(dir, "models.yaml")
	if err := os.WriteFile(path, []byte("providers:\n  fixture:\n    url: "+upstream.URL+"\n    allow_private: true\nmodels:\n  default:\n    ladder: [{provider: fixture, model: one}]\n"), 0600); err != nil {
		t.Fatal(err)
	}
	relay, err := modelRelayRegistryHandler(path, filepath.Join(dir, "sel.json"), "alice", "")
	if err != nil {
		t.Fatal(err)
	}
	check := func(proof, payload string, headers map[string]string, status int, providerCalls int32) {
		t.Helper()
		req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(payload))
		req.Header.Set("Content-Type", "application/json")
		if proof != "" {
			req.Header.Set(contract.ModelServiceTokenHeader, proof)
		}
		for key, value := range headers {
			req.Header.Set(key, value)
		}
		out := httptest.NewRecorder()
		relay.ServeHTTP(out, req)
		if out.Code != status || calls.Load() != providerCalls {
			t.Fatalf("model service status=%d provider_calls=%d, want %d/%d", out.Code, calls.Load(), status, providerCalls)
		}
	}
	for _, proof := range []string{"", "forged", foreignToken, wrongToken, expiredToken, mixedToken} {
		check(proof, body, map[string]string{"Authorization": "Bearer synthetic-model"}, http.StatusForbidden, 0)
	}
	check(token, body+" ", nil, http.StatusForbidden, 0)
	check(token, body, map[string]string{api.HeaderRunID: "run-other"}, http.StatusForbidden, 0)
	check(token, body, nil, http.StatusOK, 1)
	monitorDenied.Store(true)
	check(token, body, nil, http.StatusForbidden, 1)
	monitorDenied.Store(false)
	check(dreamingToken, body+" ", nil, http.StatusForbidden, 1)
	check(dreamingToken, body, nil, http.StatusOK, 2)
	dreamingDenied.Store(true)
	check(dreamingToken, body, nil, http.StatusForbidden, 2)
	dreamingDenied.Store(false)
	active.Store(contract.PolicyDigest([]byte("changed policy")))
	check(token, body, nil, http.StatusForbidden, 2)
	check(dreamingToken, body, nil, http.StatusForbidden, 2)
}

func modelFixtureSigner(t *testing.T, authority *modelPolicyAuthority) func(contract.SigningPurpose, any) string {
	t.Helper()
	privateKeys := map[contract.SigningPurpose]ed25519.PrivateKey{}
	return func(purpose contract.SigningPurpose, claims any) string {
		t.Helper()
		private := privateKeys[purpose]
		if private == nil {
			public, generated, err := ed25519.GenerateKey(nil)
			if err != nil {
				t.Fatal(err)
			}
			key := contract.PublicSigningKey{ID: contract.SigningKeyID(public), Public: public, Purpose: purpose}
			authority.keys = append(authority.keys, key)
			private = generated
			privateKeys[purpose] = private
		}
		keyID := contract.SigningKeyID(private.Public().(ed25519.PublicKey))
		header, err := json.Marshal(contract.SignatureHeader{Algorithm: "EdDSA", Type: purpose.Type(), KeyID: keyID})
		if err != nil {
			t.Fatal(err)
		}
		body, err := json.Marshal(claims)
		if err != nil {
			t.Fatal(err)
		}
		encode := base64.RawURLEncoding.EncodeToString
		signingInput := encode(header) + "." + encode(body)
		return signingInput + "." + encode(ed25519.Sign(private, []byte(signingInput)))
	}
}

func TestModelRelayRequiresExactSignedRunBeforeProvider(t *testing.T) {
	dir := t.TempDir()
	authority := modelPolicyAuthority{}
	mint := modelFixtureSigner(t, &authority)
	const actor = "agent:alice:default"
	template := contract.AgentTemplate{Space: "alice", SpaceKind: "private", Restrictions: contract.Restrictions{Mode: "ask"},
		Subject: contract.Principal{ID: "alice", Kind: contract.Human}, Actor: contract.Principal{ID: actor, Kind: contract.Agent, OwnerID: "alice"}}
	bundle := contract.PolicyBundle{Space: "alice", Environment: "dev", PassportRevision: "r-1", Identity: &template,
		PassportDigest: contract.PolicyDigest([]byte("source")), Passports: map[string]json.RawMessage{}}
	for _, name := range []string{"stack", "config", "models", "workspace", "tools", "grants"} {
		bundle.Passports[name] = json.RawMessage(fmt.Sprintf(`{"kind":%q,"metadata":{"space":"alice","environment":"dev","revision":"r-1"},"allow":{"models":["use"]}}`, name))
	}
	canonical, digest, err := bundle.Canonical()
	if err != nil {
		t.Fatal(err)
	}
	authority.policy = contract.SignedPolicy{Digest: digest, JWS: mint(contract.SignPolicy, json.RawMessage(canonical))}
	templateRevision, err := template.Revision()
	if err != nil {
		t.Fatal(err)
	}
	job := task.RunRecord{JobID: "job-one", RunID: "run-one", RuntimeGeneration: "gen-one", CurrentGeneration: "gen-one", Environment: "dev",
		Status: "running", Channel: "telegram_bot", TaskID: "default", Deadline: time.Now().Add(time.Minute),
		ActingIdentity: task.ActingIdentity{Subject: "alice", Actor: actor, TemplateRevision: templateRevision},
		Envelope:       identity.TelegramEnvelope("alice", 7, "alice", "policy-1"), PolicyRevision: digest, PassportDigest: bundle.PassportDigest}
	claims := task.ClaimsForRun(job, contract.ModelAudience, template.Restrictions.Mode)
	claims.Expires = job.Deadline.Unix()
	token := mint(contract.SignRun, claims)
	claims.Audience = contract.EgressAudience
	egressToken := mint(contract.SignRun, claims)
	_ = mint(contract.SignLink, struct{}{})
	_ = mint(contract.SignService, struct{}{})
	var revoked atomic.Bool
	runs := modelRunAuthority(func(_ context.Context, id string) (task.RunRecord, error) {
		if id != job.JobID {
			return task.RunRecord{}, task.ErrNotFound
		}
		record := job
		record.Revoked = revoked.Load()
		return record, nil
	})
	const verifierCredential = "synthetic-verifier"
	mux := http.NewServeMux()
	mux.Handle("/v1/identity/", contract.Handler(authority, verifierCredential))
	mux.Handle("/v1/jobs/", task.Handler(runs, verifierCredential))
	control := httptest.NewServer(mux)
	defer control.Close()
	t.Setenv("HUB_RUN_VERIFY_URL", control.URL)
	t.Setenv("HUB_RUN_VERIFY_AUTH", verifierCredential)
	t.Setenv("HUB_ENVIRONMENT", "dev")
	t.Setenv("HUB_MODEL_RELAY_AUTH", "synthetic-model")
	t.Setenv("HUB_TOOLHUB_URL", "")
	var calls atomic.Int32
	var revokeDuringCall atomic.Bool
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get(contract.RunTokenHeader) != "" || r.Header.Get(contract.EgressRunTokenHeader) != "" || r.Header.Get(api.HeaderRunID) != "" || r.Header.Get(hermesModelRunHeader) != "" {
			t.Error("model provider received Hub authority headers")
		}
		calls.Add(1)
		if revokeDuringCall.Load() {
			revoked.Store(true)
			w.WriteHeader(http.StatusServiceUnavailable)
			return
		}
		_, _ = io.WriteString(w, `{"choices":[]}`)
	}))
	defer upstream.Close()
	var fallbackCalls atomic.Int32
	fallback := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		fallbackCalls.Add(1)
		_, _ = io.WriteString(w, `{"choices":[]}`)
	}))
	defer fallback.Close()
	path := filepath.Join(dir, "models.yaml")
	if err := os.WriteFile(path, []byte("providers:\n  fixture:\n    url: "+upstream.URL+"\n    allow_private: true\n  fallback:\n    url: "+fallback.URL+"\n    allow_private: true\nmodels:\n  default:\n    ladder: [{provider: fixture, model: one}, {provider: fallback, model: two}]\n"), 0600); err != nil {
		t.Fatal(err)
	}
	relay, err := modelRelayRegistryHandler(path, filepath.Join(dir, "sel.json"), "alice", "")
	if err != nil {
		t.Fatal(err)
	}
	relayServer := httptest.NewServer(relay)
	defer relayServer.Close()
	target, err := url.Parse(relayServer.URL)
	if err != nil {
		t.Fatal(err)
	}
	runtime := &runtimeHTTP{}
	if err := runtime.bindAuthorizedRun(job.JobID, job.RunID, strings.Repeat("a", 64), "session-test"); err != nil {
		t.Fatal(err)
	}
	access := runtime.runAccess[job.RunID]
	access.tokens = map[string]string{contract.ModelAudience: token}
	runtime.runAccess[job.RunID] = access
	proxy := runtime.modelRunProxy(target)
	call := func(handler http.Handler, runHeader, runID, bearer string) int {
		req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(`{"model":"default"}`))
		req.RemoteAddr = "127.0.0.1:2000"
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("Authorization", "Bearer synthetic-model")
		req.Header.Set(runHeader, runID)
		req.Header.Set(contract.RunTokenHeader, bearer)
		w := httptest.NewRecorder()
		handler.ServeHTTP(w, req)
		return w.Code
	}
	if code := call(proxy, hermesModelRunHeader, job.RunID, "injected-caller-token"); code != http.StatusOK || calls.Load() != 1 {
		t.Fatalf("signed runtime model status=%d provider_calls=%d", code, calls.Load())
	}
	for _, tc := range []struct{ run, token string }{{job.RunID, ""}, {"run-other", token}, {job.RunID, "forged"}} {
		if code := call(relay, api.HeaderRunID, tc.run, tc.token); code != http.StatusForbidden || calls.Load() != 1 {
			t.Fatalf("invalid model authority status=%d provider_calls=%d", code, calls.Load())
		}
	}
	revoked.Store(true)
	if code := call(proxy, hermesModelRunHeader, job.RunID, ""); code != http.StatusForbidden || calls.Load() != 1 {
		t.Fatalf("revoked model authority status=%d provider_calls=%d", code, calls.Load())
	}
	t.Run("egress rejects missing and cross-audience credentials", func(t *testing.T) {
		revoked.Store(false)
		backend, err := url.Parse(upstream.URL)
		if err != nil {
			t.Fatal(err)
		}
		egress := signedEgressRelayHandler(map[string]bool{backend.Hostname(): true}, "", "alice", relayRunAuthorityFromEnv())
		send := func(method, bearer, runID string) int {
			req := httptest.NewRequest(method, upstream.URL+"/fixture", nil)
			req.Header.Set(contract.EgressRunTokenHeader, bearer)
			req.Header.Set(api.HeaderRunID, runID)
			w := httptest.NewRecorder()
			egress.ServeHTTP(w, req)
			return w.Code
		}
		for _, tc := range []struct{ token, run string }{{"", job.RunID}, {token, job.RunID}, {egressToken, "foreign-run"}} {
			if code := send(http.MethodGet, tc.token, tc.run); code != http.StatusForbidden || calls.Load() != 1 {
				t.Fatalf("unbound egress reached the backend: status=%d calls=%d", code, calls.Load())
			}
		}
		if code := send(http.MethodPost, egressToken, job.RunID); code != http.StatusForbidden || calls.Load() != 1 {
			t.Fatalf("unattended egress write reached the backend: status=%d calls=%d", code, calls.Load())
		}
		if code := send(http.MethodGet, egressToken, job.RunID); code != http.StatusOK || calls.Load() != 2 {
			t.Fatalf("bound egress request refused: status=%d calls=%d", code, calls.Load())
		}
	})
	t.Run("revocation during model call prevents fallback", func(t *testing.T) {
		revokeDuringCall.Store(true)
		if code := call(relay, api.HeaderRunID, job.RunID, token); code != http.StatusForbidden || calls.Load() != 3 || fallbackCalls.Load() != 0 {
			t.Fatalf("fallback after revocation: status=%d primary=%d fallback=%d", code, calls.Load(), fallbackCalls.Load())
		}
	})
}
