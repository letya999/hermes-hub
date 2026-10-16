package broker

import (
	"bytes"
	"errors"
	"path/filepath"
	"testing"

	"github.com/letya999/credential-broker/contract"
	"github.com/letya999/credential-broker/identity"
	"github.com/letya999/credential-broker/internal/journal"
	"github.com/letya999/credential-broker/materialize"
)

func dynamicContract() contract.Contract {
	return contract.Contract{ID: "dyn-abc123", Revision: 1, Title: "notion credentials", Fields: []contract.Field{{ID: "token", Kind: "secret", Label: "NOTION_TOKEN", Required: true, MaxBytes: 4096}}, Deliveries: []contract.Delivery{{Type: "env", Field: "token", Target: "NOTION_TOKEN"}}}
}

func TestDynamicContractRegistration(t *testing.T) {
	f := newFixture(t)
	in := dynamicContract()
	stored, err := f.b.RegisterContract(alice, in)
	if err != nil {
		t.Fatalf("register: %v", err)
	}
	if stored.Storage != "local" {
		t.Fatalf("default writable storage not assigned: %+v", stored)
	}
	found := false
	for _, c := range f.b.Catalog() {
		if c.ID == "dyn-abc123" && c.Revision == 1 && c.Digest() == stored.Digest() {
			found = true
		}
	}
	if !found {
		t.Fatal("dynamic contract missing from catalog")
	}
	again, err := f.b.RegisterContract(alice, in)
	if err != nil || again.Digest() != stored.Digest() {
		t.Fatalf("idempotent re-registration: %v", err)
	}
	other := dynamicContract()
	other.Title = "mutated title"
	if _, err = f.b.RegisterContract(alice, other); !errors.Is(err, ErrConflict) {
		t.Fatalf("same id+revision with different digest must conflict: %v", err)
	}
	revision := dynamicContract()
	revision.Revision = 2
	if _, err = f.b.RegisterContract(alice, revision); err != nil {
		t.Fatalf("new revision of dynamic contract: %v", err)
	}

	c := f.enroll(alice, "dyn-abc123", "notion-conn")
	if c.Status != "active" || c.Revision != 1 {
		t.Fatalf("dynamic contract did not enroll: %+v", c)
	}

	f.restart()
	usable := false
	for _, c := range f.b.Catalog() {
		if c.ID == "dyn-abc123" && c.Revision == 1 {
			usable = true
		}
	}
	if !usable {
		t.Fatal("dynamic contract lost across restart")
	}
	r := f.request(alice, "dyn-abc123", "notion-second", "second_request")
	if r.ContractRevision != 1 || r.AuthorizationURL == "" {
		t.Fatalf("post-restart request against dynamic contract: %+v", r)
	}
	registered := false
	for _, e := range f.b.Events(alice, 0, 100).Events {
		if e.Kind == "contract.registered" && e.ContractID == "dyn-abc123@1" {
			registered = true
		}
	}
	if !registered {
		t.Fatal("contract.registered audit event missing")
	}
}

func TestDynamicContractRegistrationBounds(t *testing.T) {
	f := newFixture(t)
	base := dynamicContract()
	base.ID = "dyn-no-storage"
	base.Storage = "ghost"
	if _, err := f.b.RegisterContract(alice, base); !errors.Is(err, ErrInvalid) {
		t.Fatalf("unknown storage must fail: %v", err)
	}
	base = dynamicContract()
	base.ID = "dyn-invalid"
	base.Fields = nil
	if _, err := f.b.RegisterContract(alice, base); !errors.Is(err, ErrInvalid) {
		t.Fatalf("invalid contract must fail: %v", err)
	}
	for name, mutate := range map[string]func(*contract.Contract){
		"routes": func(c *contract.Contract) {
			c.Routes = []contract.Route{{ID: "r", BaseURL: "https://api.example", PathPrefixes: []string{"/"}, Methods: []string{"GET"}, Auth: "bearer", Field: "token"}}
		},
		"oauth": func(c *contract.Contract) { c.OAuthProvider = "google" },
		"state": func(c *contract.Contract) {
			c.State = &contract.State{Target: "/home/mcp/.s", Files: []contract.StateFile{{Name: "s.json", MaxBytes: 64}}}
		},
	} {
		c := dynamicContract()
		c.ID = "dyn-" + name
		mutate(&c)
		if _, err := f.b.RegisterContract(alice, c); !errors.Is(err, ErrDenied) {
			t.Fatalf("%s must be denied for dynamic contracts: %v", name, err)
		}
	}
	// allow_shared is synthesizable: a Sharable shared definition's single
	// policy-owned credential is still collected through the same form.
	shared := dynamicContract()
	shared.ID = "dyn-shared"
	shared.AllowShared = true
	if _, err := f.b.RegisterContract(alice, shared); err != nil {
		t.Fatalf("allow_shared dynamic contract must register: %v", err)
	}
	if _, err := f.b.RegisterContract(identity.Actor{}, dynamicContract()); !errors.Is(err, ErrInvalid) {
		t.Fatalf("invalid actor: %v", err)
	}
}

func TestDynamicContractSurvivesOnlyWhenCatalogMatches(t *testing.T) {
	f := newFixture(t)
	if _, err := f.b.RegisterContract(alice, dynamicContract()); err != nil {
		t.Fatal(err)
	}
	if err := f.b.Close(); err != nil {
		t.Fatal(err)
	}
	f.b = nil
	reopen := func() {
		j, e := journal.Open(filepath.Join(f.root, "ledger"), bytes.Repeat([]byte{9}, 32), 8<<20)
		if e != nil {
			t.Fatal(e)
		}
		m, e := materialize.New(filepath.Join(f.root, "runtime"), false)
		if e != nil {
			t.Fatal(e)
		}
		f.cfg.Journal, f.cfg.Materializer = j, m
	}
	// An operator file that pins the same id@revision with different content
	// must fail closed at startup, not silently shadow the journaled contract.
	drifted := dynamicContract()
	drifted.Title = "operator override"
	drifted.Storage = "local"
	reopen()
	cfg := f.cfg
	cfg.Contracts = append(append([]contract.Contract{}, cfg.Contracts...), drifted)
	if _, err := New(cfg); !errors.Is(err, ErrConflict) {
		t.Fatalf("conflicting file contract must fail startup: %v", err)
	}
	if err := cfg.Journal.Close(); err != nil {
		t.Fatal(err)
	}
	reopen()
	identical := dynamicContract()
	identical.Storage = "local"
	cfg = f.cfg
	cfg.Contracts = append(append([]contract.Contract{}, cfg.Contracts...), identical)
	b, err := New(cfg)
	if err != nil {
		t.Fatalf("identical file contract must be tolerated: %v", err)
	}
	f.b = b
}
