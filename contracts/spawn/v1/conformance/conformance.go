// Package conformance is the spawn/v1 suite every transport and server
// must pass (SPEC-0100 R2.2): space registry, the submit/validate/activate
// lifecycle, effective spec and policy slices, idempotent resubmission
// and tenant isolation — everything contract-shaped, never the compiler.
package conformance

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	spawn "github.com/letya999/hermes-hub/contracts/spawn/v1"
	"github.com/letya999/hermes-hub/shared/testkit"
)

// Fixture is everything a conforming server needs: the owning tenant, a
// foreign one that must see nothing, the space to register, documents for
// every layer that validates, and one consumer the policy slice answers.
type Fixture struct {
	Tenant   spawn.Tenant
	Foreign  spawn.Tenant
	Space    string
	Layers   map[string]map[string]map[string]any
	Consumer string
}

// Run exercises one client end to end.
func Run(t *testing.T, client spawn.Client, f Fixture) {
	t.Helper()
	ctx := context.Background()

	// Space registry: create, idempotent re-create, environment conflict.
	sp, err := client.CreateSpace(ctx, spawn.CreateSpaceRequest{
		Org: f.Tenant.Org, Owner: f.Tenant.Owner, ID: f.Space, Environment: "dev", IdempotencyKey: "k-space",
	})
	if err != nil {
		t.Fatalf("create space: %v", err)
	}
	if sp.ID != f.Space || sp.Environment != "dev" || sp.Org != f.Tenant.Org || sp.Owner != f.Tenant.Owner {
		t.Fatalf("space row %+v", sp)
	}
	again, err := client.CreateSpace(ctx, spawn.CreateSpaceRequest{
		Org: f.Tenant.Org, Owner: f.Tenant.Owner, ID: f.Space, Environment: "dev", IdempotencyKey: "k-space-2",
	})
	if err != nil || again.CreatedAt != sp.CreatedAt {
		t.Fatalf("re-create space: %v %+v", err, again)
	}
	if _, err := client.CreateSpace(ctx, spawn.CreateSpaceRequest{
		Org: f.Tenant.Org, Owner: f.Tenant.Owner, ID: f.Space, Environment: "prod", IdempotencyKey: "k-space-3",
	}); !errors.Is(err, spawn.ErrConflict) {
		t.Fatalf("environment flip: want conflict, got %v", err)
	}

	// Lifecycle: submit every layer, then validate and activate each.
	// The suite runs once per transport on the same server, so a repeat
	// submission may return the already-active row — only fresh drafts
	// walk the lifecycle again.
	ids := map[string]string{}
	fresh := map[string]bool{}
	for _, layer := range spawn.Layers {
		docs, ok := f.Layers[layer]
		if !ok {
			continue
		}
		rev, err := client.SubmitRevision(ctx, f.Space, layer, spawn.SubmitRevisionRequest{
			Org: f.Tenant.Org, Owner: f.Tenant.Owner, Documents: docs, IdempotencyKey: "k-submit-" + layer,
		})
		if err != nil {
			t.Fatalf("submit %s: %v", layer, err)
		}
		if rev.Layer != layer || rev.Space != f.Space || (rev.State != "draft" && rev.State != "active") {
			t.Fatalf("submit %s: %+v", layer, rev)
		}
		fresh[layer] = rev.State == "draft"
		ids[layer] = rev.ID
	}
	// Identical resubmission returns the same row — the write dedups on
	// the layer digest.
	dup, err := client.SubmitRevision(ctx, f.Space, "user", spawn.SubmitRevisionRequest{
		Org: f.Tenant.Org, Owner: f.Tenant.Owner, Documents: f.Layers["user"], IdempotencyKey: "k-submit-dup",
	})
	if err != nil || dup.ID != ids["user"] {
		t.Fatalf("resubmit: %v id %s want %s", err, dup.ID, ids["user"])
	}
	// Activation is a set operation: the whole layer set moves in one
	// call — a bootstrap can never leave the space on a partial spec.
	for layer, id := range ids {
		if !fresh[layer] {
			continue
		}
		rev, err := client.ValidateRevision(ctx, f.Space, id, spawn.RevisionRequest{
			Org: f.Tenant.Org, Owner: f.Tenant.Owner, IdempotencyKey: "k-validate-" + layer,
		})
		if err != nil {
			t.Fatalf("validate %s: %v", layer, err)
		}
		if rev.State != "validated" {
			t.Fatalf("validate %s: state %s", layer, rev.State)
		}
	}
	all := make([]string, 0, len(ids))
	for _, id := range ids {
		all = append(all, id)
	}
	activated, err := client.ActivateRevisions(ctx, f.Space, spawn.ActivationRequest{
		Org: f.Tenant.Org, Owner: f.Tenant.Owner, Revisions: all, IdempotencyKey: "k-activate-all",
	})
	if err != nil {
		t.Fatalf("activate set: %v", err)
	}
	if len(activated) != len(all) {
		t.Fatalf("activate set returned %d rows, want %d", len(activated), len(all))
	}
	for _, rev := range activated {
		if rev.State != "active" {
			t.Fatalf("activate set: %s is %s", rev.ID, rev.State)
		}
	}
	// The one-element set is a no-op on an already-active row.
	one, err := client.ActivateRevisions(ctx, f.Space, spawn.ActivationRequest{
		Org: f.Tenant.Org, Owner: f.Tenant.Owner, Revisions: []string{ids["user"]}, IdempotencyKey: "k-activate-one",
	})
	if err != nil || len(one) != 1 || one[0].State != "active" {
		t.Fatalf("activate one: %v %+v", err, one)
	}

	// List: every row present, the layer filter narrows.
	list, err := client.ListRevisions(ctx, f.Tenant, f.Space, "")
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(list) != len(ids) {
		t.Fatalf("list returns %d rows, want %d", len(list), len(ids))
	}
	userOnly, err := client.ListRevisions(ctx, f.Tenant, f.Space, "user")
	if err != nil || len(userOnly) != 1 || userOnly[0].Layer != "user" {
		t.Fatalf("list layer filter: %v %+v", err, userOnly)
	}

	// Effective: a stable digest over the active set.
	eff, err := client.Effective(ctx, f.Tenant, f.Space)
	if err != nil {
		t.Fatalf("effective: %v", err)
	}
	if eff.Digest == "" || len(eff.Documents) == 0 {
		t.Fatalf("effective %+v", eff)
	}
	eff2, err := client.Effective(ctx, f.Tenant, f.Space)
	if err != nil || eff2.Digest != eff.Digest {
		t.Fatalf("effective unstable: %v %s vs %s", err, eff2.Digest, eff.Digest)
	}

	// Policy slice: the consumer gets documents; unknown service or
	// digest is not_found.
	slice, err := client.PolicySlice(ctx, f.Tenant, f.Space, f.Consumer, eff.Digest)
	if err != nil {
		t.Fatalf("policy slice: %v", err)
	}
	if slice.Digest != eff.Digest || len(slice.Documents) == 0 {
		t.Fatalf("policy slice %+v", slice)
	}
	if _, err := client.PolicySlice(ctx, f.Tenant, f.Space, "nonesuch", eff.Digest); !errors.Is(err, spawn.ErrNotFound) {
		t.Fatalf("unknown service: want not_found, got %v", err)
	}
	if _, err := client.PolicySlice(ctx, f.Tenant, f.Space, f.Consumer, "sha256:"+zeros(64)); !errors.Is(err, spawn.ErrNotFound) {
		t.Fatalf("unknown digest: want not_found, got %v", err)
	}

	// Tenant isolation: the foreign tenant sees not_found everywhere.
	for name, call := range map[string]func() error{
		"effective": func() error {
			_, err := client.Effective(ctx, f.Foreign, f.Space)
			return err
		},
		"list": func() error {
			_, err := client.ListRevisions(ctx, f.Foreign, f.Space, "")
			return err
		},
		"slice": func() error {
			_, err := client.PolicySlice(ctx, f.Foreign, f.Space, f.Consumer, eff.Digest)
			return err
		},
		"submit": func() error {
			_, err := client.SubmitRevision(ctx, f.Space, "user", spawn.SubmitRevisionRequest{
				Org: f.Foreign.Org, Owner: f.Foreign.Owner, Documents: f.Layers["user"], IdempotencyKey: "k-foreign",
			})
			return err
		},
		"validate": func() error {
			_, err := client.ValidateRevision(ctx, f.Space, ids["user"], spawn.RevisionRequest{
				Org: f.Foreign.Org, Owner: f.Foreign.Owner, IdempotencyKey: "k-foreign-2",
			})
			return err
		},
	} {
		if err := call(); !errors.Is(err, spawn.ErrNotFound) {
			t.Errorf("foreign %s: want not_found, got %v", name, err)
		}
	}
	// The foreign tenant may register its own space under another id — a
	// distinct row that never shadows the owner's; the owner's space stays
	// invisible to it on every subsequent leg.
	if _, err := client.CreateSpace(ctx, spawn.CreateSpaceRequest{
		Org: f.Foreign.Org, Owner: f.Foreign.Owner, ID: f.Space + "-own", Environment: "dev", IdempotencyKey: "k-foreign-3",
	}); err != nil {
		t.Fatalf("foreign create space: %v", err)
	}
	if _, err := client.Effective(ctx, f.Foreign, f.Space); !errors.Is(err, spawn.ErrNotFound) {
		t.Fatalf("foreign effective after own space: want not_found, got %v", err)
	}

	// Unknown space and malformed inputs are refused before dispatch.
	for name, call := range map[string]func() error{
		"missing space": func() error {
			_, err := client.Effective(ctx, f.Tenant, "ghost")
			return err
		},
		"bad space": func() error {
			_, err := client.Effective(ctx, f.Tenant, "Bad/Space")
			return err
		},
		"empty tenant": func() error {
			_, err := client.Effective(ctx, spawn.Tenant{}, f.Space)
			return err
		},
		"bad layer": func() error {
			_, err := client.ListRevisions(ctx, f.Tenant, f.Space, "nonesuch")
			return err
		},
		"bad revision": func() error {
			_, err := client.ActivateRevisions(ctx, f.Space, spawn.ActivationRequest{
				Org: f.Tenant.Org, Owner: f.Tenant.Owner, Revisions: []string{"rev-nope"}, IdempotencyKey: "k-bad",
			})
			return err
		},
		"empty activation": func() error {
			_, err := client.ActivateRevisions(ctx, f.Space, spawn.ActivationRequest{
				Org: f.Tenant.Org, Owner: f.Tenant.Owner, IdempotencyKey: "k-empty",
			})
			return err
		},
		"missing key": func() error {
			_, err := client.SubmitRevision(ctx, f.Space, "user", spawn.SubmitRevisionRequest{
				Org: f.Tenant.Org, Owner: f.Tenant.Owner, Documents: f.Layers["user"],
			})
			return err
		},
	} {
		if err := call(); !errors.Is(err, spawn.ErrInvalid) && !errors.Is(err, spawn.ErrNotFound) {
			t.Errorf("%s: want invalid or not_found, got %v", name, err)
		}
	}
}

func zeros(n int) string {
	out := make([]byte, n)
	for i := range out {
		out[i] = '0'
	}
	return string(out)
}

// Both runs the suite against the in-process transport over server and
// the HTTP transport over handler, then verifies bearer enforcement.
func Both(t *testing.T, server spawn.Client, handler func(token string) http.Handler, f Fixture) {
	t.Helper()
	const token = "conformance-token"
	testkit.Transports(t, spawn.NewInProc(server), handler(token), func(url string, hc *http.Client) spawn.Client {
		return spawn.NewHTTPClient(url, token, hc)
	}, func(t *testing.T, client spawn.Client) { Run(t, client, f) })
	t.Run("forbidden", func(t *testing.T) {
		srv := httptest.NewServer(handler(token))
		defer srv.Close()
		if _, err := spawn.NewHTTPClient(srv.URL, "wrong", srv.Client()).Effective(context.Background(), f.Tenant, f.Space); !errors.Is(err, spawn.ErrForbidden) {
			t.Fatalf("wrong token: want forbidden, got %v", err)
		}
	})
}
