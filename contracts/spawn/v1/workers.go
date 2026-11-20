package spawn

import (
	"context"
	"net/http"
	"net/url"
	"strings"

	"github.com/letya999/hermes-hub/shared/auth"
	"github.com/letya999/hermes-hub/shared/httpapi"
)

// Worker is one disposable cell row (decision 12): keyed by tenant, space,
// cell key and image digest, with no warm state — a claimed worker is busy
// and a released one drains.
type Worker struct {
	Org         string `json:"org"`
	Owner       string `json:"owner"`
	ID          string `json:"id"`
	Space       string `json:"space"`
	Agent       string `json:"agent"`
	Generation  int64  `json:"generation"`
	State       string `json:"state"`
	CellKey     string `json:"cell_key"`
	ImageDigest string `json:"image_digest"`
}

// WorkerWorkspace is the workspace bind a claim asks for: a scope plus the
// scope-shaped path/access the service resolves into a host bind.
type WorkerWorkspace struct {
	Scope  string `json:"scope"`
	Path   string `json:"path,omitempty"`
	Access string `json:"access,omitempty"`
}

// WorkerCred is one brokered credential the cred sidecar injects toward
// the declared host. Value travels only inside this request and the
// sidecar's exec file channel — never argv, logs or persistence.
type WorkerCred struct {
	EnvName string `json:"env_name"`
	Host    string `json:"host"`
	Prefix  string `json:"prefix,omitempty"`
	Value   string `json:"value"`
}

// WorkerLimits is the compute envelope the admitted plan earned.
type WorkerLimits struct {
	CPUMillis      int   `json:"cpu_millis"`
	MemoryMiB      int   `json:"memory_mib"`
	MaxPIDs        int   `json:"max_pids"`
	TimeoutSeconds int   `json:"timeout_seconds"`
	OutputBytes    int64 `json:"output_bytes"`
}

// ClaimWorkerRequest is the POST /v1/spaces/{space}/workers body: the
// reuse key the caller computed, the resolved image and the full cell
// shape — every isolation input the service must declare on the spec.
type ClaimWorkerRequest struct {
	Org             string           `json:"org"`
	Owner           string           `json:"owner"`
	CellKey         string           `json:"cell_key"`
	Image           string           `json:"image"`
	Agent           string           `json:"agent,omitempty"`
	Principal       string           `json:"principal"`
	BindingID       string           `json:"binding_id,omitempty"`
	JobID           string           `json:"job_id,omitempty"`
	Workspace       *WorkerWorkspace `json:"workspace,omitempty"`
	Skills          bool             `json:"skills,omitempty"`
	Toolset         []string         `json:"toolset,omitempty"`
	Egress          []string         `json:"egress,omitempty"`
	Brokered        []WorkerCred     `json:"brokered,omitempty"`
	InstallPackages bool             `json:"install_packages,omitempty"`
	Limits          WorkerLimits     `json:"limits"`
}

// ClaimedWorker is the claim's answer: the ready busy worker, the lease
// the caller renews and releases, and the sidecar endpoints the exec
// environment needs (empty when the cell has none).
type ClaimedWorker struct {
	Worker Worker `json:"worker"`
	Lease  string `json:"lease"`
	Proxy  string `json:"proxy,omitempty"`
	Cred   string `json:"cred,omitempty"`
}

// WorkerLeaseRequest is the renew/release body: the tenant and the lease
// id the claim returned.
type WorkerLeaseRequest struct {
	Org   string `json:"org"`
	Owner string `json:"owner"`
	Lease string `json:"lease"`
}

// DrainWorkersRequest selects live workers of a space by claim identity;
// empty fields match everything (POST /v1/spaces/{space}/workers/drain).
type DrainWorkersRequest struct {
	Org       string `json:"org"`
	Owner     string `json:"owner"`
	BindingID string `json:"binding_id,omitempty"`
	JobID     string `json:"job_id,omitempty"`
}

// WorkerExecRequest is the POST /v1/workers/{id}/exec body: argv runs
// without a shell, env values travel to the workload's own environment
// (never argv), and Dir/Stdin/Timeout/MaxOutput bound the call.
type WorkerExecRequest struct {
	Org       string            `json:"org"`
	Owner     string            `json:"owner"`
	Lease     string            `json:"lease"`
	Argv      []string          `json:"argv"`
	Env       map[string]string `json:"env,omitempty"`
	Dir       string            `json:"dir,omitempty"`
	Stdin     []byte            `json:"stdin,omitempty"`
	TimeoutMs int64             `json:"timeout_ms,omitempty"`
	MaxOutput int64             `json:"max_output,omitempty"`
}

// WorkerExecResult reports one exec's outcome: exit code plus the
// separately bounded output streams.
type WorkerExecResult struct {
	ExitCode        int    `json:"exit_code"`
	Stdout          []byte `json:"stdout,omitempty"`
	Stderr          []byte `json:"stderr,omitempty"`
	StdoutTruncated bool   `json:"stdout_truncated,omitempty"`
	StderrTruncated bool   `json:"stderr_truncated,omitempty"`
}

func validWorkerID(id string) bool { return strings.HasPrefix(id, "wk-") && len(id) == 19 }

func validClaim(space string, in ClaimWorkerRequest) bool {
	if !auth.ValidID(space) || !validTenant(Tenant{in.Org, in.Owner}) {
		return false
	}
	if in.CellKey == "" || len(in.CellKey) > 256 || in.Image == "" || len(in.Image) > 256 || in.Principal == "" {
		return false
	}
	return true
}

func validLeaseReq(id string, in WorkerLeaseRequest) bool {
	return validWorkerID(id) && validTenant(Tenant{in.Org, in.Owner}) && in.Lease != ""
}

func (c inProc) ClaimWorker(ctx context.Context, space string, in ClaimWorkerRequest) (ClaimedWorker, error) {
	if !validClaim(space, in) {
		return ClaimedWorker{}, httpapi.Errorf(ErrInvalid, "invalid worker claim")
	}
	out, err := c.server.ClaimWorker(ctx, space, in)
	if err != nil {
		return ClaimedWorker{}, httpapi.Normalize(err)
	}
	return httpapi.Copy(out)
}

func (c inProc) RenewWorker(ctx context.Context, id string, in WorkerLeaseRequest) (Worker, error) {
	if !validLeaseReq(id, in) {
		return Worker{}, httpapi.Errorf(ErrInvalid, "invalid worker renew")
	}
	out, err := c.server.RenewWorker(ctx, id, in)
	if err != nil {
		return Worker{}, httpapi.Normalize(err)
	}
	return httpapi.Copy(out)
}

func (c inProc) ReleaseWorker(ctx context.Context, id string, in WorkerLeaseRequest) error {
	if !validLeaseReq(id, in) {
		return httpapi.Errorf(ErrInvalid, "invalid worker release")
	}
	return httpapi.Normalize(c.server.ReleaseWorker(ctx, id, in))
}

func (c inProc) DrainWorkers(ctx context.Context, space string, in DrainWorkersRequest) (int, error) {
	if !auth.ValidID(space) || !validTenant(Tenant{in.Org, in.Owner}) {
		return 0, httpapi.Errorf(ErrInvalid, "invalid worker drain")
	}
	out, err := c.server.DrainWorkers(ctx, space, in)
	if err != nil {
		return 0, httpapi.Normalize(err)
	}
	return out, nil
}

func (c inProc) ExecWorker(ctx context.Context, id string, in WorkerExecRequest) (WorkerExecResult, error) {
	if !validLeaseReq(id, WorkerLeaseRequest{Org: in.Org, Owner: in.Owner, Lease: in.Lease}) || len(in.Argv) == 0 || len(in.Argv) > 256 {
		return WorkerExecResult{}, httpapi.Errorf(ErrInvalid, "invalid worker exec")
	}
	out, err := c.server.ExecWorker(ctx, id, in)
	if err != nil {
		return WorkerExecResult{}, httpapi.Normalize(err)
	}
	return httpapi.Copy(out)
}

func (c httpClient) ClaimWorker(ctx context.Context, space string, in ClaimWorkerRequest) (ClaimedWorker, error) {
	if !validClaim(space, in) {
		return ClaimedWorker{}, httpapi.Errorf(ErrInvalid, "invalid worker claim")
	}
	var out ClaimedWorker
	err := c.Do(ctx, http.MethodPost, "/v1/spaces/"+url.PathEscape(space)+"/workers", in, &out)
	return out, err
}

func (c httpClient) RenewWorker(ctx context.Context, id string, in WorkerLeaseRequest) (Worker, error) {
	if !validLeaseReq(id, in) {
		return Worker{}, httpapi.Errorf(ErrInvalid, "invalid worker renew")
	}
	var out Worker
	err := c.Do(ctx, http.MethodPost, "/v1/workers/"+url.PathEscape(id)+"/renew", in, &out)
	return out, err
}

func (c httpClient) ReleaseWorker(ctx context.Context, id string, in WorkerLeaseRequest) error {
	if !validLeaseReq(id, in) {
		return httpapi.Errorf(ErrInvalid, "invalid worker release")
	}
	return c.Do(ctx, http.MethodPost, "/v1/workers/"+url.PathEscape(id)+"/release", in, nil)
}

func (c httpClient) DrainWorkers(ctx context.Context, space string, in DrainWorkersRequest) (int, error) {
	if !auth.ValidID(space) || !validTenant(Tenant{in.Org, in.Owner}) {
		return 0, httpapi.Errorf(ErrInvalid, "invalid worker drain")
	}
	var out struct {
		Drained int `json:"drained"`
	}
	err := c.Do(ctx, http.MethodPost, "/v1/spaces/"+url.PathEscape(space)+"/workers/drain", in, &out)
	return out.Drained, err
}

func (c httpClient) ExecWorker(ctx context.Context, id string, in WorkerExecRequest) (WorkerExecResult, error) {
	if !validLeaseReq(id, WorkerLeaseRequest{Org: in.Org, Owner: in.Owner, Lease: in.Lease}) || len(in.Argv) == 0 || len(in.Argv) > 256 {
		return WorkerExecResult{}, httpapi.Errorf(ErrInvalid, "invalid worker exec")
	}
	var out WorkerExecResult
	err := c.Do(ctx, http.MethodPost, "/v1/workers/"+url.PathEscape(id)+"/exec", in, &out)
	return out, err
}

// handleWorker routes the /v1/workers/{id}/{verb} surface — exec, lease
// renew and release — called from Handler when the path prefix matches.
func handleWorker(client Client, r *http.Request) (any, error) {
	rest := strings.Split(strings.TrimPrefix(r.URL.Path, "/v1/workers/"), "/")
	if r.Method != http.MethodPost || len(rest) != 2 || rest[0] == "" {
		return nil, ErrNotFound
	}
	id, verb := rest[0], rest[1]
	switch verb {
	case "exec":
		var in WorkerExecRequest
		if err := httpapi.Decode(r, &in); err != nil {
			return nil, err
		}
		return client.ExecWorker(r.Context(), id, in)
	case "renew":
		var in WorkerLeaseRequest
		if err := httpapi.Decode(r, &in); err != nil {
			return nil, err
		}
		return client.RenewWorker(r.Context(), id, in)
	case "release":
		var in WorkerLeaseRequest
		if err := httpapi.Decode(r, &in); err != nil {
			return nil, err
		}
		return nil, client.ReleaseWorker(r.Context(), id, in)
	default:
		return nil, ErrNotFound
	}
}
