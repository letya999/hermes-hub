# CHG-0280 — M20 memory vault live acceptance

## Goal

Prove the M20 memory vault works end-to-end live on the merged branch —
not just that tools exist, but that a real private model turn writes
memory, capture lands in daily leaves, search returns evidence, the
nightly rollup runs, and OpenKnowledge is genuinely opened on the real
vault.

## Topology finding that shapes the acceptance

Run-scope stamps and capture are written by the communication gateway
into `g.config.VaultDir` — a single owner vault, only rendered when the
infra space enables `memory_vault` + a channel feature. Sibling managed
spaces (m29-acc-03) routed through the shared gateway can never receive
a stamp in their own vault — `memory_card` there correctly fails closed
forever. The designed owner is `spaces/local`.

## Acceptance surface

1. Enable `memory_vault` + `knowledge_ui` on `spaces/local`
   (workspace.yaml wishes, agent.yaml rollup/leaf knobs, ui edge via a
   loopback `ui_port` — verify empirically whether the runtime's own
   MCP calls to `open-knowledge:8080` still pass the Host allowlist
   when OK_EXTERNAL_URL is the loopback address).
2. Fix found live: supervisor spawn mounts `/vault` for legacy (no
   workspace passport) managed runtimes when `memory_vault` is on —
   the else branch only handled passport spaces (second instance of the
   CHG-0279 MountPlan gap).
3. Merge gaps found while wiring the owner-space path (all test-driven):
   - shared-gateway vault leak: capture/stamp/CORE+recall ran for every
     enrolled principal — gated to the vault owner (`nightOwner().ID` /
     `HTTPRunner.Owner`); sibling turns, replies, run-scope stamps and
     instructions never touch the owner vault now;
   - relay env-pair scoping: `modelRelayFor` env branch shadowed every
     principal on a shared gateway — scoped to `HUB_USER_ID` so siblings
     keep their admins-file routing;
   - unmanaged owner relay seam: `HUB_MODEL_RELAY_URL/AUTH/ADMIN` now
     pass from space secrets into `communication.<env>.env` so an
     unmanaged owner can opt the rollup into a reachable relay without
     a rendered model-relay service.
4. `hubctl up --dir spaces/local --env dev`: comm-hub recreated with
   `/vault` rw + HUB_VAULT + rollup env; open-knowledge service built
   (dev-knowledge with the git fix) and healthy.
5. Real private turn: `/v1/mock/message` as `local` (synthetic telegram
   id for control auth) → gateway stamps run-scope → supervisor →
   managed runtime → cliproxy → real model → in-turn `memory_card
   remember` writes a card through the governed writer (git commit).
6. Assert: card file + git log in spaces/local/vault; daily leaf gained
   turn+reply entries; run-scope cleared after run; writes.jsonl audit.
7. `memory_search` finds the card + leaf lines with evidence refs.
8. Nightly rollup: `memory_rollup_time` set near-term; a seeded D-1
   leaf via the governed writer rolls up through the real model seam
   (HUB_MODEL_RELAY_URL on the env file → acc-03 model-relay →
   cliproxy), day summary written, weekly/monthly/yearly folds
   attempted, prune lanes report (expired/folded/kept), digest delivery
   attempted to the owner's id.
9. OpenKnowledge really opened: loopback publish + real HTTP/MCP calls
   — verify the real vault content is visible and a UI edit lands as a
   vault commit.
10. `memory_provider`: verify the seam's honest behavior unconfigured
    (opt-in by design — report, no fake backend).
11. Honest negative evidence: sibling space (m29-acc-03) memory_card
    stays denied — the architectural boundary, not a bug.

## Risks

- `hubctl up` on spaces/local restarts the dev control plane — the
  supported path; compose diff limits churn to changed services.
- Night rollup's model seam reads HUB_MODEL_RELAY_URL — unmigrated
  spaces don't render it; operator env file carries it to cliproxy.
- Digest delivery targets the synthetic owner id — no real external send.
