// Package spawn is the v1 contract of spawn-controller: tenant-owned
// spaces, the layered passport revisions they compile into effective
// specs, and the per-consumer policy slices those specs serve
// (ADR-0105, SPEC-0100). Every call carries org and owner explicitly —
// callers are trusted control-plane clients holding the service token.
package spawn

import (
	"context"
	"encoding/json"
	"net/http"
	"net/url"
	"regexp"
	"strings"

	"github.com/letya999/hermes-hub/shared/auth"
	"github.com/letya999/hermes-hub/shared/httpapi"
)

// Typed errors (SPEC-0100 R2.5).
var (
	ErrNotFound    = httpapi.ErrNotFound
	ErrConflict    = httpapi.ErrConflict
	ErrForbidden   = httpapi.ErrForbidden
	ErrInvalid     = httpapi.ErrInvalid
	ErrUnavailable = httpapi.ErrUnavailable
)

// Tenant owns the rows a call touches; both fields travel in every DTO —
// never derived from a header or another principal.
type Tenant struct {
	Org   string `json:"org"`
	Owner string `json:"owner"`
}

// Layers is the ordered passport layer vocabulary the service accepts
// (ADR-0106); internal/spawn keeps the same order in its own constant.
var Layers = []string{
	"platform-ceiling",
	"platform-template",
	"org-ceiling",
	"org-template",
	"user",
	"agent",
}

// ValidLayer reports whether name is one of the six layer slots.
func ValidLayer(name string) bool {
	for _, l := range Layers {
		if l == name {
			return true
		}
	}
	return false
}

var revisionID = regexp.MustCompile(`^rev-[0-9a-f]{16}$`)

// ValidRevisionID is the revision row id rule (content-derived, not the
// provenance r-<hex> id).
func ValidRevisionID(id string) bool { return revisionID.MatchString(id) }

// Space is one managed space's registry row.
type Space struct {
	Org         string `json:"org"`
	Owner       string `json:"owner"`
	ID          string `json:"id"`
	Environment string `json:"environment"`
	CreatedAt   int64  `json:"created_at"`
}

// Revision is one submitted layer document set and its lifecycle state.
type Revision struct {
	ID           string `json:"id"`
	Space        string `json:"space"`
	Layer        string `json:"layer"`
	State        string `json:"state"`
	Digest       string `json:"digest"`
	Error        string `json:"error,omitempty"`
	Detail       string `json:"detail,omitempty"`
	CreatedAt    int64  `json:"created_at"`
	BaseRevision string `json:"base_revision,omitempty"`
}

// EffectiveSpec is the compiled policy of the space's active layers; the
// digest is "sha256:" + hex(sha256(PolicyBundle().Canonical())).
type EffectiveSpec struct {
	Space     string                     `json:"space"`
	Digest    string                     `json:"digest"`
	Documents map[string]json.RawMessage `json:"documents"`
}

// PolicySlice is the subset of one effective spec attributed to one
// consumer service; the response is immutable and ETag'd by digest.
type PolicySlice struct {
	Space     string                     `json:"space"`
	Service   string                     `json:"service"`
	Digest    string                     `json:"digest"`
	Documents map[string]json.RawMessage `json:"documents"`
}

// ExplainField is one leaf of the effective bundle: its value and the
// layer that set it — one of Layers, or "derived" for compiler output.
type ExplainField struct {
	Doc   string `json:"doc"`
	Path  string `json:"path"`
	Value any    `json:"value"`
	Layer string `json:"layer"`
}

// ExplainReport is the per-leaf provenance of the space's active spec
// (GET /v1/spaces/{space}/explain?org=&owner=).
type ExplainReport struct {
	Space  string         `json:"space"`
	Digest string         `json:"digest"`
	Fields []ExplainField `json:"fields"`
}

// CreateSpaceRequest registers one tenant space (POST /v1/spaces).
type CreateSpaceRequest struct {
	Org            string `json:"org"`
	Owner          string `json:"owner"`
	ID             string `json:"id"`
	Environment    string `json:"environment"`
	IdempotencyKey string `json:"idempotency_key"`
}

// SubmitRevisionRequest carries one layer's six-document set
// (POST /v1/spaces/{space}/layers/{layer}/revisions); the space and layer
// travel in the path.
type SubmitRevisionRequest struct {
	Org            string                    `json:"org"`
	Owner          string                    `json:"owner"`
	Documents      map[string]map[string]any `json:"documents"`
	IdempotencyKey string                    `json:"idempotency_key"`
}

// RevisionRequest is the validate body — only the tenant and the
// idempotency key travel; space and revision id are in the path.
type RevisionRequest struct {
	Org            string `json:"org"`
	Owner          string `json:"owner"`
	IdempotencyKey string `json:"idempotency_key"`
}

// ActivationRequest is the set-activation body (POST
// /v1/spaces/{space}/activations): one or more revision ids, at most one
// per layer, activated atomically — the cut commits only when the
// would-be active set compiles, so a space can never lose a serving
// policy to a partial move.
type ActivationRequest struct {
	Org            string   `json:"org"`
	Owner          string   `json:"owner"`
	Revisions      []string `json:"revisions"`
	IdempotencyKey string   `json:"idempotency_key"`
}

// Client is the spawn-controller surface. Path-shaped arguments come
// first on the write calls (space, layer, revision id) so a server can
// never take them from a body field.
type Client interface {
	CreateSpace(context.Context, CreateSpaceRequest) (Space, error)
	SubmitRevision(context.Context, string, string, SubmitRevisionRequest) (Revision, error)
	ValidateRevision(context.Context, string, string, RevisionRequest) (Revision, error)
	ActivateRevisions(context.Context, string, ActivationRequest) ([]Revision, error)
	Effective(context.Context, Tenant, string) (EffectiveSpec, error)
	PolicySlice(context.Context, Tenant, string, string, string) (PolicySlice, error)
	ListRevisions(context.Context, Tenant, string, string) ([]Revision, error)
	Explain(context.Context, Tenant, string) (ExplainReport, error)
}

func validTenant(t Tenant) bool {
	ok := func(v string) bool { return v != "" && len(v) <= 256 && !strings.ContainsAny(v, "\x00\r\n") }
	return ok(t.Org) && ok(t.Owner)
}

func validActivation(space string, in ActivationRequest) bool {
	if !auth.ValidID(space) || !validTenant(Tenant{in.Org, in.Owner}) || len(in.Revisions) == 0 || in.IdempotencyKey == "" {
		return false
	}
	for _, id := range in.Revisions {
		if !ValidRevisionID(id) {
			return false
		}
	}
	return true
}

type inProc struct{ server Client }

// NewInProc wraps a server for same-process callers.
func NewInProc(server Client) Client { return inProc{server} }

func (c inProc) CreateSpace(ctx context.Context, in CreateSpaceRequest) (Space, error) {
	if !validTenant(Tenant{in.Org, in.Owner}) || !auth.ValidID(in.ID) || in.IdempotencyKey == "" {
		return Space{}, httpapi.Errorf(ErrInvalid, "invalid create space request")
	}
	out, err := c.server.CreateSpace(ctx, in)
	if err != nil {
		return Space{}, httpapi.Normalize(err)
	}
	return httpapi.Copy(out)
}

func (c inProc) SubmitRevision(ctx context.Context, space, layer string, in SubmitRevisionRequest) (Revision, error) {
	if !auth.ValidID(space) || !ValidLayer(layer) || !validTenant(Tenant{in.Org, in.Owner}) || len(in.Documents) == 0 || in.IdempotencyKey == "" {
		return Revision{}, httpapi.Errorf(ErrInvalid, "invalid submit request")
	}
	out, err := c.server.SubmitRevision(ctx, space, layer, in)
	if err != nil {
		return Revision{}, httpapi.Normalize(err)
	}
	return httpapi.Copy(out)
}

func (c inProc) ValidateRevision(ctx context.Context, space, id string, in RevisionRequest) (Revision, error) {
	if !auth.ValidID(space) || !ValidRevisionID(id) || !validTenant(Tenant{in.Org, in.Owner}) || in.IdempotencyKey == "" {
		return Revision{}, httpapi.Errorf(ErrInvalid, "invalid validate request")
	}
	out, err := c.server.ValidateRevision(ctx, space, id, in)
	if err != nil {
		return Revision{}, httpapi.Normalize(err)
	}
	return httpapi.Copy(out)
}

func (c inProc) ActivateRevisions(ctx context.Context, space string, in ActivationRequest) ([]Revision, error) {
	if !validActivation(space, in) {
		return nil, httpapi.Errorf(ErrInvalid, "invalid activation request")
	}
	out, err := c.server.ActivateRevisions(ctx, space, in)
	if err != nil {
		return nil, httpapi.Normalize(err)
	}
	return httpapi.Copy(out)
}

func (c inProc) Effective(ctx context.Context, t Tenant, space string) (EffectiveSpec, error) {
	if !validTenant(t) || !auth.ValidID(space) {
		return EffectiveSpec{}, httpapi.Errorf(ErrInvalid, "invalid effective request")
	}
	out, err := c.server.Effective(ctx, t, space)
	if err != nil {
		return EffectiveSpec{}, httpapi.Normalize(err)
	}
	return httpapi.Copy(out)
}

func (c inProc) PolicySlice(ctx context.Context, t Tenant, space, service, digest string) (PolicySlice, error) {
	if !validTenant(t) || !auth.ValidID(space) || !auth.ValidID(service) || !auth.ValidDigest(digest) {
		return PolicySlice{}, httpapi.Errorf(ErrInvalid, "invalid policy slice request")
	}
	out, err := c.server.PolicySlice(ctx, t, space, service, digest)
	if err != nil {
		return PolicySlice{}, httpapi.Normalize(err)
	}
	return httpapi.Copy(out)
}

func (c inProc) ListRevisions(ctx context.Context, t Tenant, space, layer string) ([]Revision, error) {
	if !validTenant(t) || !auth.ValidID(space) || (layer != "" && !ValidLayer(layer)) {
		return nil, httpapi.Errorf(ErrInvalid, "invalid list request")
	}
	out, err := c.server.ListRevisions(ctx, t, space, layer)
	if err != nil {
		return nil, httpapi.Normalize(err)
	}
	return httpapi.Copy(out)
}

func (c inProc) Explain(ctx context.Context, t Tenant, space string) (ExplainReport, error) {
	if !validTenant(t) || !auth.ValidID(space) {
		return ExplainReport{}, httpapi.Errorf(ErrInvalid, "invalid explain request")
	}
	out, err := c.server.Explain(ctx, t, space)
	if err != nil {
		return ExplainReport{}, httpapi.Normalize(err)
	}
	return httpapi.Copy(out)
}

type httpClient struct{ httpapi.Client }

// NewHTTPClient calls a server at baseURL with a bearer token; hc may be nil.
func NewHTTPClient(baseURL, token string, hc *http.Client) Client {
	return httpClient{httpapi.Client{BaseURL: baseURL, Token: token, HTTP: hc}}
}

func (c httpClient) CreateSpace(ctx context.Context, in CreateSpaceRequest) (Space, error) {
	if !validTenant(Tenant{in.Org, in.Owner}) || !auth.ValidID(in.ID) || in.IdempotencyKey == "" {
		return Space{}, httpapi.Errorf(ErrInvalid, "invalid create space request")
	}
	var out Space
	err := c.Do(ctx, http.MethodPost, "/v1/spaces", in, &out)
	return out, err
}

func (c httpClient) SubmitRevision(ctx context.Context, space, layer string, in SubmitRevisionRequest) (Revision, error) {
	if !auth.ValidID(space) || !ValidLayer(layer) || !validTenant(Tenant{in.Org, in.Owner}) || len(in.Documents) == 0 || in.IdempotencyKey == "" {
		return Revision{}, httpapi.Errorf(ErrInvalid, "invalid submit request")
	}
	var out Revision
	err := c.Do(ctx, http.MethodPost, "/v1/spaces/"+url.PathEscape(space)+"/layers/"+url.PathEscape(layer)+"/revisions", in, &out)
	return out, err
}

func (c httpClient) ValidateRevision(ctx context.Context, space, id string, in RevisionRequest) (Revision, error) {
	if !auth.ValidID(space) || !ValidRevisionID(id) || !validTenant(Tenant{in.Org, in.Owner}) || in.IdempotencyKey == "" {
		return Revision{}, httpapi.Errorf(ErrInvalid, "invalid validate request")
	}
	var out Revision
	err := c.Do(ctx, http.MethodPost, "/v1/spaces/"+url.PathEscape(space)+"/revisions/"+url.PathEscape(id)+"/validate", in, &out)
	return out, err
}

func (c httpClient) ActivateRevisions(ctx context.Context, space string, in ActivationRequest) ([]Revision, error) {
	if !validActivation(space, in) {
		return nil, httpapi.Errorf(ErrInvalid, "invalid activation request")
	}
	var out []Revision
	err := c.Do(ctx, http.MethodPost, "/v1/spaces/"+url.PathEscape(space)+"/activations", in, &out)
	return out, err
}

func (c httpClient) Effective(ctx context.Context, t Tenant, space string) (EffectiveSpec, error) {
	if !validTenant(t) || !auth.ValidID(space) {
		return EffectiveSpec{}, httpapi.Errorf(ErrInvalid, "invalid effective request")
	}
	var out EffectiveSpec
	err := c.Do(ctx, http.MethodGet, "/v1/spaces/"+url.PathEscape(space)+"/effective?"+tenantQuery(t), nil, &out)
	return out, err
}

func (c httpClient) PolicySlice(ctx context.Context, t Tenant, space, service, digest string) (PolicySlice, error) {
	if !validTenant(t) || !auth.ValidID(space) || !auth.ValidID(service) || !auth.ValidDigest(digest) {
		return PolicySlice{}, httpapi.Errorf(ErrInvalid, "invalid policy slice request")
	}
	var out PolicySlice
	err := c.Do(ctx, http.MethodGet, "/v1/spaces/"+url.PathEscape(space)+"/policy/"+url.PathEscape(service)+"?"+tenantQuery(t)+"&digest="+url.QueryEscape(digest), nil, &out)
	return out, err
}

func (c httpClient) ListRevisions(ctx context.Context, t Tenant, space, layer string) ([]Revision, error) {
	if !validTenant(t) || !auth.ValidID(space) || (layer != "" && !ValidLayer(layer)) {
		return nil, httpapi.Errorf(ErrInvalid, "invalid list request")
	}
	query := tenantQuery(t)
	if layer != "" {
		query += "&layer=" + url.QueryEscape(layer)
	}
	var out []Revision
	err := c.Do(ctx, http.MethodGet, "/v1/spaces/"+url.PathEscape(space)+"/revisions?"+query, nil, &out)
	return out, err
}

func (c httpClient) Explain(ctx context.Context, t Tenant, space string) (ExplainReport, error) {
	if !validTenant(t) || !auth.ValidID(space) {
		return ExplainReport{}, httpapi.Errorf(ErrInvalid, "invalid explain request")
	}
	var out ExplainReport
	err := c.Do(ctx, http.MethodGet, "/v1/spaces/"+url.PathEscape(space)+"/explain?"+tenantQuery(t), nil, &out)
	return out, err
}

func tenantQuery(t Tenant) string {
	return "org=" + url.QueryEscape(t.Org) + "&owner=" + url.QueryEscape(t.Owner)
}

// Handler serves the eight spawn routes behind the shared bearer check;
// the policy route's success response is immutable with ETag = digest.
func Handler(server Client, token string) http.Handler {
	inner := httpapi.Handle(token, func(r *http.Request) (any, error) {
		client := NewInProc(server)
		segments := strings.Split(strings.TrimPrefix(r.URL.Path, "/v1/spaces"), "/")
		// POST /v1/spaces is the only route with an empty segment list.
		if r.Method == http.MethodPost && r.URL.Path == "/v1/spaces" {
			var in CreateSpaceRequest
			if err := httpapi.Decode(r, &in); err != nil {
				return nil, err
			}
			return client.CreateSpace(r.Context(), in)
		}
		if len(segments) < 2 || segments[0] != "" {
			return nil, ErrNotFound
		}
		space := segments[1]
		rest := segments[2:]
		switch {
		case r.Method == http.MethodPost && len(rest) == 3 && rest[0] == "layers" && rest[2] == "revisions":
			var in SubmitRevisionRequest
			if err := httpapi.Decode(r, &in); err != nil {
				return nil, err
			}
			return client.SubmitRevision(r.Context(), space, rest[1], in)
		case r.Method == http.MethodPost && len(rest) == 3 && rest[0] == "revisions" && rest[2] == "validate":
			var in RevisionRequest
			if err := httpapi.Decode(r, &in); err != nil {
				return nil, err
			}
			return client.ValidateRevision(r.Context(), space, rest[1], in)
		case r.Method == http.MethodPost && len(rest) == 1 && rest[0] == "activations":
			var in ActivationRequest
			if err := httpapi.Decode(r, &in); err != nil {
				return nil, err
			}
			return client.ActivateRevisions(r.Context(), space, in)
		case r.Method == http.MethodGet && len(rest) == 1 && rest[0] == "revisions":
			t, err := queryTenant(r)
			if err != nil {
				return nil, err
			}
			return client.ListRevisions(r.Context(), t, space, r.URL.Query().Get("layer"))
		case r.Method == http.MethodGet && len(rest) == 1 && rest[0] == "effective":
			t, err := queryTenant(r)
			if err != nil {
				return nil, err
			}
			return client.Effective(r.Context(), t, space)
		case r.Method == http.MethodGet && len(rest) == 1 && rest[0] == "explain":
			t, err := queryTenant(r)
			if err != nil {
				return nil, err
			}
			return client.Explain(r.Context(), t, space)
		case r.Method == http.MethodGet && len(rest) == 2 && rest[0] == "policy":
			t, err := queryTenant(r)
			if err != nil {
				return nil, err
			}
			return client.PolicySlice(r.Context(), t, space, rest[1], r.URL.Query().Get("digest"))
		default:
			return nil, ErrNotFound
		}
	})
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodGet && strings.HasPrefix(r.URL.Path, "/v1/spaces/") && strings.Contains(r.URL.Path, "/policy/") {
			w = policyHeaders{ResponseWriter: w, digest: r.URL.Query().Get("digest")}
		}
		inner.ServeHTTP(w, r)
	})
}

func queryTenant(r *http.Request) (Tenant, error) {
	t := Tenant{Org: r.URL.Query().Get("org"), Owner: r.URL.Query().Get("owner")}
	if !validTenant(t) {
		return t, httpapi.Errorf(ErrInvalid, "tenant query parameters required")
	}
	return t, nil
}

// policyHeaders injects the immutable-caching headers onto a successful
// policy-slice response — error responses keep no cache signal.
type policyHeaders struct {
	http.ResponseWriter
	digest string
}

func (w policyHeaders) WriteHeader(code int) {
	if code == http.StatusOK {
		w.Header().Set("Cache-Control", "public, max-age=31536000, immutable")
		if w.digest != "" {
			w.Header().Set("ETag", w.digest)
		}
	}
	w.ResponseWriter.WriteHeader(code)
}
