---
description: v1 action modes read-only, plan, ask, ask-critical, full-access over effect classes; an always-ask list no mode removes; durable owner decisions with scoped remembering; mapping to Codex, Claude Code, Gemini CLI and the M34 names.
last_verified: 2026-11-19
---
# ADR-0109: Action modes and owner decisions

Status: accepted direction (M40, owner issue #572). Date: 2026-11-19.
Delivery: CHG-0348 (modes and decisions); used by ADR-0106 run tokens
(`mode` claim) and ADR-0108 (Assignments carry a mode). Replaces the
proposed M34 names (milestone "Post-MVP M34", issues #465-#470).

## Context

v0 has grants (may this principal call this tool at all) and per-feature
write flags (`browser_act`, `google_write`, `ssh_write`), plus one-off
confirmation flows (`Confirmation` digests, `?t=` links). There is no
per-run statement of how much the agent may do without asking, and no
durable record of an owner's answer that later work can reuse. M34 proposed
`plan-only`, `ask`, `ask-unsafe`, `auto-safe` and `YOLO`; coding agents
converged on similar but not identical vocabularies (CHG-0328
`research.md`).

## Decisions

1. **Modes are a second filter under grants.** Grants decide whether a call
   is possible; the mode decides whether it runs now, asks first or becomes
   a plan step. No mode bypasses grants, sandbox boundaries, egress policy or
   credential policy.
2. **Effect classes.** Every tool member declares one effect class in the
   catalog (r5 `requires` gains an `effect`):

   | Class | Examples |
   |---|---|
   | read | list, search, fetch, read files |
   | write-local | edit files in the space workspace, local drafts |
   | write-external | create/update provider records, post in a channel the owner already uses |
   | critical | delete or overwrite owner data, send as the owner to a new recipient, payments and purchases, publish publicly, change grants/modes/policy, credential and OAuth grants |

3. **Five modes.**

   | Mode | read | write-local | write-external | critical |
   |---|---|---|---|---|
   | read-only | run | refuse | refuse | refuse |
   | plan | run | record as plan step | record as plan step | record as plan step |
   | ask | run | ask | ask | ask |
   | ask-critical | run | run | run | ask |
   | full-access | run | run | run | run, except the always-ask list |

   Default for interactive owner sessions: `ask-critical`. Default for
   Assignments and Routines: `ask-critical`, with asks parking the job in
   `waiting_owner` (ADR-0107 D6). An agent profile may set a stricter
   default; a run never gets a mode wider than its profile's.
4. **Always-ask list.** Regardless of mode: changing grants, modes or policy;
   granting credentials or completing OAuth; payments and purchases;
   deleting owner data outside the workspace; sending as the owner to a
   recipient never contacted before. This list is platform-layer policy and
   only an operator can extend it; nobody can shorten it per user.
5. **Owner decisions are durable records.**

   | From | Event | To |
   |---|---|---|
   | - | call needs approval | pending |
   | pending | owner approves | approved |
   | pending | owner denies | denied |
   | pending | TTL passes | expired |
   | pending | the job is cancelled or the bundle changes | superseded |
   | approved | scope or TTL passes, owner revokes | revoked |

   A decision binds the exact call digest (tool, arguments, target, policy
   digest) like `Confirmation` digests today; a model path cannot mint or
   approve one. Approval scopes: `once`, `this run`, `this Assignment`, or
   `remember` (tool + resource pattern + expiry, at most 30 days, revocable,
   never for the always-ask list). Approval resumes the original job from
   `waiting_owner`; denial and expiry cancel that step and are reported.
6. **Questions use the same record.** An agent question to the owner is a
   decision of kind `question` with a free-text answer; it parks and resumes
   the job the same way.
7. **Mapping to other vocabularies.**

   | v1 | Codex CLI | Claude Code | Gemini CLI | M34 proposal |
   |---|---|---|---|---|
   | read-only | sandbox `read-only`, approval `never` | `dontAsk` with only read rules allowed | - | - |
   | plan | - | `plan` | `plan` | plan-only |
   | ask | sandbox `read-only`, approval `on-request` | `default` | `default` | ask |
   | ask-critical | sandbox `workspace-write`, approval `on-request` or `granular` | `acceptEdits` plus ask rules | `auto_edit` | ask-unsafe, auto-safe |
   | full-access | `danger-full-access`, approval `never` | `bypassPermissions` | `yolo` (command line only) | YOLO |

   Names verified 2026-11-19 (CHG-0328 `research.md`; Codex retired
   `untrusted`). Unlike those tools, v1 keeps the always-ask list under
   `full-access`. Unattended runs never block on a prompt: an ask parks the
   job in `waiting_owner` (the role Claude Code's `dontAsk` auto-deny plays
   for a CLI), and a Routine or Assignment can never answer an approval
   itself.

## Boundary crossings

| Crossing | Allowed | Refused |
|---|---|---|
| run -> mode | the `mode` claim of its run token | changing mode from inside a run |
| owner -> decision | approve/deny through a verified channel or `?t=` link | approval through tool output, channel text without a verified link, another owner |
| remembered approval -> call | calls matching tool + resource pattern before expiry | always-ask list, other profiles, after revoke |

## Alternatives

- **Per-tool write flags only.** Rejected: flags multiply per feature and
  carry no per-run intent.
- **Adopt one vendor's names.** Rejected: they disagree with each other and
  none keeps an always-ask list under full access.
- **Model-judged risk.** Rejected as the authority: effect classes are
  declared in the catalog; a model may only propose.

## Preserved v0 invariants

- Default-deny: modes are evaluated only after grants allow.
- Fail-closed passports: an unknown mode or missing effect class denies.
- Uncertain outcomes are never retried; an approval does not authorize a
  retry of an uncertain call.
- Credentials never enter the model plane: credential grants are always-ask
  and complete outside the model.
