---
id: CHG-0121
title: "Knowledge sources move from communication spool to runtime state"
type: fix
scope: internal/agenttools, internal/communication, cmd/hubctl
status: done
---

# CHG-0121 — Phase 1, slice 1b: knowledge out of communication

Knowledge sources are workspace/runtime data, not delivery-plane state.
Today `GET /v1/knowledge` is served by the communication hub from
`spool/knowledge/<user>` and agenttools crosses a network boundary +
control-token auth to read files that conceptually belong to the
agent's own runtime volume.

## Целевое состояние

- Staging root: `<HUB_STATE>/knowledge/<principal>/` (+ optional
  `<context>` subdir, same shape as today) — host-visible under
  `spaces/<user>/runtime/knowledge`.
- agenttools `knowledge_list`/`knowledge_read`/`knowledge_search` read the
  directory in-process via `os.Root` — no HTTP, no control token, same
  name/size/type restrictions as the comm handler enforced.
- Communication `/v1/knowledge` endpoints and `knowledge.go` removed.
- `hubctl apply` gains `--state <dir>` for the knowledge target;
  `--spool` stays routines-only. Knowledge in a spec without `--state`
  is an error, not a silent spool write.

## Тесты

- agenttools: staged file listing/read/search from state dir; foreign
  principal dir unreachable; missing dir → empty list; no workspace writes.
- communication: knowledge endpoint gone (404 on the mux); tests deleted.
- hubctl apply: knowledge written to state dir; spec with knowledge and
  no --state errors; routine-only apply still works with just --spool.

## Docs

- SPEC-0048 knowledge section, SPEC-0049 apply surface,
  docs/operations.md staging path.
