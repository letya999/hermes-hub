package spawn_test

// Runtime-claim tenant isolation: identical space ids and agent names
// under different tenants never resolve to the same runtime — each claim
// binds its own tenant's stored digests, and a foreign tenant cannot
// renew, release or even list the owner's row.

import (
	"context"
	"errors"
	"net/http"
	"testing"

	spawn "github.com/letya999/hermes-hub/contracts/spawn/v1"
	"github.com/letya999/hermes-hub/contracts/spawn/v1/fake"
	"github.com/letya999/hermes-hub/shared/testkit"
)

func TestRuntimeClaimNeverCrossesTenants(t *testing.T) {
	server := &fake.Server{}
	testkit.Transports(t, spawn.NewInProc(server), spawn.Handler(server, "tok"),
		func(url string, hc *http.Client) spawn.Client { return spawn.NewHTTPClient(url, "tok", hc) },
		func(t *testing.T, client spawn.Client) {
			ctx := context.Background()
			owner := spawn.Tenant{Org: "org-a", Owner: "alice"}
			foreign := spawn.Tenant{Org: "org-b", Owner: "mallory"}
			// Both tenants own a space carrying the SAME id — the tenant,
			// not the space id, scopes the runtime.
			for _, tenant := range []spawn.Tenant{owner, foreign} {
				if _, err := client.CreateSpace(ctx, spawn.CreateSpaceRequest{
					Org: tenant.Org, Owner: tenant.Owner, ID: "alice", Environment: "dev",
					IdempotencyKey: "k-" + tenant.Org,
				}); err != nil {
					t.Fatalf("create space for %s: %v", tenant.Org, err)
				}
			}
			mine, err := client.ClaimRuntime(ctx, "alice", spawn.ClaimRuntimeRequest{
				Org: owner.Org, Owner: owner.Owner, Agent: "main",
			})
			if err != nil {
				t.Fatalf("owner claim: %v", err)
			}
			theirs, err := client.ClaimRuntime(ctx, "alice", spawn.ClaimRuntimeRequest{
				Org: foreign.Org, Owner: foreign.Owner, Agent: "main",
			})
			if err != nil {
				t.Fatalf("foreign claim: %v", err)
			}
			if mine.Runtime.ID == theirs.Runtime.ID || mine.Lease == theirs.Lease {
				t.Fatalf("identical space+agent under different tenants must not share a runtime or lease: %s %s",
					mine.Runtime.ID, mine.Lease)
			}
			// A foreign tenant cannot renew or release the owner's lease and
			// never lists the owner's row.
			if _, err := client.RenewLease(ctx, mine.Lease, spawn.LeaseRequest{Org: foreign.Org, Owner: foreign.Owner}); !errors.Is(err, spawn.ErrNotFound) {
				t.Fatalf("foreign renew: want not_found, got %v", err)
			}
			rows, err := client.ListRuntimes(ctx, foreign)
			if err != nil {
				t.Fatalf("foreign list: %v", err)
			}
			for _, r := range rows {
				if r.ID == mine.Runtime.ID {
					t.Fatalf("foreign inventory carries the owner's runtime: %+v", rows)
				}
			}
			// The foreign release is a no-op, not a reach across tenants:
			// the owner's lease still renews afterwards.
			if err := client.ReleaseLease(ctx, mine.Lease, spawn.LeaseRequest{Org: foreign.Org, Owner: foreign.Owner}); err != nil {
				t.Fatalf("foreign release must be a no-op: %v", err)
			}
			if _, err := client.RenewLease(ctx, mine.Lease, spawn.LeaseRequest{Org: owner.Org, Owner: owner.Owner}); err != nil {
				t.Fatalf("owner renew after foreign release: %v", err)
			}
		})
}
