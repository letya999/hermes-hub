# CHG-0210: OpenKnowledge MCP wiring and capability split (M20 stage 7)

Chain: ADR-0086 D8 -> SPEC-0085 R7 -> this plan -> `state.yaml`.
Previous stage: CHG-0209. Next stage: CHG-0211.

## Goal

Hermes sees the OpenKnowledge tool surface through the same governance
as every other capability: read tools under `knowledge_ui`, the four
write verbs under `knowledge_ui_write`, denials enforced at the
ToolHub/governance layer — never merely unadvertised.

## Read before coding

1. SPEC-0040 §2/§5 (surfaces, mutation grants), SPEC-0041;
   `internal/stack/hermesconfig.go` mcp_servers render;
   `internal/toolhub` tools.include/exclude + governance;
   `docs/spaces.md` `access: ro` semantics; OK `docs/reference/mcp.mdx`
   tool list (research.md).

## Slices

### S7.1 Read surface under `knowledge_ui` (R7.1)

Anchors: `internal/stack/hermesconfig.go` server entry render;
`internal/stack/render.go`.

Change:
1. Feature `knowledge_ui` renders an MCP server entry →
   `http://open-knowledge:8080/mcp` (private-network service name) with
   `tools.include` = {`exec`,`search`,`links`,`history`,`audit`,`lint`,
   `config`,`palette`,`preview_url`,`share_link`,`skills`} minus the
   `skills`-write sub-ops if the contract allows expressing it — else
   document the residue.
2. Owner `mcp_servers` may not reuse the name (hub-owned, like `hub`).

Tests (red first):
- `TestOKEntryUnderFeature` — absent without feature.
- `TestOKReadToolIncludeList` — exact set, no write verbs.
- `TestOKNameReserved` — owner can't squat `open-knowledge`.

### S7.2 Write verbs under `knowledge_ui_write` (R7.2)

Anchors: `internal/toolhub` governance/tool filtering; feature registry.

Change:
1. `knowledge_ui_write` adds `write`,`edit`,`delete`,`move`; without the
   grant they sit in `tools.exclude` (deny enforced, deny receipt
   recorded) — the SPEC-0040 `*_write` pattern.
2. `access: ro` on the tools entry strips verbs even when the write
   grant exists elsewhere (deny wins).

Tests:
- `TestWriteVerbsExcludedWithoutGrant` — deny receipt on call.
- `TestWriteVerbsAdmittedWithGrant`.
- `TestAccessROStripsVerbs`.

### S7.3 Org read-only path (R7.3)

Anchors: `internal/stack/scope.go` `ReadOnlyMCP`, org overlay render.

Change:
1. Org `read_only_mcp`/`tools.include` can grant the read surface to
   members; org scope can never grant `knowledge_ui_write` to a user
   runtime (org material is read-only by SPEC-0004).

Tests:
- `TestOrgReadOnlyGrant` — members get read tools only.
- `TestOrgWriteGrantRejected` — validation error.

### S7.4 Boundary contract tests (R7.4)

Anchors: `internal/devcheck` / tool contract test suites.

Change:
1. Calls with `cwd`/paths outside `spaces/<id>/vault` are refused —
   proven via the projection contract tests, not live-claimed.

Tests:
- `TestOKCallOutsideVaultRefused` — `..`, absolute, sibling-space paths.

## Red-team checklist

- Owner defines `mcp-raw:open-knowledge` pointing at the same service to
  bypass include/exclude → reserved-name rejection covers it.
- Write verb invoked directly at the service (not through governance) —
  the service trusts the private network; document the residue: the
  boundary is governance-level admission, same as other MCP connectors.
- `exec` tool reads anything inside the vault project — fine — but a
  symlink inside vault pointing outside → OK confines to project root;
  verify in live acceptance.
- Beta channel installs (`open-knowledge-beta`) never appear in render.

## Acceptance

A7.1–A7.4 on dev stack: read tools listed; `write` denied with receipt;
grant on → attributed edit visible in OK live; outside-vault refused.

## Out of scope

- Per-tool finer grants beyond include/exclude.
- OK skills.sh installs through the agent (evaluated later; `skills`
  stays read-only in M20).
