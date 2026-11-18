# CHG-0316 — Programmatic user provisioning

## Goal

New managed space onboarding becomes one command and near-instant when
templates, env secrets and shared services already exist:

```
hubctl provision --user <id> [--profile standard-user] --env dev --confirm
```

scaffold → authored passports → capability bundle (policy + profile +
rights + agent-tools binding, one confirmed batch) → `space apply` →
`up --no-build` → sibling token enrollment → verify. Idempotent and
resumable; every stage reports all preflight findings before writing.

## Why

Live bootstrap of `anpanmannn` showed ~15 hidden preconditions discovered
one failure at a time: sequential first-error `space apply`, recovery
disabling the candidate profile before it was ever live (next apply then
failed on `bound profile unavailable` before reaching the real error),
silent sibling skips during token enrollment, per-right CLI minting,
manual policy/profile JSON cloning, and `up` rebuilding unchanged images.

## Slices

1. `policies/profiles/standard-user/` — six passport templates for a
   sibling managed space (shared cliproxy + shared infra, per-space
   browser/egress-relay/model-relay/toolhub-relay, supervised runtime,
   full standard toolset/grants surface).
2. `internal/migration/provision.go` — capability bundle: policy derived
   from grants ask + compiled `hub-agent-tools` digest, bound profile,
   governance rights minted in-process, agent-tools binding pinned to the
   compiled catalog version. One `--confirm` stamps the batch.
3. `cmd/hubctl/provision.go` — the orchestration command: init (when
   passports absent), per-space port allocation, secrets preflight,
   aggregate findings, then apply/up/enroll using existing code paths.
4. Recovery fix — `restoreApplyState` no longer disables the candidate
   profile when the journal shows apply never reached `activated`
   (a never-live binding is inert; the disable only double-failed retries).
5. Enrollment warnings — skipped siblings print their reason instead of
   `continue`-ing silently.
6. Tests + operations.md rewrite of the onboarding ceremony.

## Non-goals

- Org/SCIM/group provisioning sync (SyncOrganizationMembers already
  scaffolds; provision extends it later).
- The ToolHub in-service rights writer — that is CHG-0314; provision uses
  the existing bounded-retry store writes meanwhile.
- Changing `up`'s default build behavior for interactive operators;
  provision always passes `--no-build` semantics internally.
