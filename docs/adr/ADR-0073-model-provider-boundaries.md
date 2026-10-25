---
description: Model/provider ownership, bounded failover and optional local media boundary.
last_verified: 2026-10-20
---
# ADR-0073 — Model/provider boundaries

Status: accepted for implementation; live acceptance pending.

## Context

M15 issues #131/#182/#185 require independent model selection, credential scope
and proxy placement. CHG-0139 provides routing but leaves conflicting profiles,
unbounded model requests and multi-engine Fal downloads unresolved. Issue #150
has a workspace-confined tool whose live multi-image oracle remains open.

## Decisions

1. Profiles contain configuration and opaque Broker references, never credentials.
   Owner overrides are final; disagreeing group assignments fail closed.
2. A shared provider is an explicit grant to named owners. Each relay verifies
   its own caller and resolves credentials under its own Broker identity. A shared
   CLIProxy with unproven account-to-owner routing is rejected for private accounts.
3. Fallback has a bounded ladder, per-hop and total deadlines, explicit provider
   model ids, and redacted receipts. Redirects are denied. Custom endpoints are
   operator-allowlisted and DNS-checked; trusted local endpoints are explicit.
4. Each media engine owns its endpoint and credential reference. Router wiring
   must preserve result fetching and durable engine-bound submission handles.
5. ComfyUI is optional and external to the base agent image. Hub-owned Go adapts
   its upstream HTTP contract. Workflows/models stay in a service volume, never
   the user's runtime. No optional ML dependency enters the base image.
6. Fixture evidence and live acceptance stay separate. No Docker or provider
   actions are executed by this implementation request.

## Alternatives

Patching Hermes, copying shared secret values and installing ML dependencies in
the base image would cross existing boundaries and are rejected. The existing
Go relay, Broker and media interfaces provide the required extension points.
