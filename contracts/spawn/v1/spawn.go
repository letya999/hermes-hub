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
// policy to a partial move. BaseDigest, when set, is the compare-and-swap
// anchor on the space's effective digest — the apply rollback uses it to
// prove the new set is still live before re-activating the previous one.
type ActivationRequest struct {
	Org            string   `json:"org"`
	Owner          string   `json:"owner"`
	Revisions      []string `json:"revisions"`
	BaseDigest     string   `json:"base_digest,omitempty"`
	IdempotencyKey string   `json:"idempotency_key"`
}

// Change is one classified diff leaf (POST /v1/spaces/{space}/apply
// response): the concrete path, the rendered before/after and the effect
// class the consumer registry assigns — the same shape passport.Diff
// emits inside the service.
type Change struct {
	Path     string `json:"path"`
	Before   string `json:"before"`
	After    string `json:"after"`
	Effect   string `json:"effect"`
	Consumer string `json:"consumer"`
}

// Action is one deduplicated reconcile unit the apply response carries —
// the caller executes it through its own effect handlers (compose,
// supervisor respawn); spawn-controller never runs them.
type Action struct {
	Kind      string   `json:"kind"`
	Target    string   `json:"target"`
	Consumers []string `json:"consumers"`
	Paths     []string `json:"paths"`
}

// ApplyRequest submits one or more layer document sets for compile and
// — without dry_run — atomic activation (POST
// /v1/spaces/{space}/apply). BaseDigest is the CAS anchor on the space's
// current effective digest ("" on the first apply); a mismatch refuses
// the whole call and writes nothing.
type ApplyRequest struct {
	Org            string                               `json:"org"`
	Owner          string                               `json:"owner"`
	Layers         map[string]map[string]map[string]any `json:"layers"`
	BaseDigest     string                               `json:"base_digest"`
	DryRun         bool                                 `json:"dry_run"`
	IdempotencyKey string                               `json:"idempotency_key"`
}

// ApplyResponse is the apply receipt: the submitted layer rows, the
// would-be (dry run) or new (confirm) effective digest, the digest it
// replaced, the previous layer set for a rollback activation, the
// classified diff and the action plan the caller executes. Documents
// carries the compiled effective bundle so callers that must read its
// leaves (rights ceilings, connector bindings) never recompile.
type ApplyResponse struct {
	Revisions         []Revision                 `json:"revisions"`
	EffectiveDigest   string                     `json:"effective_digest"`
	PreviousDigest    string                     `json:"previous_digest"`
	PreviousRevisions []string                   `json:"previous_revisions"`
	Changes           []Change                   `json:"changes"`
	Actions           []Action                   `json:"actions"`
	Documents         map[string]json.RawMessage `json:"documents"`
}

// SettingsResult is the self-settings read (GET
// /v1/spaces/{space}/settings): the published values of the reviewed
// allowlist plus its keys. The revision fields carry the effective
// digest — the tool schema keeps the published_revision name.
type SettingsResult struct {
	PublishedRevision    string         `json:"published_revision"`
	EffectiveRevision    string         `json:"effective_revision"`
	PublishedValues      map[string]any `json:"published_values"`
	ManageableKeys       []string       `json:"manageable_keys"`
	SecretValuesIncluded bool           `json:"secret_values_included"`
}

// SettingsEditRequest is one allowlisted config leaf edit (POST
// /v1/spaces/{space}/settings). BaseRevision carries the digest the
// caller last read — the tool argument keeps its base_revision name.
type SettingsEditRequest struct {
	Org            string  `json:"org"`
	Owner          string  `json:"owner"`
	Key            string  `json:"key"`
	Value          *string `json:"value,omitempty"`
	BaseRevision   string  `json:"base_revision"`
	IdempotencyKey string  `json:"idempotency_key"`
}

// SettingsEditResult reports the edit's activation; Changed is false
// when the edit is a no-op on the published spec.
type SettingsEditResult struct {
	PublishedRevision string `json:"published_revision"`
	PreviousRevision  string `json:"previous_revision"`
	Changed           bool   `json:"changed"`
}

// Client is the spawn-controller surface. Path-shaped arguments come
// first on the write calls (space, layer, revision id) so a server can
// never take them from a body field.
// EffectiveReader is the read-only passport slice consumers need (S7d): the
// durable effective spec and an immutable policy slice by digest. Callers
// with a full Client pass it directly; narrower seams (supervisor, toolhub
// gateways) carry just this surface.
type EffectiveReader interface {
	Effective(context.Context, Tenant, string) (EffectiveSpec, error)
	PolicySlice(context.Context, Tenant, string, string, string) (PolicySlice, error)
}

type Client interface {
	CreateSpace(context.Context, CreateSpaceRequest) (Space, error)
	SubmitRevision(context.Context, string, string, SubmitRevisionRequest) (Revision, error)
	ValidateRevision(context.Context, string, string, RevisionRequest) (Revision, error)
	ActivateRevisions(context.Context, string, ActivationRequest) ([]Revision, error)
	// Apply is the passport-writer surface: submit + validate + compile
	// every changed layer in one call. dry_run stops before activation;
	// without it the set cuts atomically on the base_digest CAS.
	Apply(context.Context, string, ApplyRequest) (ApplyResponse, error)
	// Settings is the reviewed self-settings surface: the read returns
	// the published allowlist values; the edit applies one config leaf
	// on the base digest CAS and activates the patched user layer.
	GetSettings(context.Context, Tenant, string, string) (SettingsResult, error)
	EditSettings(context.Context, string, SettingsEditRequest) (SettingsEditResult, error)
	Effective(context.Context, Tenant, string) (EffectiveSpec, error)
	PolicySlice(context.Context, Tenant, string, string, string) (PolicySlice, error)
	ListRevisions(context.Context, Tenant, string, string) ([]Revision, error)
	Explain(context.Context, Tenant, string) (ExplainReport, error)
	// Workers are the disposable-cell surface (decision 12): claim, exec
	// and the lease lifecycle, plus the identity-selector drain that backs
	// ToolHub's /cli-release.
	ClaimWorker(context.Context, string, ClaimWorkerRequest) (ClaimedWorker, error)
	RenewWorker(context.Context, string, WorkerLeaseRequest) (Worker, error)
	ReleaseWorker(context.Context, string, WorkerLeaseRequest) error
	DrainWorkers(context.Context, string, DrainWorkersRequest) (int, error)
	ExecWorker(context.Context, string, WorkerExecRequest) (WorkerExecResult, error)
	// Runtimes are the tenant's warm-pool surface: the claim binds the
	// space's stored digests to a ready runtime and hands back the lease
	// the caller renews and releases around its forwards; the inventory
	// is tenant-scoped.
	ClaimRuntime(context.Context, string, ClaimRuntimeRequest) (ClaimedRuntime, error)
	RenewLease(context.Context, string, LeaseRequest) (Runtime, error)
	ReleaseLease(context.Context, string, LeaseRequest) error
	ListRuntimes(context.Context, Tenant) ([]Runtime, error)
	DrainRuntimes(context.Context, string, DrainRuntimesRequest) (int, error)
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

func validApply(space string, in ApplyRequest) bool {
	if !auth.ValidID(space) || !validTenant(Tenant{in.Org, in.Owner}) || len(in.Layers) == 0 || in.IdempotencyKey == "" {
		return false
	}
	for layer, docs := range in.Layers {
		if !ValidLayer(layer) || len(docs) == 0 {
			return false
		}
	}
	return in.BaseDigest == "" || auth.ValidDigest(in.BaseDigest)
}

func (c inProc) Apply(ctx context.Context, space string, in ApplyRequest) (ApplyResponse, error) {
	if !validApply(space, in) {
		return ApplyResponse{}, httpapi.Errorf(ErrInvalid, "invalid apply request")
	}
	out, err := c.server.Apply(ctx, space, in)
	if err != nil {
		return ApplyResponse{}, httpapi.Normalize(err)
	}
	return httpapi.Copy(out)
}

func (c inProc) GetSettings(ctx context.Context, t Tenant, space, key string) (SettingsResult, error) {
	if !validTenant(t) || !auth.ValidID(space) {
		return SettingsResult{}, httpapi.Errorf(ErrInvalid, "invalid settings request")
	}
	out, err := c.server.GetSettings(ctx, t, space, key)
	if err != nil {
		return SettingsResult{}, httpapi.Normalize(err)
	}
	return httpapi.Copy(out)
}

func (c inProc) EditSettings(ctx context.Context, space string, in SettingsEditRequest) (SettingsEditResult, error) {
	if !auth.ValidID(space) || !validTenant(Tenant{in.Org, in.Owner}) || in.Key == "" || in.IdempotencyKey == "" ||
		in.BaseRevision == "" || !auth.ValidDigest(in.BaseRevision) {
		return SettingsEditResult{}, httpapi.Errorf(ErrInvalid, "invalid settings edit request")
	}
	out, err := c.server.EditSettings(ctx, space, in)
	if err != nil {
		return SettingsEditResult{}, httpapi.Normalize(err)
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

func (c httpClient) Apply(ctx context.Context, space string, in ApplyRequest) (ApplyResponse, error) {
	if !validApply(space, in) {
		return ApplyResponse{}, httpapi.Errorf(ErrInvalid, "invalid apply request")
	}
	var out ApplyResponse
	err := c.Do(ctx, http.MethodPost, "/v1/spaces/"+url.PathEscape(space)+"/apply", in, &out)
	return out, err
}

func (c httpClient) GetSettings(ctx context.Context, t Tenant, space, key string) (SettingsResult, error) {
	if !validTenant(t) || !auth.ValidID(space) {
		return SettingsResult{}, httpapi.Errorf(ErrInvalid, "invalid settings request")
	}
	var out SettingsResult
	query := tenantQuery(t)
	if key != "" {
		query += "&key=" + url.QueryEscape(key)
	}
	err := c.Do(ctx, http.MethodGet, "/v1/spaces/"+url.PathEscape(space)+"/settings?"+query, nil, &out)
	return out, err
}

func (c httpClient) EditSettings(ctx context.Context, space string, in SettingsEditRequest) (SettingsEditResult, error) {
	if !auth.ValidID(space) || !validTenant(Tenant{in.Org, in.Owner}) || in.Key == "" || in.IdempotencyKey == "" ||
		in.BaseRevision == "" || !auth.ValidDigest(in.BaseRevision) {
		return SettingsEditResult{}, httpapi.Errorf(ErrInvalid, "invalid settings edit request")
	}
	var out SettingsEditResult
	err := c.Do(ctx, http.MethodPost, "/v1/spaces/"+url.PathEscape(space)+"/settings", in, &out)
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

// Handler serves the spawn routes behind the shared bearer check; the
// policy route's success response is immutable with ETag = digest.
func Handler(server Client, token string) http.Handler {
	inner := httpapi.Handle(token, func(r *http.Request) (any, error) {
		client := NewInProc(server)
		if strings.HasPrefix(r.URL.Path, "/v1/workers/") {
			return handleWorker(client, r)
		}
		if strings.HasPrefix(r.URL.Path, "/v1/leases/") {
			return handleLease(client, r)
		}
		if r.URL.Path == "/v1/runtimes" {
			if r.Method != http.MethodGet {
				return nil, ErrNotFound
			}
			t, err := queryTenant(r)
			if err != nil {
				return nil, err
			}
			return client.ListRuntimes(r.Context(), t)
		}
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
		case r.Method == http.MethodPost && len(rest) == 1 && rest[0] == "workers":
			var in ClaimWorkerRequest
			if err := httpapi.Decode(r, &in); err != nil {
				return nil, err
			}
			return client.ClaimWorker(r.Context(), space, in)
		case r.Method == http.MethodPost && len(rest) == 1 && rest[0] == "runtimes":
			var in ClaimRuntimeRequest
			if err := httpapi.Decode(r, &in); err != nil {
				return nil, err
			}
			return client.ClaimRuntime(r.Context(), space, in)
		case r.Method == http.MethodPost && len(rest) == 2 && rest[0] == "runtimes" && rest[1] == "drain":
			var in DrainRuntimesRequest
			if err := httpapi.Decode(r, &in); err != nil {
				return nil, err
			}
			n, err := client.DrainRuntimes(r.Context(), space, in)
			if err != nil {
				return nil, err
			}
			return map[string]int{"drained": n}, nil
		case r.Method == http.MethodPost && len(rest) == 2 && rest[0] == "workers" && rest[1] == "drain":
			var in DrainWorkersRequest
			if err := httpapi.Decode(r, &in); err != nil {
				return nil, err
			}
			n, err := client.DrainWorkers(r.Context(), space, in)
			if err != nil {
				return nil, err
			}
			return map[string]int{"drained": n}, nil
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
		case r.Method == http.MethodPost && len(rest) == 1 && rest[0] == "apply":
			var in ApplyRequest
			if err := httpapi.Decode(r, &in); err != nil {
				return nil, err
			}
			return client.Apply(r.Context(), space, in)
		case r.Method == http.MethodGet && len(rest) == 1 && rest[0] == "settings":
			t, err := queryTenant(r)
			if err != nil {
				return nil, err
			}
			return client.GetSettings(r.Context(), t, space, r.URL.Query().Get("key"))
		case r.Method == http.MethodPost && len(rest) == 1 && rest[0] == "settings":
			var in SettingsEditRequest
			if err := httpapi.Decode(r, &in); err != nil {
				return nil, err
			}
			return client.EditSettings(r.Context(), space, in)
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
