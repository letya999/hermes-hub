package secrets

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/letya999/hermes-hub/internal/audit"
	"github.com/letya999/hermes-hub/internal/credstore"
	"github.com/letya999/hermes-hub/internal/toolhub/api"
)

type mutationBackend struct {
	credstore.Backend
	puts       atomic.Int32
	deletes    atomic.Int32
	beforePut  func(int)
	afterPut   func(int)
	failPut    int
	failStatus bool
}

func (b *mutationBackend) Put(locator, owner string, values map[string]string) error {
	n := int(b.puts.Add(1))
	if b.beforePut != nil {
		b.beforePut(n)
	}
	if err := b.Backend.Put(locator, owner, values); err != nil {
		return err
	}
	if b.afterPut != nil {
		b.afterPut(n)
	}
	if n == b.failPut {
		return errors.New("fixture lost reply after durable write")
	}
	return nil
}
func (b *mutationBackend) Delete(locator, owner string) error {
	b.deletes.Add(1)
	return b.Backend.Delete(locator, owner)
}
func (b *mutationBackend) SetStatus(locator, owner, status string) error {
	if err := b.Backend.SetStatus(locator, owner, status); err != nil {
		return err
	}
	if b.failStatus {
		return errors.New("fixture lost status acknowledgement")
	}
	return nil
}

func missingAudit(t *testing.T, svc *Service) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "audit.jsonl")
	var err error
	svc.Audit, err = audit.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestAuditFailureNeverDestroysExistingCredential(t *testing.T) {
	svc := newService(t)
	original := randomValue(t)
	infos, err := svc.Set("alice", map[string]string{"GOOGLE_TOKEN": original})
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "unavailable-audit.jsonl")
	svc.Audit, err = audit.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	if _, err := svc.Set("alice", map[string]string{"GOOGLE_TOKEN": randomValue(t)}); err == nil {
		t.Fatal("unavailable audit allowed a confirmed write")
	}
	values, err := svc.Backend.Get(infos[0].Locator, "alice")
	if err != nil || values["GOOGLE_TOKEN"] != original {
		t.Fatal("audit failure destroyed or replaced the existing credential")
	}
}

func TestSetRequiresAuditBeforeEffect(t *testing.T) {
	svc := newService(t)
	backend := &mutationBackend{Backend: svc.Backend}
	svc.Backend, svc.Audit = backend, nil
	if _, err := svc.Set("alice", map[string]string{"GOOGLE_TOKEN": "fixture"}); !errors.Is(err, audit.ErrWrite) {
		t.Fatal("missing required audit accepted")
	}
	if backend.puts.Load() != 0 {
		t.Fatal("unaudited credential mutation")
	}
}

func TestSetCompletionFaultKeepsCiphertextAndDurableFence(t *testing.T) {
	svc := newService(t)
	path := filepath.Join(t.TempDir(), "audit.jsonl")
	var err error
	svc.Audit, err = audit.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	backend := &mutationBackend{Backend: svc.Backend, afterPut: func(int) {
		if err := os.Rename(path, path+".saved"); err != nil {
			t.Fatal(err)
		}
		if err := os.Mkdir(path, 0700); err != nil {
			t.Fatal(err)
		}
	}}
	svc.Backend = backend
	if infos, err := svc.Set("alice", map[string]string{"GOOGLE_TOKEN": "fixture-value"}); !errors.Is(err, audit.ErrUncertain) || infos != nil {
		t.Fatal("post-effect audit failure looked retryable or successful")
	}
	if backend.puts.Load() != 1 || backend.deletes.Load() != 0 {
		t.Fatal("ciphertext was rolled back or action repeated")
	}
	records, err := backend.List("alice")
	if err != nil || len(records) != 1 {
		t.Fatal("committed ciphertext disappeared")
	}
	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(path+".saved", path); err != nil {
		t.Fatal(err)
	}
	svc.Audit, err = audit.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := svc.Set("alice", map[string]string{"GOOGLE_TOKEN": "changed-fixture"}); !errors.Is(err, audit.ErrUncertain) {
		t.Fatal("unsettled write was replayed")
	}
	if backend.puts.Load() != 1 {
		t.Fatal("recovered request repeated the credential effect")
	}
}

func TestPartialBatchIsUncertainAndNeverDeletesEarlierWrites(t *testing.T) {
	svc := newService(t)
	backend := &mutationBackend{Backend: svc.Backend, failPut: 2}
	svc.Backend = backend
	names, message, err := svc.ApplyChat("alice", "A_TOKEN=fixture-a\nB_TOKEN=fixture-b\nC_TOKEN=fixture-c")
	if !errors.Is(err, audit.ErrUncertain) || names != nil || !strings.Contains(message, "uncertain") {
		t.Fatal("partially applied batch looked rejected or successful")
	}
	records, err := backend.List("alice")
	if err != nil || len(records) != 2 || backend.deletes.Load() != 0 {
		t.Fatal("partial write evidence was destroyed")
	}
	if _, err := svc.Set("alice", map[string]string{"C_TOKEN": "retry"}); !errors.Is(err, audit.ErrUncertain) {
		t.Fatal("unknown owner batch was blindly repeated")
	}
	if backend.puts.Load() != 2 {
		t.Fatal("unknown batch repeated an effect")
	}
	pending, err := svc.Audit.Pending(audit.Scope{PrincipalID: "alice"}, 10)
	if err != nil || len(pending) != 1 || pending[0].Execution != "unknown" || pending[0].Authorization != "allow" {
		t.Fatal("durable batch intent missing")
	}
}

func TestSetValidatesWholeBatchBeforeMutation(t *testing.T) {
	svc := newService(t)
	backend := &mutationBackend{Backend: svc.Backend}
	svc.Backend = backend
	if _, err := svc.Set("alice", map[string]string{"A_TOKEN": "valid", "B_TOKEN": ""}); !errors.Is(err, credstore.ErrInvalid) {
		t.Fatal("invalid batch accepted")
	}
	if backend.puts.Load() != 0 {
		t.Fatal("invalid batch changed a valid credential")
	}
}

func TestOwnerAdmissionFencesConcurrentMutationWithoutBlockingOtherOwner(t *testing.T) {
	svc := newService(t)
	entered, release := make(chan struct{}), make(chan struct{})
	released := false
	defer func() {
		if !released {
			close(release)
		}
	}()
	backend := &mutationBackend{Backend: svc.Backend, beforePut: func(n int) {
		if n == 1 {
			close(entered)
			<-release
		}
	}}
	svc.Backend = backend
	done := make(chan error, 1)
	go func() { _, err := svc.Set("alice", map[string]string{"A_TOKEN": "first"}); done <- err }()
	<-entered
	if _, err := svc.Set("alice", map[string]string{"A_TOKEN": "second"}); !errors.Is(err, audit.ErrUncertain) {
		t.Error("concurrent owner effect was admitted")
	}
	if _, err := svc.Set("bob", map[string]string{"A_TOKEN": "bob"}); err != nil {
		t.Errorf("foreign owner inherited fence: %v", err)
	}
	close(release)
	released = true
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	if backend.puts.Load() != 2 {
		t.Fatal("duplicate effect passed owner admission")
	}
	for _, owner := range []string{"alice", "bob"} {
		records, err := backend.List(owner)
		if err != nil || len(records) != 1 {
			t.Fatal("concurrent owner write lost namespace data")
		}
	}
}

func TestRotateAndExposureAuditFailurePreventsEffect(t *testing.T) {
	for _, action := range []string{"rotate", "expose"} {
		t.Run(action, func(t *testing.T) {
			svc := newService(t)
			infos, err := svc.Set("alice", map[string]string{"GOOGLE_TOKEN": "original"})
			if err != nil {
				t.Fatal(err)
			}
			before, err := svc.Backend.Info(infos[0].Locator, "alice")
			if err != nil {
				t.Fatal(err)
			}
			missingAudit(t, svc)
			env := filepath.Join(t.TempDir(), "secrets.env")
			svc.EnvFile = func(string) string { return env }
			if action == "rotate" {
				err = svc.Rotate("alice", "GOOGLE_TOKEN", "replacement", "")
			} else {
				err = svc.SetTerminalExposure("alice", "GOOGLE_TOKEN", true)
			}
			if err == nil {
				t.Fatal("audit failure reported success")
			}
			after, readErr := svc.Backend.Info(infos[0].Locator, "alice")
			if readErr != nil || after.Revision != before.Revision || after.TerminalExposure {
				t.Fatal("audit failure allowed a credential side effect")
			}
			if _, err := os.Stat(env); !errors.Is(err, os.ErrNotExist) {
				t.Fatal("audit failure published plaintext credential")
			}
		})
	}
}

func TestDeleteStillRevokesRegistryWhenAuditUnavailable(t *testing.T) {
	svc := newService(t)
	infos, err := svc.Set("alice", map[string]string{"GOOGLE_TOKEN": "fixture"})
	if err != nil {
		t.Fatal(err)
	}
	registry, auth, binding := seededRegistry(t, infos[0].Locator)
	svc.Registry = registry
	missingAudit(t, svc)
	if err := svc.Delete("alice", "GOOGLE_TOKEN"); !errors.Is(err, audit.ErrUncertain) {
		t.Fatal("unconfirmed cleanup was not uncertain")
	}
	if _, err := registry.Resolve(auth, binding.ToolBindingID); err == nil {
		t.Fatal("audit failure prevented access revocation")
	}
}

func TestExposureDisableSurfacesPlaintextCleanupFailure(t *testing.T) {
	svc := newService(t)
	infos, err := svc.Set("alice", map[string]string{"GOOGLE_TOKEN": "fixture"})
	if err != nil {
		t.Fatal(err)
	}
	env := filepath.Join(t.TempDir(), "env-is-directory")
	if err := os.Mkdir(env, 0700); err != nil {
		t.Fatal(err)
	}
	svc.EnvFile = func(string) string { return env }
	if err := svc.SetTerminalExposure("alice", "GOOGLE_TOKEN", false); !errors.Is(err, audit.ErrUncertain) {
		t.Fatal("failed plaintext cleanup reported success")
	}
	info, err := svc.Backend.Info(infos[0].Locator, "alice")
	if err != nil || info.TerminalExposure {
		t.Fatal("cleanup failure reopened terminal exposure")
	}
}

func TestInjectionRequiresAuditBeforeCredentialAccess(t *testing.T) {
	svc := newService(t)
	infos, err := svc.Set("alice", map[string]string{"GOOGLE_TOKEN": "fixture"})
	if err != nil {
		t.Fatal(err)
	}
	registry, auth, _ := seededRegistry(t, infos[0].Locator)
	svc.Registry, svc.Audit = registry, nil
	result, err := svc.Inject(auth, api.ProjectedToolName("google-work", "1.0.0", "search"), t.TempDir(), "")
	if !errors.Is(err, audit.ErrWrite) || result.Env != nil {
		t.Fatal("credential injection accepted missing audit")
	}
}

func TestSetAndRotatePreserveSiblingCredentialValues(t *testing.T) {
	for _, action := range []string{"set", "rotate"} {
		t.Run(action, func(t *testing.T) {
			svc := newService(t)
			locator, err := svc.Backend.NewLocator()
			if err != nil {
				t.Fatal(err)
			}
			if err := svc.Backend.Put(locator, "alice", map[string]string{"A_TOKEN": "original-a", "B_TOKEN": "original-b"}); err != nil {
				t.Fatal(err)
			}
			if action == "set" {
				_, err = svc.Set("alice", map[string]string{"A_TOKEN": "new-a"})
			} else {
				err = svc.Rotate("alice", "A_TOKEN", "new-a", "")
			}
			if err != nil {
				t.Fatal(err)
			}
			values, err := svc.Backend.Get(locator, "alice")
			if err != nil || values["A_TOKEN"] != "new-a" || values["B_TOKEN"] != "original-b" {
				t.Fatal("updating one credential erased another value")
			}
		})
	}
}

func TestRotateCannotTargetAnotherOwnersConnection(t *testing.T) {
	svc := newService(t)
	if _, err := svc.Set("bob", map[string]string{"GOOGLE_TOKEN": "bob-original"}); err != nil {
		t.Fatal(err)
	}
	registry, auth, binding := seededRegistry(t, "foreign-locator")
	svc.Registry = registry
	if err := svc.Rotate("bob", "GOOGLE_TOKEN", "replacement", "google-work"); !errors.Is(err, api.ErrUnauthorized) {
		t.Fatal("foreign connection accepted an owner rotation")
	}
	if _, err := registry.Resolve(auth, binding.ToolBindingID); err != nil {
		t.Fatal("foreign connection changed")
	}
}

func TestCredentialValidationNeverEchoesDictionaryKey(t *testing.T) {
	svc := newService(t)
	locator, err := svc.Backend.NewLocator()
	if err != nil {
		t.Fatal(err)
	}
	key := "FIXTUREBEARERDICTIONARYKEY"
	if err := svc.Backend.Put(locator, "alice", map[string]string{key: ""}); err == nil || strings.Contains(err.Error(), key) {
		t.Fatal("credential validation disclosed a dictionary key")
	}
}

func TestInvalidRotationRecordsConfirmedFailureWithoutCredentialWrite(t *testing.T) {
	svc := newService(t)
	infos, err := svc.Set("alice", map[string]string{"GOOGLE_TOKEN": "original"})
	if err != nil {
		t.Fatal(err)
	}
	backend := &mutationBackend{Backend: svc.Backend}
	svc.Backend = backend
	if err := svc.Rotate("alice", "GOOGLE_TOKEN", "", ""); !errors.Is(err, credstore.ErrInvalid) {
		t.Fatal("verified input failure was not classified")
	}
	if backend.puts.Load() != 0 {
		t.Fatal("invalid rotation reached credential write")
	}
	info, err := backend.Info(infos[0].Locator, "alice")
	if err != nil || info.Status != credstore.StatusDegraded {
		t.Fatal("failed rotation did not close credential access")
	}
	pending, err := svc.Audit.Pending(audit.Scope{PrincipalID: "alice"}, 10)
	if err != nil || len(pending) != 0 {
		t.Fatal("confirmed failure left a false unknown intent")
	}
}

func TestInvalidRotationWithUnconfirmedDowngradeStaysUnknown(t *testing.T) {
	svc := newService(t)
	if _, err := svc.Set("alice", map[string]string{"GOOGLE_TOKEN": "original"}); err != nil {
		t.Fatal(err)
	}
	backend := &mutationBackend{Backend: svc.Backend, failStatus: true}
	svc.Backend = backend
	if err := svc.Rotate("alice", "GOOGLE_TOKEN", "", ""); !errors.Is(err, audit.ErrUncertain) {
		t.Fatal("unconfirmed downgrade was declared a known failure")
	}
	pending, err := svc.Audit.Pending(audit.Scope{PrincipalID: "alice"}, 10)
	if err != nil || len(pending) != 1 || pending[0].Execution != "unknown" {
		t.Fatal("unconfirmed status effect lost its durable intent")
	}
	if backend.puts.Load() != 0 {
		t.Fatal("invalid rotation reached credential write")
	}
}
