package spawn

import (
	"context"
	"net/http"
	"net/url"
	"strings"

	identityv1 "github.com/letya999/hermes-hub/contracts/identity/v1"
	"github.com/letya999/hermes-hub/shared/auth"
	"github.com/letya999/hermes-hub/shared/httpapi"
)

// Runtime is one durable runtime row: the claim binds tenant, space,
// agent, policy digest and image digest to a generation whose workload
// serves at Address — the endpoint the claim's caller forwards to.
// HostPort is the workload's loopback-published control port when the
// backend publishes one; a caller outside the workload's network reaches
// the same endpoint as Address through 127.0.0.1:HostPort.
type Runtime struct {
	Org          string `json:"org"`
	Owner        string `json:"owner"`
	ID           string `json:"id"`
	Space        string `json:"space"`
	Agent        string `json:"agent"`
	Generation   int64  `json:"generation"`
	State        string `json:"state"`
	PolicyDigest string `json:"policy_digest"`
	ImageDigest  string `json:"image_digest"`
	Address      string `json:"address,omitempty"`
	HostPort     int    `json:"host_port,omitempty"`
}

// ClaimRuntimeRequest is the POST /v1/spaces/{space}/runtimes body: the
// tenant and the agent the runtime serves — the digests the claim binds
// come from the space's stored effective spec, never from the caller.
// The routing and identity fields carry the caller's job binding into
// the workload's environment; the row binds runtime_id and the identity
// material at spawn — a warm row claimed under a rotated identity
// revision or a different runtime id is never reused. Omitted identity
// material inherits the previous generation's binding.
type ClaimRuntimeRequest struct {
	Org              string                   `json:"org"`
	Owner            string                   `json:"owner"`
	Agent            string                   `json:"agent"`
	RuntimeID        string                   `json:"runtime_id,omitempty"`
	RuntimeMode      string                   `json:"runtime_mode,omitempty"`
	UserID           string                   `json:"user_id,omitempty"`
	OrganizationID   string                   `json:"organization_id,omitempty"`
	PolicyVersion    string                   `json:"policy_version,omitempty"`
	IdentityRevision string                   `json:"identity_revision,omitempty"`
	Restrictions     *identityv1.Restrictions `json:"restrictions,omitempty"`
}

// ClaimedRuntime is the claim's answer: the ready runtime row (its
// Address is the endpoint the caller forwards to) and the lease the
// caller renews and releases.
type ClaimedRuntime struct {
	Runtime Runtime `json:"runtime"`
	Lease   string  `json:"lease"`
}

// LeaseRequest is the renew/release identity: the tenant, with the lease
// id travelling in the path (POST /v1/leases/{id}/renew,
// DELETE /v1/leases/{id}).
type LeaseRequest struct {
	Org   string `json:"org"`
	Owner string `json:"owner"`
}

// DrainRuntimesRequest selects a space's live runtime rows for drain —
// respawn/replace forces a fresh generation while the old row's workload
// stops under the machine's stop budget; the optional Agent scopes it to
// one runtime lane (POST /v1/spaces/{space}/runtimes/drain).
type DrainRuntimesRequest struct {
	Org   string `json:"org"`
	Owner string `json:"owner"`
	Agent string `json:"agent,omitempty"`
}

// claimField bounds the routing values the claim carries into the
// workload's environment — a newline or an overlong value would inject
// into the env-file and -e channels.
func claimField(s string) bool {
	return len(s) <= 256 && !strings.ContainsAny(s, "\r\n")
}

func validRuntimeClaim(space string, in ClaimRuntimeRequest) bool {
	return auth.ValidID(space) && validTenant(Tenant{in.Org, in.Owner}) &&
		in.Agent != "" && claimField(in.Agent) &&
		claimField(in.RuntimeID) && claimField(in.UserID) && claimField(in.OrganizationID) &&
		claimField(in.PolicyVersion) && claimField(in.IdentityRevision)
}

func validLeaseID(id string) bool {
	return strings.HasPrefix(id, "ls-") && len(id) == 19
}

func validRuntimeLease(id string, in LeaseRequest) bool {
	return validLeaseID(id) && validTenant(Tenant{in.Org, in.Owner})
}

func validRuntimeDrain(space string, in DrainRuntimesRequest) bool {
	return auth.ValidID(space) && validTenant(Tenant{in.Org, in.Owner}) &&
		(in.Agent == "" || claimField(in.Agent))
}

func (c inProc) ClaimRuntime(ctx context.Context, space string, in ClaimRuntimeRequest) (ClaimedRuntime, error) {
	if !validRuntimeClaim(space, in) {
		return ClaimedRuntime{}, httpapi.Errorf(ErrInvalid, "invalid runtime claim")
	}
	out, err := c.server.ClaimRuntime(ctx, space, in)
	if err != nil {
		return ClaimedRuntime{}, httpapi.Normalize(err)
	}
	return httpapi.Copy(out)
}

func (c inProc) RenewLease(ctx context.Context, id string, in LeaseRequest) (Runtime, error) {
	if !validRuntimeLease(id, in) {
		return Runtime{}, httpapi.Errorf(ErrInvalid, "invalid lease renew")
	}
	out, err := c.server.RenewLease(ctx, id, in)
	if err != nil {
		return Runtime{}, httpapi.Normalize(err)
	}
	return httpapi.Copy(out)
}

func (c inProc) ReleaseLease(ctx context.Context, id string, in LeaseRequest) error {
	if !validRuntimeLease(id, in) {
		return httpapi.Errorf(ErrInvalid, "invalid lease release")
	}
	return httpapi.Normalize(c.server.ReleaseLease(ctx, id, in))
}

func (c inProc) ListRuntimes(ctx context.Context, t Tenant) ([]Runtime, error) {
	if !validTenant(t) {
		return nil, httpapi.Errorf(ErrInvalid, "invalid runtime list")
	}
	out, err := c.server.ListRuntimes(ctx, t)
	if err != nil {
		return nil, httpapi.Normalize(err)
	}
	return httpapi.Copy(out)
}

func (c inProc) DrainRuntimes(ctx context.Context, space string, in DrainRuntimesRequest) (int, error) {
	if !validRuntimeDrain(space, in) {
		return 0, httpapi.Errorf(ErrInvalid, "invalid runtime drain")
	}
	n, err := c.server.DrainRuntimes(ctx, space, in)
	if err != nil {
		return 0, httpapi.Normalize(err)
	}
	return n, nil
}

func (c httpClient) ClaimRuntime(ctx context.Context, space string, in ClaimRuntimeRequest) (ClaimedRuntime, error) {
	if !validRuntimeClaim(space, in) {
		return ClaimedRuntime{}, httpapi.Errorf(ErrInvalid, "invalid runtime claim")
	}
	var out ClaimedRuntime
	err := c.Do(ctx, http.MethodPost, "/v1/spaces/"+url.PathEscape(space)+"/runtimes", in, &out)
	return out, err
}

func (c httpClient) RenewLease(ctx context.Context, id string, in LeaseRequest) (Runtime, error) {
	if !validRuntimeLease(id, in) {
		return Runtime{}, httpapi.Errorf(ErrInvalid, "invalid lease renew")
	}
	var out Runtime
	err := c.Do(ctx, http.MethodPost, "/v1/leases/"+url.PathEscape(id)+"/renew", in, &out)
	return out, err
}

func (c httpClient) ReleaseLease(ctx context.Context, id string, in LeaseRequest) error {
	if !validRuntimeLease(id, in) {
		return httpapi.Errorf(ErrInvalid, "invalid lease release")
	}
	// DELETE carries the tenant in the query — a body on DELETE is
	// unreliable across transports; the lease id is already in the path.
	t := Tenant{Org: in.Org, Owner: in.Owner}
	var discard struct{}
	return c.Do(ctx, http.MethodDelete, "/v1/leases/"+url.PathEscape(id)+"?"+tenantQuery(t), nil, &discard)
}

func (c httpClient) ListRuntimes(ctx context.Context, t Tenant) ([]Runtime, error) {
	if !validTenant(t) {
		return nil, httpapi.Errorf(ErrInvalid, "invalid runtime list")
	}
	var out []Runtime
	err := c.Do(ctx, http.MethodGet, "/v1/runtimes?"+tenantQuery(t), nil, &out)
	return out, err
}

func (c httpClient) DrainRuntimes(ctx context.Context, space string, in DrainRuntimesRequest) (int, error) {
	if !validRuntimeDrain(space, in) {
		return 0, httpapi.Errorf(ErrInvalid, "invalid runtime drain")
	}
	var out struct {
		Drained int `json:"drained"`
	}
	err := c.Do(ctx, http.MethodPost, "/v1/spaces/"+url.PathEscape(space)+"/runtimes/drain", in, &out)
	return out.Drained, err
}

// handleLease routes the /v1/leases/{id} surface — renew (POST body) and
// release (DELETE, tenant query) — called from Handler when the path
// prefix matches.
func handleLease(client Client, r *http.Request) (any, error) {
	rest := strings.Split(strings.TrimPrefix(r.URL.Path, "/v1/leases/"), "/")
	if len(rest) > 2 || rest[0] == "" {
		return nil, ErrNotFound
	}
	switch {
	case r.Method == http.MethodPost && len(rest) == 2 && rest[1] == "renew":
		var in LeaseRequest
		if err := httpapi.Decode(r, &in); err != nil {
			return nil, err
		}
		return client.RenewLease(r.Context(), rest[0], in)
	case r.Method == http.MethodDelete && len(rest) == 1:
		t, err := queryTenant(r)
		if err != nil {
			return nil, err
		}
		return nil, client.ReleaseLease(r.Context(), rest[0], LeaseRequest{Org: t.Org, Owner: t.Owner})
	default:
		return nil, ErrNotFound
	}
}
