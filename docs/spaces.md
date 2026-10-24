---
description: spaces/<id>/ layout — settings.yaml, generated configs, secrets/env files, state dirs, and what is hand-edited vs rendered.
last_verified: 2026-10-20
---

# Spaces

One `spaces/<id>/` directory is one principal's deployment unit: its config,
secrets, generated artifacts and state. `hubctl init`/`render`/`up` work on a
`--dir spaces/<id>` target. User namespace only; org-level material lives in
the org dirs referenced from `settings.yaml`, not inside the space.

## Hand-edited inputs

Two YAML files with different owners — the pair is deliberately split so
capability intent never masquerades as runtime config. Keys placed in the
wrong file are rejected by name, not merged.

| File | Owner | Contents |
|---|---|---|
| `agent.yaml` (schema 3) | operator | runtime config: `user`, `organization`, `model`, `model_url`, `timezone`, ports (`oauth_port`, `browser_port`, `slack_events_port`), `hooks`, `memory`, `honcho`, `cliproxy`, `infra` (exactly one deployed space renders the shared control plane), `diagnostics`, `image_gen`, `web`, `cli_registries`, managed-mode pin (`capability_mode`, `capability_profile_id`, `capability_generation`) |
| `workspace.yaml` (schema 3) | capability intent | the `tools:` map, `ingress:` channels, `workspace:` mounts, `mcp:` raw server definitions. **Visibility, not authority** — ToolHub profiles/grants still govern admission |
| `settings.yaml` (schema 1, legacy) | — | pre-split file; still parses — `features`, `native_toolsets`, `disabled_mcp` migrate into the pair at load — but mixing old keys with the new pair is rejected. `internal/stack/defaults/{agent,workspace}.yaml` seed new spaces |
| `SOUL.md` | owner | The persona/agent-home document — see docs/agent-home.md |
| `secrets.<env>.env` | operator | Provider keys for this space (`TELEGRAM_BOT_TOKEN`, `GOOGLE_*`, `GITLAB_TOKEN`, …) — named by feature `requires`. Never committed beyond the local operator flow |
| `runtime.<env>.env`, `communication.<env>.env`, `model-relay.<env>.env` | operator | Per-service env, one file per environment |
| `runtime.auth`, `media.auth`, `model-relay.auth`, `toolhub-tokens.json`, `supervisor.auth` | operator | Bearer files — auth between services, never literal secrets in compose |
| `communication.users.yaml` | operator | Sibling Telegram identities for the gateway; `TELEGRAM_ALLOWED_USERS` is the infra-owner fallback |
| `telegram-auth.<env>.env` | operator | Telegram QR auth app API pair when `telegram_auth` is on (default for telegram stacks) |

`tools:` backends: `native` (upstream toolset in-runtime — no per-call
admission), `toolhub` (managed executor, per-call admission + audit +
os.Root scopes), `mcp[:server]` (ToolHub-mediated connector), `mcp-raw:name`
(raw `mcp:` definition rendered into `mcp_servers`; managed mode permits
only org-scoped), `off` (explicit deny; absent = denied). `access: ro`
strips every mutation path the backend can honor — write tools denied in the
executor, `browser` loses mutation tools, `mcp-raw` gains `tools.exclude`;
on `native` it is a validation error (upstream grants toolsets whole).

```yaml
tools:
  terminal: native            # upstream toolset inside the runtime
  file:    {via: toolhub, only: [file_read, file_list], paths: [docs, inbox]}
  browser: mcp:playwright     # ToolHub-mediated connector
  gitea:   mcp-raw:gitea      # renders mcp.gitea below
  code_exec: off
  ssh:     {via: toolhub, access: ro, tools: {shell: false, tunnel: false}}
ingress: [telegram]           # transport channels, never agent tools
workspace:
  mounts:
    - {from: docs, to: /docs, mode: ro}
    - {from: org:docs, to: /orgdocs}   # organization dirs are always ro
mcp:
  gitea: {url: https://gitea.example/mcp}
```

## Rendered artifacts (`hubctl render`, never hand-edit)

- `compose.<env>.yaml` — the per-space compose (`internal/stack/compose.go`).
- `hermes.<env>.yaml` — upstream Hermes config.
- `generated/` — merged models.yaml (`models.<env>.yaml`), filtered global
  skills, feature-gated plugins (`generated/plugins/<name>` mounts read-only
  into `/opt/hermes/plugins/` — `source "bundled"`, kind "backend",
  auto-loads, agent-untouchable).
- `connections/` — connector materialization output.

## State (runtime-writable)

- `runtime/` — bind-mounted as `/state` into the runtime: `toolhub/store.json`
  + `audit.jsonl` + `governance.json`, `credentials/store.enc`,
  `credential.key`, `artifacts/`, `hermes/` (agent-visible Hermes state; in
  managed mode skills/hooks/plugins/bundles/scripts/bin/node/lsp are read-only
  tmpfs shadows), routines/watch state, job mappings (ADR-0015).
- `home/` — the agent's home; `workspace/` — the admitted workspace scope;
  `archive/` — read-only extracted archive; `inbox/` — user-file intake.
- `broker/` — broker materialization output; `cache/`, `cliproxy/` —
  service-local state; `managed/<env>/model-relay` — relay selection state.
- `diagnostics/` — bounded log surface backing `mcp__toolhub__diagnostics`
  when `diagnostics: true`.

## Features (schema-1 vocabulary)

In legacy `settings.yaml` the opt-in contract is the `features` list from
`internal/stack/config.go`'s `Features` registry: `workspace`, `web`,
`deep_research`, `browser`, `browser_act`, `ssh`, `ssh_write`, `ssh_shell`,
`ssh_tunnel`, `hh`, `telegram`, `slack_app`, `telegram_user`,
`telegram_write`, `google`, `google_write`, `gitlab`, `meet`,
`transcription`, `image_gen`, `github`, `slack`, `atlassian`, `desktop`,
`drafts` —
each with declared `requires` env keys and a scope note. Write-effect
features (`*_write`, `browser_act`, `ssh_shell`, `ssh_tunnel`) are separate
opt-ins on top of their read parent, matching deny-wins. In schema-3 spaces
the same intent is expressed through the `tools:` map above.

## Secondary spaces

`infra: false` spaces render only their runtime; they reach the shared
control plane (`toolhub`, `credential-broker`, `cliproxy`, and relays) over
the shared docker network by service name.
