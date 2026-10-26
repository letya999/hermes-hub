---
description: spaces/<id>/ layout — settings.yaml, generated configs, secrets/env files, state dirs, and what is hand-edited vs rendered.
last_verified: 2026-10-26
---

# Spaces

One `spaces/<id>/` directory is one principal's deployment unit: its config,
secrets, generated artifacts and state. `hubctl init`/`render`/`up` work on a
`--dir spaces/<id>` target. `hubctl init` creates a user scope;
`hubctl group-init` creates a new dedicated group scope. Both keep their own
home. Org-level material lives in the referenced organization directories.

## Hand-edited inputs

Two YAML files with different owners — the pair is deliberately split so
capability intent never masquerades as runtime config. Keys placed in the
wrong file are rejected by name, not merged.

| File | Owner | Contents |
|---|---|---|
| `scope.yaml` | operator | Host-owned `kind` (`user`, `group`, `organization`), `id` and optional organization binding; group init refuses existing personal scopes |
| `agent.yaml` (schema 3) | operator | runtime config: `user`, `organization`, `model`, `model_url`, `timezone`, ports (`oauth_port`, `browser_port`, `slack_events_port`), `hooks`, `memory`, `honcho`, `cliproxy`, `infra` (exactly one deployed space renders the shared control plane), `diagnostics`, `image_gen`, `web`, `cli_registries`, managed-mode pin (`capability_mode`, `capability_profile_id`, `capability_generation`) |
| `workspace.yaml` (schema 3) | capability intent | the `tools:` map, `ingress:` channels, `workspace:` mounts plus optional `goal`/`bootstrap_revision`, `mcp:` raw server definitions. **Visibility, not authority** — ToolHub profiles/grants still govern admission |
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
- `generated/` — merged models.yaml (`model-registry/<env>/models.yaml`), filtered global
  skills, feature-gated plugins (`generated/plugins/<name>` mounts read-only
  into `/opt/hermes/plugins/` — `source "bundled"`, kind "backend",
  auto-loads, agent-untouchable).
- `connections/` — connector materialization output.

## State (runtime-writable)

M16 also keeps private `runtime/admissions/`, `runtime/knowledge/`,
`runtime/bootstrap.json` and `runtime/bootstrap-history/`. Session history and
compaction live in Hermes' own session store, not in a hub-side copy.
Knowledge snapshots carry explicit freshness and source state.
Bootstrap receipts carry bounded redacted output and changed paths. Their
lifecycle and operator commands are documented in docs/operations.md and
docs/tools.md; they are not shared personal-memory stores.

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

## Shared model profiles and personal accounts

`spaces/profiles.yaml` contains named overlays of `model`, `model_url`,
`cliproxy` and `cliproxy_credentials`. Resolution is default → matching named
group → values explicitly set by the owner. Conflicting matching groups are
rejected even when the owner also supplies an override. Matching groups with
the same value are unambiguous. Profile values never contain credential bytes.

```yaml
profiles:
  default:
    model: reviewed-model
    model_url: http://model-relay:8318/v1
    cliproxy: shared
    cliproxy_credentials: shared
  personal:
    owners: [alice]
    cliproxy: dedicated
    cliproxy_credentials: private
```

`cliproxy_credentials: private` requires dedicated placement; a shared proxy
cannot safely select private upstream accounts by owner without a proven
identity-routing contract. `shared` explicitly declares the operator's shared
upstream pool. An omitted scope preserves the undeclared legacy behavior and is
shown as such by doctor. Dedicated proxy rendering is supported in secondary
(`infra: false`) spaces; the infrastructure space hosts the shared proxy. For a
fully personal-account deployment, keep private accounts out of that shared
instance and use dedicated secondary owner spaces. Mixed shared/private users
use the same arrangement.

Sharing a profile does not share OAuth files, service bearers, workspaces,
selection state or Broker grants. Each space keeps its own proxy directory and
model-relay state. A shared model/provider definition uses explicit owner grant
references in `models.yaml`; see [model-relay](services/model-relay.md). Re-render
only affected spaces. Model registry edits reload after rendering; provider env
rotation recreates that owner's relay; Broker rotation resolves on the next call
without a relay restart. Model identity changes apply to the next owner runtime.

## Hosting policy

Opt-in `hosting` uses `hosting.slots` (up to four), optional digest-pinned `hosting.image` and explicit `hosting.runtime` (`runsc` or `kata`) in agent settings. These are rights-bearing fields. Published sites live in per-user/env named volumes, not the full workspace mount. See [hosting](services/hosting.md).
