package api

import (
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/letya999/hermes-hub/internal/credstore"
)

type stubCredBackend struct {
	values map[string]string
	owner  string
	err    error
}

func (s *stubCredBackend) Name() string                                { return "stub" }
func (s *stubCredBackend) NewLocator() (string, error)                 { return "", nil }
func (s *stubCredBackend) Put(string, string, map[string]string) error { return nil }
func (s *stubCredBackend) Get(locator, owner string) (map[string]string, error) {
	s.owner = owner
	return s.values, s.err
}
func (s *stubCredBackend) Delete(string, string) error { return nil }
func (s *stubCredBackend) Info(string, string) (credstore.RecordInfo, error) {
	return credstore.RecordInfo{}, nil
}
func (s *stubCredBackend) List(string) ([]credstore.RecordInfo, error)    { return nil, nil }
func (s *stubCredBackend) SetStatus(string, string, string) error         { return nil }
func (s *stubCredBackend) SetTerminalExposure(string, string, bool) error { return nil }
func (s *stubCredBackend) Backup(io.Writer) error                         { return nil }
func (s *stubCredBackend) Restore(io.Reader) error                        { return nil }
func (s *stubCredBackend) RotateKey([]byte) error                         { return nil }

func TestOpenWorkloadWorkspaceClasses(t *testing.T) {
	root := t.TempDir()
	base := EffectiveBinding{Definition: ToolDefinition{DefinitionID: "def-1"},
		Binding: ToolBinding{PrincipalID: "p1", ContextID: "ctx-1", ToolBindingID: "bind-1"}}

	// shared workloads get a context-scoped path for credential
	// materialization; they still own no principal-scoped or job state
	shared := base
	shared.Definition.Workload.Class = Shared
	ws, err := OpenWorkloadWorkspace(root, shared, "")
	if err != nil || !strings.Contains(ws.Path, filepath.Join("shared", "ctx-1", "def-1")) {
		t.Fatalf("shared workspace: %+v %v", ws, err)
	}
	if err := ws.Cleanup(); err != nil {
		t.Fatal(err)
	}
	// shared + jobID is an isolation violation
	if _, err := OpenWorkloadWorkspace(root, shared, "job-1"); !errors.Is(err, ErrUnauthorized) {
		t.Fatalf("shared job state: %v", err)
	}
	// per-user gets a stable binding-scoped path
	perUser := base
	perUser.Definition.Workload.Class = PerUser
	w1, err := OpenWorkloadWorkspace(root, perUser, "")
	if err != nil || w1.Path == "" {
		t.Fatalf("per-user workspace: %+v %v", w1, err)
	}
	w2, err := OpenWorkloadWorkspace(root, perUser, "")
	if err != nil || w1.Path != w2.Path {
		t.Fatalf("per-user path not stable: %q %q", w1.Path, w2.Path)
	}
	// per-job gets a fresh temp dir; cleanup removes it
	perJob := base
	perJob.Definition.Workload.Class = PerJob
	wj, err := OpenWorkloadWorkspace(root, perJob, "job-1")
	if err != nil || wj.Path == "" {
		t.Fatalf("per-job workspace: %v", err)
	}
	if !filepath.IsAbs(wj.Path) {
		t.Fatal(wj.Path)
	}
	if err := wj.Cleanup(); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(wj.Path); !os.IsNotExist(err) {
		t.Fatal("job workspace not removed")
	}
	// invalid class and missing identity are rejected
	bad := base
	bad.Definition.Workload.Class = "weird"
	if _, err := OpenWorkloadWorkspace(root, bad, ""); !errors.Is(err, ErrInvalid) {
		t.Fatalf("class: %v", err)
	}
	anon := base
	anon.Definition.Workload.Class = PerUser
	anon.Binding.ContextID = ""
	if _, err := OpenWorkloadWorkspace(root, anon, ""); !errors.Is(err, ErrInvalid) {
		t.Fatalf("anon context: %v", err)
	}
}

func TestDecryptAuthorizedAndEnvFile(t *testing.T) {
	eff := EffectiveBinding{
		Definition: ToolDefinition{DefinitionID: "def-1", Workload: WorkloadPolicy{Class: PerUser}},
		Binding:    ToolBinding{PrincipalID: "p1", ContextID: "ctx-1", ToolBindingID: "bind-1"},
		Credential: &CredentialReference{Locator: "loc-1", Keys: []string{"API_TOKEN"}},
		Connection: &Connection{ConnectionID: "conn-1", Owner: OwnerRef{ID: "owner-1"}},
	}
	// nil backend and shared-static credentials are refused
	if _, err := DecryptAuthorized(nil, eff); !errors.Is(err, ErrInvalid) {
		t.Fatalf("nil backend: %v", err)
	}
	shared := eff
	shared.Definition.Workload.Class = Shared
	shared.Definition.Credentials = []CredentialInput{{Name: "API_TOKEN"}}
	if _, err := DecryptAuthorized(&stubCredBackend{}, shared); !errors.Is(err, ErrUnauthorized) {
		t.Fatalf("shared static cred: %v", err)
	}
	// only admitted keys are injected; shared owner override wins the locator scope
	backend := &stubCredBackend{values: map[string]string{"API_TOKEN": "v", "OTHER_KEY": "drop"}}
	got, err := DecryptAuthorized(backend, eff)
	if err != nil || len(got) != 1 || got["API_TOKEN"] != "v" {
		t.Fatalf("decrypt: %v %v", got, err)
	}
	if backend.owner != "owner-1" {
		t.Fatalf("owner: %q", backend.owner)
	}
	eff.Connection.Metadata = map[string]string{"credential_owner": "shared-owner"}
	backend.owner = ""
	if _, err := DecryptAuthorized(backend, eff); err != nil || backend.owner != "shared-owner" {
		t.Fatalf("shared owner: %q %v", backend.owner, err)
	}
	// no credential reference → empty environment, no store call
	noCred := eff
	noCred.Credential = nil
	if got, err := DecryptAuthorized(backend, noCred); err != nil || len(got) != 0 {
		t.Fatalf("no credential: %v %v", got, err)
	}

	// env file lands at 0600-keyed KEY=value under the per-user workspace
	root := t.TempDir()
	env := map[string]string{"API_TOKEN": "v1", "REGION": "us"}
	cleanup, err := WriteAuthorizedFiles(context.Background(), root, eff, env)
	if err != nil {
		t.Fatal(err)
	}
	var envPath string
	filepath.Walk(root, func(p string, info os.FileInfo, err error) error {
		if info != nil && info.Name() == "credentials.env" {
			envPath = p
		}
		return nil
	})
	body, _ := os.ReadFile(envPath)
	if string(body) != "API_TOKEN=v1\nREGION=us" {
		t.Fatalf("env file: %q", body)
	}
	if err := cleanup(); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(envPath); !os.IsNotExist(err) {
		t.Fatal("credentials.env not removed by cleanup")
	}
	// empty environment is a no-op; env without a root is an isolation error
	if cleanup, err := WriteAuthorizedFiles(context.Background(), root, eff, nil); err != nil || cleanup == nil {
		t.Fatalf("empty env: %v", err)
	}
	if _, err := WriteAuthorizedFiles(context.Background(), "  ", eff, env); !errors.Is(err, ErrIsolation) {
		t.Fatalf("rootless env: %v", err)
	}
	// shared class gets a context-scoped inject path: the credential is
	// context-owned, so its materialization lives under shared/<context>/
	// rather than a per-principal directory, and job state stays forbidden.
	sharedEff := eff
	sharedEff.Definition.Workload.Class = Shared
	cleanup, err = WriteAuthorizedFiles(context.Background(), root, sharedEff, env)
	if err != nil {
		t.Fatalf("shared env rejected: %v", err)
	}
	var sharedEnvPath string
	filepath.Walk(root, func(p string, info os.FileInfo, err error) error {
		if info != nil && info.Name() == "credentials.env" && strings.Contains(p, "shared") {
			sharedEnvPath = p
		}
		return nil
	})
	if sharedEnvPath == "" || !strings.Contains(sharedEnvPath, filepath.Join("shared", "ctx-1")) {
		t.Fatalf("shared env file not under context scope: %q", sharedEnvPath)
	}
	if err := cleanup(); err != nil {
		t.Fatal(err)
	}
}

// CHG-0127 S0.6: file/json-delivery values are staged as <name>.secret
// siblings and never land on an env line; env-bound values stay in
// credentials.env.
func TestWriteAuthorizedFilesRoutesFileDelivery(t *testing.T) {
	eff := EffectiveBinding{
		Definition: ToolDefinition{DefinitionID: "def-1", Workload: WorkloadPolicy{Class: PerUser}, Credentials: []CredentialInput{
			{Name: "TLS_PEM", Required: true, Delivery: "file"},
			{Name: "API_TOKEN", Required: true, Delivery: "env"},
		}},
		Binding:    ToolBinding{PrincipalID: "p1", ContextID: "ctx-1", ToolBindingID: "bind-1"},
		Credential: &CredentialReference{Locator: "loc-1", Keys: []string{"API_TOKEN", "TLS_PEM"}},
		Connection: &Connection{ConnectionID: "conn-1", Owner: OwnerRef{ID: "owner-1"}},
	}
	root := t.TempDir()
	env := map[string]string{"API_TOKEN": "v1", "TLS_PEM": "-----BEGIN-----\nabc\n-----END-----\n"}
	cleanup, err := WriteAuthorizedFiles(context.Background(), root, eff, env)
	if err != nil {
		t.Fatal(err)
	}
	defer cleanup()
	var envPath, secretPath string
	filepath.Walk(root, func(p string, info os.FileInfo, err error) error {
		if info != nil && info.Name() == "credentials.env" {
			envPath = p
		}
		if info != nil && info.Name() == "TLS_PEM.secret" {
			secretPath = p
		}
		return nil
	})
	if secretPath == "" {
		t.Fatal("file-delivery value not staged as .secret")
	}
	body, _ := os.ReadFile(envPath)
	if strings.Contains(string(body), "BEGIN") || !strings.Contains(string(body), "API_TOKEN=v1") {
		t.Fatalf("env file leaked file content: %q", body)
	}
	staged, _ := os.ReadFile(secretPath)
	if !strings.Contains(string(staged), "BEGIN") {
		t.Fatalf("secret file content lost: %q", staged)
	}
}
