---
description: Project philosophy — why the repo is shaped this way; the rules every other document assumes.
last_verified: 2026-10-20
---

# Principles

## The product

A personal AI assistant for one owner (with organizations as narrowing
contexts, not a SaaS). Telegram is the front door; Hermes is the brain;
Go is the trust boundary around it. The system is an operator-deployed
appliance — not a platform, not a public service.

## Rules that everything else assumes

1. **The model is untrusted.** Content it reads or writes — web pages,
   messages, tool output, transcripts — is data, never authority.
   Authorization comes from operator-owned policy and human-confirmed
   records only; a `Confirmation` digest binds a human to exact bytes.
2. **Default deny, explicit grants.** A capability exists only because a
   reviewed definition + grant selected it. Absence of evidence is a deny,
   not a warning. Deny always wins over allow.
3. **Credentials never live in the model plane.** Values terminate at the
   credential broker or encrypted store; everything else holds opaque
   locators. Human entry is a one-time `?t=` link or a protected local
   form — never chat, never argv.
4. **Pinned, reviewed, immutable.** Upstream code is pinned by digest/SHA
   (Hermes, connectors, base images); review records what was admitted and
   changes only through a new review. Mutable tags and "latest" are bugs.
5. **Opt-in or absent.** Integrations ship disabled. A feature that is off
   does not exist at runtime — it is not merely hidden.
6. **Durable before claimed.** State transitions persist before they are
   reported; an uncertain outcome is recorded as uncertain, never retried
   blindly and never reported as success. Tombstones outlive deletion —
   replay must be idempotent.
7. **Evidence, not mocks.** A fixture proves code behavior only; live
   claims need real accounts and recorded receipts. The standing
   "unproven" list in docs/validation.md is load-bearing.
8. **One docs tree, one voice.** docs/ is the current system — facts, not
   plans and not history. Decisions go to adr/, frozen requirements to
   specs/, work-in-flight and evidence to .work/. A document ≤500 lines;
   if it exceeds that it is describing two things — split it.
9. **Operator control, reversible actions.** Deployments stay local and
   reversible; destructive steps are two-phase with explicit confirmation.
   The owner authorizes implementation; login, external sends and
   publication each require their own instruction.
10. **Small control plane, cold compute.** Always-on services are tiny;
    agent runtimes scale to zero and respawn with full durable identity.

## What that buys

Agents (human and LLM) can answer "what does the system do" from docs/,
"why was it decided" from adr/, "what must it do" from specs/, and "what
is happening right now" from .work/ — without one document contradicting
another or hiding 1400 lines of mixed concerns under one heading.
