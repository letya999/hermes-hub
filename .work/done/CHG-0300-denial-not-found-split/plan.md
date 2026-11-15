---
description: Split declared-tool denials from not-found, audit a bounded reason class and make authoritative inventory answers terminal — incident telegram-837846090 (4m13s language ask).
---

# CHG-0300: denial-vs-not-found split + terminal inventory answers

## Incident

`telegram-837846090` (2026-11-12 21:44:20–21:48:33): the owner asked to switch
chat language to Russian. `settings_read` at 21:45:36 already returned the
complete reviewed `manageable_keys` (no language key — language is the
`/language` channel command). The model kept working ~3 more minutes: 11
successful tool calls incl. 4 file_list/5 file_read/1 file_search, one malformed
`tool_call` (missing `tool`), and three `file_list` calls on undeclared roots
(`archive`, `organization`, `knowledge`) that ToolHub logged as `record not
found`.

Root cause: a declared selection whose arguments/scope failed
`NarrowSelectionScopes`/`CapabilityDecision` collapsed into `api.ErrNotFound`
in `managedToolLocked`, so nested `tool_call` answered "not an admitted tool
name — run tool_search". `tool_search` correctly showed the tool existed, the
model retried with more roots — the denial loop. Nothing told the model that
`manageable_keys` is exhaustive or that an authoritative answer ends the search.

## Changes

- `internal/toolhub/capability_store.go`: `managedToolLocked` — a selection
  matching the name but failing admission returns wrapped `ErrUnauthorized`
  ("<name> is denied for this call — run tool_search for its admitted shape
  and missing right"); only a name absent from the profile returns
  `ErrNotFound`. Projection callers (`managedToolsLocked`,
  `previewSelectionsLocked`, restart digest, `tool_search`) call
  `managedSelectionLocked` directly or treat any error identically — listing
  and preview behavior unchanged.
- `internal/toolhub/gateway_call.go`: the call-path deny/reject audit records
  a bounded `reason` class (`not-found`, `denied`, `invalid`, `error`) from
  the sentinel error only; `reason` added to the slog field allowlist.
- `internal/audit/audit.go` + `internal/toolhub/audit_ledger.go`: `Event.Reason`
  (omitempty, ≤128 chars, control-char checked) mapped from `fields["reason"]`.
- `internal/stack/prompt_contract.go` (`contractMid`): `manageable_keys` is the
  complete reviewed key set — absent keys cannot be changed through any tool;
  chat language is `/language`; a denial is not an unadmitted name and is not
  retried with guessed values; an authoritative inventory answer ends the
  search.
- Catalog: `hub-agent-tools@18.3.19`, platform Release 37
  (`platform/catalogs/37/` emitted; vocabulary identical, only the definition
  pin and release moved).
- Docs: `docs/tools.md` (settings row + release note + denial distinction),
  `docs/routines.md` deployment paragraph.
- Regression: `TestPublishedToolScopeConstrainsCall` (denied-vs-not-found
  split), `TestManagedCapabilityDispatchAndNoLegacyFallback` reason counts
  (5 not-found / 8 denied / 1 invalid), `TestChannelContractTerminatesAuthoritativeAnswers`.

## Out of scope

- The malformed `tool_call` (missing `tool`) — schema validation already
  rejected it correctly; model-side issue, not a hub defect.
- Cold-start ~37 s — unchanged by design.
- Fast-path phrase matching for language asks — rejected approach; the shared
  contract fix covers the class.
- `control_http.go`/`knowledge_view.go` backend-error audits keep their
  `outcome` field; off the agent call path.

## Verification

- `go test -short ./internal/toolhub` — green (incl. new reason-class counts).
- `go test -short ./internal/audit` — green.
- `go test -short ./internal/stack` — green except pre-existing
  `TestComposeContainerSupervisorIsSinglePrivateControlService` (fails on the
  clean branch too; unrelated supervisor-ports assertion).
- `go run ./cmd/devcheck catalog-emit` — release 37 emitted, diff vs 36 is
  only `release`/`definition_version`/manifest digests.
- Pending delivery legs: managed-space rebind + respawn so the 18.3.19
  contract actually projects, then a live rerun of the original language ask
  measuring model turns, tool calls and repeated searches.
