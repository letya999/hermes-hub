# CHG-0279 — M31+M20 merge live acceptance

## Goal

Prove the merged branch (`chore/merge-m31-m20-into-dev` = dev + m31
consolidated tool surface + m20 memory vault) works live end-to-end:
real Docker images, a managed acceptance space on the shared authed
cliproxy, real model turns exercising the catalog r10 tool surface.

## Acceptance surface (as executed)

1. Dev images rebuilt from the merged branch through the repo build path
   (`hubctl build`, `hubctl dev-rebuild`, `hubctl up`; never raw compose).
2. Managed space `spaces/m29-acc-03` (already provisioned) migrated to
   v2 passports; wishes extended to context/files/memory/documents/
   routines/watches/sessions/settings/skills/models/web; broker-grant
   materialization for cliproxy-m29-acc-03 minted through the broker.
3. Live assertions — all verified:
   - catalog r10 (`hub-agent-tools@10.0.0`, 92 tools/23 families)
     registered; policy rev 4 ceiling (212 rules); profile rev 8
     (45 selections, 66 allows, 13 core_tools, rights rows) published
     with operator confirmation;
   - MCP `tools/list` through toolhub-relay = 13 core + platform +
     `tool_search`/`tool_call`; extended tools callable via `tool_call`;
   - real model turn through `/v1/execute` → supervisor → managed
     runtime → model-relay → cliproxy → `gemini-3-flash` answered
     "pong"; a second turn had the model invoke `memory_card` and
     report the honest run-scope denial verbatim;
   - `file_write`/`file_read` round-trip through the docker-exec agent
     executor; `project_create` created `projects/acc-proj-1`;
   - `settings_read`, `context_status`, `skill_search`,
     `routine_list`, `watch_list` executed (routines/watch after
     enrolling m29-acc-03 in the shared comm-hub users file);
   - `memory_search` executed the real BM25 index path
     (degraded-then-ok); `memory_card` correctly denied without a
     stamped private run (SPEC-0085 boundary — writes require a
     channel turn);
   - denials verified: `../` escape rejected, absolute paths rejected,
     non-selected tools not found, `runtime:*` families fail closed
     with honest "unavailable", relay auth negatives (bad/missing
     bearer, admin-only model switch) all enforced;
   - uncertain op `tool-bf80e852986f966a…` reconciled through the audit
     ledger (backend-error on a read-only op; retry succeeded).
4. Merge-gap fixes found only by running it: `hub:memory` governance
   section in the ToolHub inventory; MountPlan `/vault` bind under
   managed+memory_vault (spawn path had no mount source);
   `tools.select` → invoke admission for tool_call; `egress-relay`
   stack service declaration for managed egress; open-knowledge image
   git, `/state/home` tmpfs, host allowlist; broker grant minting +
   space-unique cliproxy workload name.
5. Docker gates on the built images — partial: dev/control/space
   images built and healthy; heavy image-budget canaries remain CI.

## Risks

- Windows AV fork/exec flakes during builds/tests — rerun with
  `GOFLAGS=-p=4`.
- IPAM pool exhaustion blocked two managed canaries on prior sessions —
  report identically if it recurs.
- acc-01's dedicated cliproxy has no auths → used `cliproxy: shared`
  against the infra instance (antigravity account already enrolled).
- acc-03 has no channel features → channel-gateway capture/stamp paths
  verified through the code path + m20 wiring tests; the run-scope
  write boundary verified live via its honest denial.
