package secrets

import (
	"context"
	"crypto/rand"
	"encoding/json"
	"errors"
	"github.com/letya999/hermes-hub/internal/toolhub/api"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/letya999/hermes-hub/internal/audit"
	"github.com/letya999/hermes-hub/internal/credstore"
	"github.com/letya999/hermes-hub/internal/envstore"
	"github.com/letya999/hermes-hub/internal/identity"
	"github.com/letya999/hermes-hub/internal/locale"
	"github.com/letya999/hermes-hub/internal/toolhub"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

func newService(t *testing.T) *Service {
	t.Helper()
	key, err := credstore.GenerateKey()
	if err != nil {
		t.Fatal(err)
	}
	backend, err := credstore.Open(credstore.Options{Path: filepath.Join(t.TempDir(), "store.enc"), Key: key})
	if err != nil {
		t.Fatal(err)
	}
	ledger, err := audit.Open(filepath.Join(t.TempDir(), "audit.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	return &Service{Backend: backend, Audit: ledger}
}

func randomValue(t *testing.T) string {
	t.Helper()
	raw := make([]byte, 12)
	if _, err := rand.Read(raw); err != nil {
		t.Fatal(err)
	}
	const hexdigits = "0123456789abcdef"
	out := make([]byte, 24)
	for i, b := range raw {
		out[i*2] = hexdigits[b>>4]
		out[i*2+1] = hexdigits[b&0x0f]
	}
	return "s-" + string(out)
}

func TestSetListDeleteOmitsValues(t *testing.T) {
	svc := newService(t)
	secret := randomValue(t)
	infos, err := svc.Set("alice", map[string]string{"GOOGLE_TOKEN": secret})
	if err != nil || len(infos) != 1 || infos[0].Name != "GOOGLE_TOKEN" || infos[0].Status != credstore.StatusActive {
		t.Fatalf("set=%+v err=%v", infos, err)
	}
	listed, err := svc.List("alice")
	if err != nil || len(listed) != 1 || listed[0].Name != "GOOGLE_TOKEN" {
		t.Fatal(listed, err)
	}
	status := FormatStatus(locale.Operator(), []string{listed[0].Name}, listed[0].Status)
	if strings.Contains(status, secret) || ContainsValue(status, map[string]string{"GOOGLE_TOKEN": secret}) {
		t.Fatal(status)
	}
	bob, err := svc.List("bob")
	if err != nil || len(bob) != 0 {
		t.Fatalf("cross-user list=%+v", bob)
	}
	if err := svc.Delete("alice", "GOOGLE_TOKEN"); err != nil {
		t.Fatal(err)
	}
	listed, err = svc.List("alice")
	if err != nil || len(listed) != 0 {
		t.Fatalf("deleted still listed=%+v err=%v", listed, err)
	}
}

// The operator-facing status line localizes its labels while the status
// token itself stays a stable machine word.
func TestFormatStatusLocalized(t *testing.T) {
	want := map[locale.Lang]string{
		locale.LangEN: "Names: A, B. Status: active.",
		locale.LangRU: "Имена: A, B. Статус: active.",
		locale.LangKK: "Аттары: A, B. Күйі: active.",
		locale.LangES: "Nombres: A, B. Estado: active.",
	}
	for lang, wantText := range want {
		if got := FormatStatus(lang, []string{"B", "A"}, "active"); got != wantText {
			t.Errorf("%s: %q", lang, got)
		}
	}
	if got := FormatStatus(locale.LangEN, nil, "key-rotated"); got != "Status: key-rotated." {
		t.Fatalf("bare status: %q", got)
	}
	if got := FormatStatus(locale.LangRU, nil, "rejected"); got != "Статус: rejected." {
		t.Fatalf("bare status ru: %q", got)
	}
}

// ApplyChat's rejection status follows the operator language, not a fixed
// language literal.
func TestApplyChatRejectionLocalized(t *testing.T) {
	svc := newService(t)
	t.Setenv("HUB_LANG", "en")
	_, message, err := svc.ApplyChat("alice", "=x")
	if err == nil || message != "Status: rejected." {
		t.Fatalf("en rejection=%q err=%v", message, err)
	}
	t.Setenv("HUB_LANG", "ru")
	_, message, _ = svc.ApplyChat("alice", "=x")
	if message != "Статус: rejected." {
		t.Fatalf("ru rejection=%q", message)
	}
	_, message, _ = svc.ApplyChat("alice", "   ")
	if message != "Статус: rejected." {
		t.Fatalf("empty rejection=%q", message)
	}
	if _, message, _ = (*Service)(nil).ApplyChat("alice", "GOOGLE_TOKEN=x"); message != "Статус: rejected." {
		t.Fatalf("backend rejection=%q", message)
	}
}

func TestChatInterceptStatusOnly(t *testing.T) {
	svc := newService(t)
	secret := randomValue(t)
	names, message, err := svc.ApplyChat("alice", "GOOGLE_TOKEN="+secret)
	if err != nil || len(names) != 1 || names[0] != "GOOGLE_TOKEN" || strings.Contains(message, secret) {
		t.Fatalf("names=%v message=%q err=%v", names, message, err)
	}
}

func TestInjectAfterAuthorizeAndIsolation(t *testing.T) {
	svc := newService(t)
	secret := randomValue(t)
	infos, err := svc.Set("alice", map[string]string{"GOOGLE_TOKEN": secret})
	if err != nil {
		t.Fatal(err)
	}
	registry, auth, binding := seededRegistry(t, infos[0].Locator)
	svc.Registry = registry
	root := t.TempDir()
	projected := api.ProjectedToolName("google-work", "1.0.0", "search")
	injected, err := svc.Inject(auth, projected, root, "")
	if err != nil || injected.Env["GOOGLE_TOKEN"] != secret {
		t.Fatalf("inject=%+v err=%v", injected, err)
	}
	body, err := os.ReadFile(filepath.Join(root, "per-user", "alice", "alice", "google-work", api.CredentialReferenceID("google-work", 0), "credentials.env"))
	if err != nil || !strings.Contains(string(body), secret) {
		t.Fatalf("workload file=%s err=%v", body, err)
	}
	bob := identity.TelegramEnvelope("bob", 9, "runtime-bob", "policy-1")
	if _, err := svc.Inject(bob, projected, t.TempDir(), ""); !errors.Is(err, api.ErrUnauthorized) && !errors.Is(err, api.ErrNotFound) {
		t.Fatalf("cross-user inject: %v", err)
	}
	if err := injected.Wipe(); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(root, "per-user", "alice", "alice", "google-work", api.CredentialReferenceID("google-work", 0), "credentials.env")); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("injected file survived wipe")
	}
	catalog, err := registry.Catalog(auth)
	if err != nil {
		t.Fatal(err)
	}
	encoded, err := json.Marshal(catalog)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(encoded), secret) {
		t.Fatal("catalog leaked secret material")
	}
	path := filepath.Join(t.TempDir(), "toolhub.json")
	if err := registry.Save(path); err != nil {
		t.Fatal(err)
	}
	snapshot, _ := os.ReadFile(path)
	if strings.Contains(string(snapshot), secret) {
		t.Fatal("toolhub snapshot contained plaintext")
	}
	_ = binding
}

func TestSharedStaticCredentialRejectedAtInject(t *testing.T) {
	svc := newService(t)
	secret := randomValue(t)
	infos, err := svc.Set("alice", map[string]string{"GOOGLE_TOKEN": secret})
	if err != nil {
		t.Fatal(err)
	}
	store := toolhub.NewStore()
	definition := api.ToolDefinition{
		Schema: api.SchemaVersion, DefinitionID: "google-work", Version: "1.0.0", Transport: api.RemoteMCP,
		Source:      api.DefinitionSource{URL: "https://api.example.com/mcp", TLSMode: "required"},
		Tools:       []api.ToolSpec{{Name: "search", Effect: api.ReadEffect}},
		Credentials: []api.CredentialInput{{Name: "GOOGLE_TOKEN", Required: true, PerRequest: false}},
		Workload:    api.WorkloadPolicy{Class: api.Shared, Rationale: "should be rejected"},
		Execution:   api.ExecutionPolicy{TimeoutSeconds: 30, OutputBytes: 1 << 20, CPUMillis: 500, MemoryMiB: 256, MaxPIDs: 32, Egress: []string{"api.example.com"}},
		Health:      api.HealthProbe{Kind: "http", Value: "/health", TimeoutSeconds: 5},
	}
	if err := store.RegisterDefinition(definition); err == nil {
		t.Fatal("shared static credential definition accepted")
	}
	_ = infos
}

func TestTerminalExposureIsReversible(t *testing.T) {
	svc := newService(t)
	secret := randomValue(t)
	envPath := filepath.Join(t.TempDir(), envstore.FileName)
	svc.EnvFile = func(string) string { return envPath }
	if _, err := svc.Set("alice", map[string]string{"GOOGLE_TOKEN": secret}); err != nil {
		t.Fatal(err)
	}
	if err := svc.SetTerminalExposure("alice", "GOOGLE_TOKEN", true); err != nil {
		t.Fatal(err)
	}
	values, err := envstore.Load(envPath, "GOOGLE_TOKEN", "")
	if err != nil || values["GOOGLE_TOKEN"] != secret {
		t.Fatal(values, err)
	}
	if err := svc.SetTerminalExposure("alice", "GOOGLE_TOKEN", false); err != nil {
		t.Fatal(err)
	}
	values, err = envstore.Load(envPath, "GOOGLE_TOKEN", "")
	if err != nil || values["GOOGLE_TOKEN"] != "" {
		t.Fatalf("terminal exposure not reversed: %v err=%v", values, err)
	}
}

func TestOpenNilAndErrorPaths(t *testing.T) {
	key, err := credstore.GenerateKey()
	if err != nil {
		t.Fatal(err)
	}
	keyFile := filepath.Join(t.TempDir(), "key")
	if err := credstore.WriteKeyFile(keyFile, key); err != nil {
		t.Fatal(err)
	}
	svc, err := Open(filepath.Join(t.TempDir(), "store.enc"), keyFile, nil, "")
	if err != nil || svc == nil {
		t.Fatal(err)
	}
	if _, err := (*Service)(nil).Set("alice", map[string]string{"GOOGLE_TOKEN": "x"}); !errors.Is(err, ErrNotConfigured) {
		t.Fatalf("nil set: %v", err)
	}
	if _, _, err := svc.ApplyChat("alice", "not a secret"); err == nil {
		t.Fatal("invalid chat accepted")
	}
	if _, err := svc.Inject(identity.TelegramEnvelope("alice", 1, "runtime", "policy-1"), "hub-x", t.TempDir(), ""); !errors.Is(err, ErrNotConfigured) {
		t.Fatalf("inject without registry: %v", err)
	}
	secret := randomValue(t)
	if _, err := svc.Set("alice", map[string]string{"GOOGLE_TOKEN": secret}); err != nil {
		t.Fatal(err)
	}
	if err := svc.Rotate("alice", "GOOGLE_TOKEN", randomValue(t), ""); err != nil {
		t.Fatal(err)
	}
	if !ContainsValue("prefix"+secret+"suffix", map[string]string{"GOOGLE_TOKEN": secret}) {
		t.Fatal("contains value missed")
	}
	envPath := filepath.Join(t.TempDir(), envstore.FileName)
	svc.EnvFile = func(string) string { return envPath }
	if err := svc.SetTerminalExposure("alice", "GOOGLE_TOKEN", true); err != nil {
		t.Fatal(err)
	}
	if err := svc.Delete("alice", "GOOGLE_TOKEN"); err != nil {
		t.Fatal(err)
	}
	if err := svc.Delete("alice", "MISSING"); !errors.Is(err, credstore.ErrNotFound) {
		t.Fatalf("missing delete: %v", err)
	}
}

func TestInjectMissingCiphertextFailsClosed(t *testing.T) {
	svc := newService(t)
	registry, auth, _ := seededRegistry(t, "loc-missing00000000000000000000")
	svc.Registry = registry
	projected := api.ProjectedToolName("google-work", "1.0.0", "search")
	if _, err := svc.Inject(auth, projected, t.TempDir(), ""); err == nil {
		t.Fatal("missing ciphertext injected")
	}
}

func TestRotateSucceedsAgainstRegistry(t *testing.T) {
	svc := newService(t)
	secret := randomValue(t)
	infos, err := svc.Set("alice", map[string]string{"GOOGLE_TOKEN": secret})
	if err != nil {
		t.Fatal(err)
	}
	registry, auth, binding := seededRegistry(t, infos[0].Locator)
	svc.Registry = registry
	if err := svc.Rotate("alice", "GOOGLE_TOKEN", randomValue(t), "google-work"); err != nil {
		t.Fatal(err)
	}
	if _, err := registry.Resolve(auth, binding.ToolBindingID); !errors.Is(err, api.ErrStale) {
		t.Fatalf("rotated binding still current: %v", err)
	}
}

func TestFailedRotateMarksDegraded(t *testing.T) {
	svc := newService(t)
	secret := randomValue(t)
	infos, err := svc.Set("alice", map[string]string{"GOOGLE_TOKEN": secret})
	if err != nil {
		t.Fatal(err)
	}
	registry, auth, binding := seededRegistry(t, infos[0].Locator)
	svc.Registry = registry
	if err := registry.SetConnectionStatus("google-work", api.DisabledStatus); err != nil {
		t.Fatal(err)
	}
	if err := svc.Rotate("alice", "GOOGLE_TOKEN", randomValue(t), "google-work"); err == nil {
		t.Fatal("rotate on disabled connection succeeded")
	}
	if _, err := registry.Resolve(auth, binding.ToolBindingID); !errors.Is(err, api.ErrDegraded) && !errors.Is(err, api.ErrStale) && !errors.Is(err, api.ErrUnauthorized) && !errors.Is(err, api.ErrNotFound) {
		t.Fatalf("failed rotate did not fail closed: %v", err)
	}
	info, err := svc.Backend.Info(infos[0].Locator, "alice")
	if err != nil || info.Status != credstore.StatusDegraded {
		t.Fatalf("ciphertext status=%+v err=%v", info, err)
	}
}

func seededRegistry(t *testing.T, locator string) (*toolhub.Store, identity.Envelope, api.ToolBinding) {
	t.Helper()
	store := toolhub.NewStore()
	definition := api.ToolDefinition{
		Schema: api.SchemaVersion, DefinitionID: "google-work", Version: "1.0.0", Transport: api.RemoteMCP,
		Source:      api.DefinitionSource{URL: "https://api.example.com/mcp", TLSMode: "required"},
		Tools:       []api.ToolSpec{{Name: "search", Effect: api.ReadEffect}, {Name: "send", Effect: api.WriteEffect}},
		Credentials: []api.CredentialInput{{Name: "GOOGLE_TOKEN", Required: true}, {Name: "TLS_PEM", Required: true, Delivery: "json"}, {Name: "API_TOKEN", Required: true, Delivery: "env"}},
		Workload:    api.WorkloadPolicy{Class: api.PerUser, Rationale: "OAuth account and provider session are user-owned"},
		Execution:   api.ExecutionPolicy{TimeoutSeconds: 30, OutputBytes: 1 << 20, CPUMillis: 500, MemoryMiB: 256, MaxPIDs: 32, Egress: []string{"api.example.com"}},
		Health:      api.HealthProbe{Kind: "http", Value: "/health", TimeoutSeconds: 5},
	}
	if err := store.RegisterDefinition(definition); err != nil {
		t.Fatal(err)
	}
	credential := api.CredentialReference{Schema: api.SchemaVersion, CredentialRefID: api.CredentialReferenceID("google-work", 1), ConnectionID: "google-work", Revision: 1, Backend: "local", Locator: locator, Keys: []string{"GOOGLE_TOKEN", "TLS_PEM", "API_TOKEN"}, Status: api.ActiveStatus}
	if err := store.PutCredentialReference(credential); err != nil {
		t.Fatal(err)
	}
	connection := api.Connection{Schema: api.SchemaVersion, ConnectionID: "google-work", Owner: api.OwnerRef{Type: api.PrincipalOwner, ID: "alice"}, DefinitionID: definition.DefinitionID, CredentialRefID: credential.CredentialRefID, Revision: 1, Status: api.ActiveStatus}
	if err := store.PutConnection(connection); err != nil {
		t.Fatal(err)
	}
	auth := identity.TelegramEnvelope("alice", 7, "runtime", "policy-1")
	binding := api.ToolBinding{Schema: api.SchemaVersion, PrincipalID: "alice", ContextID: "alice", RuntimeID: "runtime", DefinitionID: definition.DefinitionID, DefinitionVersion: definition.Version, ConnectionID: connection.ConnectionID, ConnectionRevision: connection.Revision, CredentialRefID: credential.CredentialRefID, CredentialRevision: credential.Revision, PolicyVersion: auth.PolicyVersion, WorkloadClass: definition.Workload.Class, Status: api.ActiveStatus, Revision: 1, ProjectionRevision: 1}
	if err := store.PutBinding(binding); err != nil {
		t.Fatal(err)
	}
	binding.ToolBindingID = api.DeterministicBindingID(binding.PrincipalID, binding.ContextID, binding.RuntimeID, binding.DefinitionID, binding.DefinitionVersion, binding.ConnectionID, binding.CredentialRefID)
	return store, auth, binding
}

type listErrBackend struct {
	credstore.Backend
	err error
}

func (b listErrBackend) List(owner string) ([]credstore.RecordInfo, error) {
	if b.err != nil {
		return nil, b.err
	}
	return b.Backend.List(owner)
}

func TestReuseLocatorRotateErrorsAndInjectFilters(t *testing.T) {
	svc := newService(t)
	secret := randomValue(t)
	first, err := svc.Set("alice", map[string]string{"GOOGLE_TOKEN": secret})
	if err != nil {
		t.Fatal(err)
	}
	second, err := svc.Set("alice", map[string]string{"GOOGLE_TOKEN": randomValue(t)})
	if err != nil || first[0].Locator != second[0].Locator {
		t.Fatalf("locator not reused: first=%+v second=%+v err=%v", first, second, err)
	}
	if err := svc.Rotate("alice", "GOOGLE_TOKEN", "", ""); err == nil {
		t.Fatal("empty rotate accepted")
	}
	info, err := svc.Backend.Info(first[0].Locator, "alice")
	if err != nil || info.Status != credstore.StatusDegraded {
		t.Fatalf("empty rotate status=%+v err=%v", info, err)
	}
	if err := svc.Rotate("alice", "MISSING", randomValue(t), ""); !errors.Is(err, credstore.ErrNotFound) {
		t.Fatalf("missing rotate: %v", err)
	}
	if _, err := (*Service)(nil).List("alice"); !errors.Is(err, ErrNotConfigured) {
		t.Fatalf("nil list: %v", err)
	}
	if err := (*Service)(nil).Delete("alice", "GOOGLE_TOKEN"); !errors.Is(err, ErrNotConfigured) {
		t.Fatalf("nil delete: %v", err)
	}
	if err := (*Service)(nil).Rotate("alice", "GOOGLE_TOKEN", "x", ""); !errors.Is(err, ErrNotConfigured) {
		t.Fatalf("nil rotate: %v", err)
	}
	if err := (*Service)(nil).SetTerminalExposure("alice", "GOOGLE_TOKEN", true); !errors.Is(err, ErrNotConfigured) {
		t.Fatalf("nil expose: %v", err)
	}
	if _, err := svc.Set("alice", map[string]string{}); !errors.Is(err, credstore.ErrInvalid) {
		t.Fatalf("empty set: %v", err)
	}
	if _, err := Open("relative.enc", "", nil, ""); err == nil {
		t.Fatal("relative open accepted")
	}
	if err := svc.SetTerminalExposure("alice", "MISSING", true); !errors.Is(err, credstore.ErrNotFound) {
		t.Fatalf("missing expose: %v", err)
	}
	other := randomValue(t)
	if err := svc.Backend.Put(first[0].Locator, "alice", map[string]string{"GOOGLE_TOKEN": secret, "OTHER_TOKEN": other}); err != nil {
		t.Fatal(err)
	}
	if err := svc.Backend.SetStatus(first[0].Locator, "alice", credstore.StatusActive); err != nil {
		t.Fatal(err)
	}
	registry, auth, _ := seededRegistry(t, first[0].Locator)
	svc.Registry = registry
	injected, err := svc.Inject(auth, api.ProjectedToolName("google-work", "1.0.0", "search"), t.TempDir(), "")
	if err != nil || injected.Env["GOOGLE_TOKEN"] != secret || injected.Env["OTHER_TOKEN"] != "" {
		t.Fatalf("filter=%+v err=%v", injected.Env, err)
	}
	if err := injected.Wipe(); err != nil {
		t.Fatal(err)
	}
	failing := &Service{Backend: listErrBackend{Backend: svc.Backend, err: credstore.ErrInvalid}}
	if _, err := failing.List("alice"); !errors.Is(err, credstore.ErrInvalid) {
		t.Fatalf("list error: %v", err)
	}
	if _, err := failing.Set("alice", map[string]string{"GOOGLE_TOKEN": secret}); !errors.Is(err, credstore.ErrInvalid) {
		t.Fatalf("set list error: %v", err)
	}
}

func TestInjectWithoutCredentialAndPerJobWipe(t *testing.T) {
	svc := newService(t)
	store := toolhub.NewStore()
	definition := api.ToolDefinition{
		Schema: api.SchemaVersion, DefinitionID: "notes-work", Version: "1.0.0", Transport: api.RemoteMCP,
		Source:    api.DefinitionSource{URL: "https://api.example.com/mcp", TLSMode: "required"},
		Tools:     []api.ToolSpec{{Name: "search", Effect: api.ReadEffect}},
		Workload:  api.WorkloadPolicy{Class: api.PerJob, Rationale: "ephemeral job state is owner-scoped"},
		Execution: api.ExecutionPolicy{TimeoutSeconds: 30, OutputBytes: 1 << 20, CPUMillis: 500, MemoryMiB: 256, MaxPIDs: 32, Egress: []string{"api.example.com"}},
		Health:    api.HealthProbe{Kind: "http", Value: "/health", TimeoutSeconds: 5},
	}
	if err := store.RegisterDefinition(definition); err != nil {
		t.Fatal(err)
	}
	connection := api.Connection{Schema: api.SchemaVersion, ConnectionID: "notes-work", Owner: api.OwnerRef{Type: api.PrincipalOwner, ID: "alice"}, DefinitionID: definition.DefinitionID, Revision: 1, Status: api.ActiveStatus}
	if err := store.PutConnection(connection); err != nil {
		t.Fatal(err)
	}
	auth := identity.TelegramEnvelope("alice", 7, "runtime", "policy-1")
	binding := api.ToolBinding{Schema: api.SchemaVersion, PrincipalID: "alice", ContextID: "alice", RuntimeID: "runtime", DefinitionID: definition.DefinitionID, DefinitionVersion: definition.Version, ConnectionID: connection.ConnectionID, ConnectionRevision: connection.Revision, PolicyVersion: auth.PolicyVersion, WorkloadClass: definition.Workload.Class, Status: api.ActiveStatus, Revision: 1, ProjectionRevision: 1}
	if err := store.PutBinding(binding); err != nil {
		t.Fatal(err)
	}
	svc.Registry = store
	root := t.TempDir()
	injected, err := svc.Inject(auth, api.ProjectedToolName("notes-work", "1.0.0", "search"), root, "job-one")
	if err != nil || len(injected.Env) != 0 {
		t.Fatalf("no-credential inject=%+v err=%v", injected, err)
	}
	if err := injected.Wipe(); err != nil {
		t.Fatal(err)
	}
	shared := api.ToolDefinition{
		Schema: api.SchemaVersion, DefinitionID: "shared-work", Version: "1.0.0", Transport: api.RemoteMCP,
		Source:      api.DefinitionSource{URL: "https://api.example.com/mcp", TLSMode: "required"},
		Tools:       []api.ToolSpec{{Name: "search", Effect: api.ReadEffect}},
		Credentials: []api.CredentialInput{{Name: "GOOGLE_TOKEN", Required: true, PerRequest: true}},
		Workload:    api.WorkloadPolicy{Class: api.Shared, Rationale: "per-request credentials only"},
		Execution:   api.ExecutionPolicy{TimeoutSeconds: 30, OutputBytes: 1 << 20, CPUMillis: 500, MemoryMiB: 256, MaxPIDs: 32, Egress: []string{"api.example.com"}},
		Health:      api.HealthProbe{Kind: "http", Value: "/health", TimeoutSeconds: 5},
	}
	if err := store.RegisterDefinition(shared); err != nil {
		t.Fatal(err)
	}
}

func TestSetFailsClosedWhenAuditWriteFails(t *testing.T) {
	key, err := credstore.GenerateKey()
	if err != nil {
		t.Fatal(err)
	}
	backend, err := credstore.Open(credstore.Options{Path: filepath.Join(t.TempDir(), "store.enc"), Key: key})
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "audit.jsonl")
	ledger, err := audit.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	svc := &Service{Backend: backend, Audit: ledger}
	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(path, 0700); err != nil {
		t.Fatal(err)
	}
	if _, err := svc.Set("alice", map[string]string{"GOOGLE_TOKEN": randomValue(t)}); err == nil {
		t.Fatal("set succeeded with unwritable ledger")
	}
	listed, err := svc.List("alice")
	if err != nil || len(listed) != 0 {
		t.Fatalf("failed set left ciphertext listed=%+v err=%v", listed, err)
	}
}

func TestRotateAuditFailureMarksDegraded(t *testing.T) {
	svc := newService(t)
	infos, err := svc.Set("alice", map[string]string{"GOOGLE_TOKEN": randomValue(t)})
	if err != nil {
		t.Fatal(err)
	}
	registry, _, _ := seededRegistry(t, infos[0].Locator)
	svc.Registry = registry
	path := filepath.Join(t.TempDir(), "broken.jsonl")
	ledger, err := audit.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(path, 0700); err != nil {
		t.Fatal(err)
	}
	svc.Audit = ledger
	if err := svc.Rotate("alice", "GOOGLE_TOKEN", randomValue(t), "google-work"); err == nil {
		t.Fatal("rotate succeeded with unwritable ledger")
	}
}

type secretStopRecorder struct{ ids []string }

func (s *secretStopRecorder) Stop(id string) error {
	s.ids = append(s.ids, id)
	return nil
}

type secretBearerTransport struct {
	base  http.RoundTripper
	token string
}

func (t secretBearerTransport) RoundTrip(request *http.Request) (*http.Response, error) {
	copyRequest := request.Clone(request.Context())
	copyRequest.Header.Set("Authorization", "Bearer "+t.token)
	return t.base.RoundTrip(copyRequest)
}

func TestApplyChatCutsOpenSession(t *testing.T) {
	svc := newService(t)
	secret := randomValue(t)
	infos, err := svc.Set("alice", map[string]string{"GOOGLE_TOKEN": secret})
	if err != nil {
		t.Fatal(err)
	}
	registry, auth, binding := seededRegistry(t, infos[0].Locator)
	workload, err := api.NewWorkloadInstance(binding, &api.OwnerRef{Type: api.ContextOwner, ID: "alice"}, "", 1, time.Now().UTC(), time.Time{})
	if err != nil {
		t.Fatal(err)
	}
	workload.Status = api.RunningStatus
	if err := registry.PutWorkloadInstance(workload); err != nil {
		t.Fatal(err)
	}
	stops := &secretStopRecorder{}
	registry.Stopper = stops
	svc.Registry = registry
	var calls atomic.Int32
	handler, err := (&toolhub.Gateway{
		Store:   registry,
		Tokens:  map[string]identity.Envelope{"01234567890123456789012345678901": auth},
		Backend: toolhubCallCounter{n: &calls},
	}).Handler()
	if err != nil {
		t.Fatal(err)
	}
	server := httptest.NewServer(handler)
	defer server.Close()
	client := mcp.NewClient(&mcp.Implementation{Name: "chat-cut", Version: "1"}, nil)
	session, err := client.Connect(context.Background(), &mcp.StreamableClientTransport{Endpoint: server.URL + toolhub.DefaultEndpointPath, HTTPClient: &http.Client{Transport: secretBearerTransport{base: http.DefaultTransport, token: "01234567890123456789012345678901"}}}, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer session.Close()
	name := api.ProjectedToolName("google-work", "1.0.0", "search")
	if _, err := session.CallTool(context.Background(), &mcp.CallToolParams{Name: name, Arguments: map[string]any{"query": "ok"}}); err != nil {
		t.Fatal(err)
	}
	if calls.Load() != 1 {
		t.Fatalf("calls=%d", calls.Load())
	}
	if _, _, err := svc.ApplyChat("alice", "GOOGLE_TOKEN="+randomValue(t)); err != nil {
		t.Fatal(err)
	}
	if _, err := session.CallTool(context.Background(), &mcp.CallToolParams{Name: name, Arguments: map[string]any{"query": "after"}}); err == nil {
		t.Fatal("chat mutation did not cut open session")
	}
	if calls.Load() != 1 {
		t.Fatalf("backend ran after chat rotate: %d", calls.Load())
	}
	if len(stops.ids) == 0 {
		t.Fatal("affected workload was not stopped")
	}
}

type toolhubCallCounter struct{ n *atomic.Int32 }

func (c toolhubCallCounter) Call(context.Context, api.EffectiveBinding, api.ToolSpec, map[string]any) (api.BackendResult, error) {
	c.n.Add(1)
	return api.BackendResult{Text: "ok"}, nil
}

// CHG-0127 S0.6: file/json-delivery values reach the workspace as .secret
// files, never env lines.
func TestInjectRoutesFileDeliveryToSecretFile(t *testing.T) {
	svc := newService(t)
	pem := "-----BEGIN-----\nabc\n-----END-----\n"
	infos, err := svc.Set("alice", map[string]string{"TLS_PEM": pem, "API_TOKEN": "tok"})
	if err != nil {
		t.Fatal(err)
	}
	var locator string
	for _, info := range infos {
		if info.Name == "TLS_PEM" {
			locator = info.Locator
		}
	}
	store, auth, _ := seededRegistry(t, locator)
	svc.Registry = store
	root := t.TempDir()
	injected, err := svc.Inject(auth, api.ProjectedToolName("google-work", "1.0.0", "search"), root, "")
	if err != nil {
		t.Fatal(err)
	}
	if injected.Env["TLS_PEM"] != "" {
		t.Fatal("file-bound value landed in env map")
	}
	var envBody, pemBody string
	filepath.Walk(root, func(p string, info os.FileInfo, err error) error {
		if info != nil && info.Name() == "credentials.env" {
			data, _ := os.ReadFile(p)
			envBody = string(data)
		}
		if info != nil && info.Name() == "TLS_PEM.secret" {
			data, _ := os.ReadFile(p)
			pemBody = string(data)
		}
		return nil
	})
	if strings.Contains(envBody, "BEGIN") {
		t.Fatalf("env file leaked file content: %q", envBody)
	}
	if pemBody != pem {
		t.Fatalf("secret file content: %q", pemBody)
	}
}
