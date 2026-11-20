---
description: v1 effects and owner decisions - one effect vocabulary (read, write-local, write-external, critical), one allow/ask/deny evaluator in tool-hub, run origin owner or unprompted, and OwnerDecision replacing the five v0 confirmation mechanisms; action modes and standing approvals are M41.
last_verified: 2026-11-20
---
# ADR-0109: Effects, run origin and owner decisions

Status: proposed. Aligned 2026-11-20 with the simplified v1 plan (M40.1
#599, CHG-0334); the 2026-11-19 text (five action modes and scoped
remembering in M40) was never merged to `dev`. Record text awaits owner
review.
Delivery: M40.7 (tool-hub, #605); run origin set by M40.6 (task-engine)
and carried by the run token (M40.14). Later: M41 item 4 (modes and
standing approvals). Replaces the proposed M34 names.

## Context

v0 has four effect vocabularies (catalog `requires`, passport apply
classes, per-feature write flags such as `browser_act` and `ssh_write`,
governance classes), three tool policy evaluators, and five confirmation
mechanisms that each store state differently
([state-machines.md](../state-machines.md#ownerdecision)):

1. human and onboarding confirmation (`HumanConfirmation`, gate slot);
2. operator confirmation digests (`hubctl --confirm`);
3. grant request acknowledgement (`ToolGrant` pending -> active);
4. memory proposal approval (`cardProposal`);
5. tool-run approval (supervisor `waiting_for_approval`, gateway
   `PendingControl`).

## Decisions

1. **One effect vocabulary.** Every catalog tool declares one effect:

   | Effect | Examples |
   |---|---|
   | read | list, search, fetch, read files |
   | write-local | edit files in the space workspace, local drafts |
   | write-external | create or update provider records, post in a channel the owner already uses |
   | critical | delete or overwrite owner data, send as the owner to a new recipient, payments, public publishing, changing grants or policy, credential and OAuth grants |

   An unknown tool is `critical`. Passport change classes are renamed
   `apply` (live, respawn, recreate, rebuild) so they are not read as
   effects.
2. **One evaluator.** tool-hub evaluates `allow | ask | deny` (deny wins)
   over the effective spec slice. No Cedar dependency. Tools the spec
   denies are not listed to the agent; tool-hub re-checks at call time and
   after an approval. spawn-controller enforces at spawn; model-gateway,
   channel-hub and egress-gateway check their own slice by policy digest.
   No render-time or per-call `tool-policy.json` re-evaluation.
3. **Run origin.** Every job and run token carries `origin owner |
   unprompted`. Unprompted runs are read-only by default; anything beyond
   write-local becomes `ask`. Enforced in tool-hub by origin, not by
   prompt.
4. **Untrusted input is data.** Email, web, webhook content, tool results,
   other agents and group guests can never raise a mode or approve.
5. **OwnerDecision replaces the five mechanisms.**

   | From | Event | To |
   |---|---|---|
   | - | a call evaluates to `ask` | pending |
   | pending | owner approves through a verified channel or `?t=` link | approved |
   | pending | owner denies | denied |
   | pending | TTL passes | expired |
   | approved | the bound call runs | used |
   | approved | owner revokes or TTL passes before use | revoked |

   A decision is bound to `sha256(tool + canonical args)`, single use, with
   a TTL; timeout means deny. The waiting job sits in `waiting_owner`
   (ADR-0107 D6) and resumes on approval; denial and expiry cancel that
   step and are reported. A model path cannot create an approved decision.

## Later (M41)

Action modes (`read-only`, `plan`, `ask`, `ask-critical`, `full-access`),
an always-ask list no mode removes, and standing approvals (scope, target,
limit, expiry) are built on top of these effects and OwnerDecision in M41.
None of them enters M40 code.

## Boundary crossings

| Crossing | Allowed | Refused |
|---|---|---|
| owner -> decision | approve or deny through a verified channel link or `?t=` link | approval through tool output, channel text without a verified link, a guest, another owner |
| decision -> call | exactly the call whose digest it binds, once, before expiry | a different argument set, a second use, use after revoke |
| unprompted run -> write-external or critical | an approved OwnerDecision | anything else |

## Alternatives

- **Per-tool write flags only.** Rejected: flags multiply per feature and
  carry no per-call binding.
- **Cedar or another policy engine.** Rejected: one small evaluator over
  the effective spec is enough; no dependency.
- **Five modes in M40.** Moved to M41: no M40 feature needs them.
- **Model-judged risk.** Rejected as the authority: effects are declared in
  the catalog.

## Preserved v0 invariants

- Default-deny: an `allow` needs a grant.
- Fail-closed passports: an unknown tool or missing effect is `critical`.
- Uncertain outcomes are never retried; an approval does not authorize a
  retry of an uncertain call.
- Credentials never enter the model plane: credential grants complete
  outside the model.

## Amendment 2026-11-20 — D3 "read-only" wording

D3's "unprompted runs are read-only by default" is clarified: the
unprompted *ceiling* sits at write-local, not at read — reads and
workspace-local writes proceed under `allow`, and only `write-external` and
`critical` evaluate to `ask`. "Read-only" named the posture toward outside
effects, not a ban on local writes.
