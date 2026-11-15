# CHG-0291: Separate shared and infra-owner user Compose projects

## Problem

The published stack passport distinguishes `shared` from `per-space`, but
the infra owner's renderer placed both in one Compose project. A failed
dedicated CLIProxy init also stayed exited after a transient Broker error.

## Scope

- Render shared and infra-owner user plans from the published `stack.yaml`.
- Keep the existing shared project and named Broker volumes; start the
  infra owner's user services in a separate project.
- Route `hubctl up/down`, `dev-rebuild`, and `space apply` to the right plan.
- Retry the same CLIProxy init container after transient Broker failure.
- Verify artem_main and artem_two service/project/volume placement live.

## Acceptance

- Exactly one shared control-plane project, one user project per owner.
- `artem_two`'s dedicated CLIProxy remains healthy and reachable.
- An `up` or `space apply` does not merge the two plans again.
- No personal or Broker volume is deleted or replaced.
