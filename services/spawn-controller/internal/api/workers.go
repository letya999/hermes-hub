package api

import (
	"context"
	"time"

	spawnv1 "github.com/letya999/hermes-hub/contracts/spawn/v1"
	"github.com/letya999/hermes-hub/services/spawn-controller/internal/backend"
	"github.com/letya999/hermes-hub/services/spawn-controller/internal/spawn"
)

// workerOf maps the durable row onto the contract DTO.
func workerOf(w spawn.Worker) spawnv1.Worker {
	return spawnv1.Worker{
		Org: "", Owner: "", ID: w.ID, Space: w.Space, Agent: w.Agent,
		Generation: w.Generation, State: w.State, CellKey: w.CellKey, ImageDigest: w.ImageDigest,
	}
}

// claimOf maps the contract claim onto the service's WorkerClaim: the
// reuse key, the pinned image and the full cell shape. Brokered values
// cross here only — the service strips them before persistence.
func claimOf(in spawnv1.ClaimWorkerRequest) spawn.WorkerClaim {
	out := spawn.WorkerClaim{
		CellKey:         in.CellKey,
		Image:           in.Image,
		Agent:           in.Agent,
		Principal:       in.Principal,
		BindingID:       in.BindingID,
		JobID:           in.JobID,
		Skills:          in.Skills,
		Toolset:         in.Toolset,
		Egress:          in.Egress,
		InstallPackages: in.InstallPackages,
		Limits: spawn.CellLimits{
			CPUMillis:      in.Limits.CPUMillis,
			MemoryMiB:      in.Limits.MemoryMiB,
			MaxPIDs:        in.Limits.MaxPIDs,
			TimeoutSeconds: in.Limits.TimeoutSeconds,
			OutputBytes:    in.Limits.OutputBytes,
		},
	}
	if in.Workspace != nil {
		out.Workspace = spawn.CellWorkspace{
			Scope:  spawn.WorkspaceScope(in.Workspace.Scope),
			Path:   in.Workspace.Path,
			Access: in.Workspace.Access,
		}
	}
	for _, c := range in.Brokered {
		out.Brokered = append(out.Brokered, spawn.CellCred{
			EnvName: c.EnvName, Host: c.Host, Prefix: c.Prefix, Value: c.Value,
		})
	}
	return out
}

// ClaimWorker resolves one claim to a ready busy worker plus its lease.
func (c Contract) ClaimWorker(ctx context.Context, space string, in spawnv1.ClaimWorkerRequest) (spawnv1.ClaimedWorker, error) {
	lease, err := c.Workers.Claim(ctx, tenantOf(in.Org, in.Owner), space, claimOf(in))
	if err != nil {
		return spawnv1.ClaimedWorker{}, err
	}
	out := spawnv1.ClaimedWorker{
		Worker: workerOf(lease.Worker), Lease: lease.Lease,
		Proxy: lease.Proxy, Cred: lease.CredHost,
	}
	out.Worker.Org, out.Worker.Owner = in.Org, in.Owner
	return out, nil
}

// RenewWorker re-arms a held worker's busy bound.
func (c Contract) RenewWorker(ctx context.Context, id string, in spawnv1.WorkerLeaseRequest) (spawnv1.Worker, error) {
	w, err := c.Workers.Renew(ctx, tenantOf(in.Org, in.Owner), id, in.Lease)
	out := workerOf(w)
	out.Org, out.Owner = in.Org, in.Owner
	return out, err
}

// ReleaseWorker drops the lease; the last hold drains the worker.
func (c Contract) ReleaseWorker(ctx context.Context, id string, in spawnv1.WorkerLeaseRequest) error {
	return c.Workers.Release(ctx, tenantOf(in.Org, in.Owner), id, in.Lease)
}

// DrainWorkers drains a space's live workers, optionally scoped to one
// binding or job.
func (c Contract) DrainWorkers(ctx context.Context, space string, in spawnv1.DrainWorkersRequest) (int, error) {
	return c.Workers.Drain(ctx, tenantOf(in.Org, in.Owner), space, in.BindingID, in.JobID)
}

// ExecWorker runs one bounded command inside a busy leased worker.
func (c Contract) ExecWorker(ctx context.Context, id string, in spawnv1.WorkerExecRequest) (spawnv1.WorkerExecResult, error) {
	env := make([]string, 0, len(in.Env))
	for k, v := range in.Env {
		env = append(env, k+"="+v)
	}
	res, err := c.Workers.Exec(ctx, tenantOf(in.Org, in.Owner), id, in.Lease, backend.ExecSpec{
		Argv:      in.Argv,
		Env:       env,
		Dir:       in.Dir,
		Stdin:     in.Stdin,
		Timeout:   time.Duration(in.TimeoutMs) * time.Millisecond,
		MaxOutput: in.MaxOutput,
	})
	if err != nil {
		return spawnv1.WorkerExecResult{}, err
	}
	return spawnv1.WorkerExecResult{
		ExitCode: res.ExitCode, Stdout: res.Stdout, Stderr: res.Stderr,
		StdoutTruncated: res.StdoutTruncated, StderrTruncated: res.StderrTruncated,
	}, nil
}
