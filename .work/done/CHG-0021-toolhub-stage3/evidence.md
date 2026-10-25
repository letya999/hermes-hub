---
description: Delivery evidence extracted from docs/validation.md (moved by CHG-0136).
last_verified: 2026-10-20
---

## CHG-0021 M2 ToolHub stage 3

Stage 3 adds the manifest-backed opt-in catalog, exact-owner enable/disable,
persisted projection revisions, a bounded reconnect hook, controller enforcement
attestation and explicit dry-run/apply migration. The current generated MCP runtime
remains the default when `HUB_TOOLHUB_STORE` is unset.

Control-plane wiring now matches SPEC-0017/0018/0019:

- Enable/disable persist to `HUB_TOOLHUB_STORE`; list/call/catalog reload the file
  so a tools process and `hub-toolhub` share current bindings.
- `tools/call` holds `AuthorizeProjected` through backend admission.
- Ambiguous owner connections are denied. Organization scope may disable and cannot
  enable. Bounded CLI is dispatched to the CLI runner and fails closed without isolation.
- Autostart prefers `hub-toolhub` (image symlink to `toolhub`).
- Projection changes write secret-free `toolhub-reconnect.request`. Gateway audit
  records principal/context/runtime/binding/policy/backend/outcome without secrets.
- `hubctl migrate-toolhub` uses the user as context_id, reports generated MCP under
  the space `generated/` directory, and sets `rollback_on_failure` when apply restores
  the previous store.

Local evidence:

- Targeted ToolHub, migration, agent-tools and runtime tests pass after the wiring
  patch. `just check` passed: race/shuffle, Go statement coverage 85.03%, format,
  vet, staticcheck, docs and actionlint.
- `just docker-check` passed on image `dd669637042e` (Hermes Agent v0.21.0):
  standalone smoke and pinned Hermes contract/lifecycle, including the actual
  five-minute idle shutdown and original-session cold restore. No live Telegram
  or external-provider call. The image contains `/usr/local/bin/hub-toolhub` as a
  symlink to `toolhub`.
- Container admission now requires a JSON receipt proving `running`, enforcement,
  exact workload ID, digest-pinned images and CPU/memory/PID/mount/egress policy.
  Empty 200/204 responses fail closed, so controller reachability is not reported as
  enforcement success.
- `hubctl migrate-toolhub` defaults to dry-run, imports only explicit tool lists,
  preserves disabled state, writes symbolic credential references without values and
  keeps a rollback copy on apply. Generated/discovery-only services are reported as
  unmatched instead of guessed.
- The vMCP run remains unavailable: its log records `no backends returned
  capabilities` followed by `Manager.Terminate: session terminated`; the subsequent
  404 is therefore an upstream fixture/backend-health failure. No real remote or
  stateful MCP call, gateway integration, or reconnect success is claimed.

Remaining gates are follow-up issues, not mock-closed: [#73](https://github.com/letya999/hermes-hub/issues/73)
live ToolHive/vMCP on Linux VPS, and [#74](https://github.com/letya999/hermes-hub/issues/74) real-Hermes
transport reconnect while preserving session/home/memory/run state. `just security`
is not required because this change adds no dependency or pin. GitHub #20–#26 are
closed against this control-plane evidence; #47 stays open for M3 secret storage.
