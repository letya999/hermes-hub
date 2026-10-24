# CHG-0070: Managed capability cutover for the local space

Issue 122 close-out + live migration of `spaces/telegram-8275678764` to
`capability_mode: managed` with supervisor execution. Owner authorized the
switch on 2026-10; transcript of the operator request: "переключись в этот
режим. Заскриптуй перенос, не потеряй сессии, файлы, энвы, креды. Доделай
leases/cancellation P4. Полиси профайл под текущий набор по дефолту; hh и
transcription — нет."

## Scope decisions (honest capability map)

Served under managed v1:

| Surface | Backend | Why it works on the agent network |
|---|---|---|
| files/docs/images/artifacts/image_gen/code_exec | toolhub agenttools executor (docker exec into managed runtime; scratch via sibling container) | no egress needed; executor-side |
| web, deep_research | native toolset behind new `web-egress` squid relay | CONNECT allowlist per provider host |
| image_gen provider calls | `hub-media` attached to agentNet | internal service |
| memory/todo/session_search/vision | native carveouts | local state |
| telegram ingress | communication-hub (unchanged) | gateway side, not agent |

Dropped under managed v1 (documented, not silently broken):

| Surface | Reason |
|---|---|
| hh | owner excluded it |
| transcription (stt/tts) | owner excluded it |
| browser / playwright MCP | open-web egress is not expressible in the reviewed allowlist model |
| services/service_* tools | need `communication-hub` control API — must stay off agentNet (broker approve surface) |
| routines via tools | same control-API dependency; `/v1/routines` stays gateway-side only |
| connectors (github/gitlab/google/slack/...) | no connections enrolled under managed v1; needs per-connector admission decision |
| ssh | no reviewed host config for this space |

## Code changes

1. `internal/agenttools/capability.go` — fine-grained capability ids
   (`files.list`, `files.read`, …) because a profile admits at most one
   selection per (capability_id, connection_id). `readOnlyToolNames` gains
   family-prefix matching so `access: ro` on `file` still denies `files.write`.
2. `internal/stack/render.go` — managed dirs gain `workspace`; managed
   `/workspace` becomes a durable bind of `managed/<env>/workspace` (tmpfs
   removed); `managed-runtime.<env>.env` gains web provider keys, proxy env
   and hub-media endpoint/auth; compose gains `web-egress` service and
   `hub-media` joins the agent network when managed.
3. `internal/supervisor/supervisor.go` — same durable workspace in
   `managedRunArgs`; `normalize` creates `managed/<env>/workspace`.
4. `docs/operations.md` — managed cutover runbook section.

## Migration (scripted, `scripts/migrate-managed.ps1`-equivalent via steps)

- `workspace/`, `hermes/`, `home/`, `cache/` → `managed/dev/<name>/`
- `runtime/` (toolhub store, credentials, artifacts, connector state) stays —
  it is control-plane state, not the agent's `/state` anymore
- env/auth files at the space root are shared, unchanged
- settings.yaml → agent.yaml+workspace.yaml split (schema 3) with
  `capability_mode: managed`, profile pin, `model_url: http://model-relay:8318/v1`,
  `tools:` map above, `ingress: [telegram]`
- spool/tasks live in communication-hub volume — untouched; session ids stay
  valid because `hermes/` carries over whole
- rollback: legacy `settings.yaml` + unmanaged dirs untouched; re-render old mode

## Publish order

1. code changes + `just check`
2. `hubctl render --env dev` (dry) → compute `HUB_POLICY_VERSION`
3. `capability --kind agent-tools` → register `hub-agent-tools` + binding
4. generate policy/profile JSON (generator in .work/…/gen.go), preview, confirm
5. switch settings to managed, render, start supervisor, `select-execution`
6. `hubctl up`, verify relays/isolation/bot end-to-end
