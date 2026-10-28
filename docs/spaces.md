---
description: spaces/<id>/ layout — settings.yaml, generated configs, secrets/env files, state dirs, and what is hand-edited vs rendered.
last_verified: 2026-10-28
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
| `communication.users.yaml` | operator | Sibling Telegram identities for the gateway; `id`/`runtime_id`/`telegram_ids`/`policy_version` must match the rendered stack policy or tool projection silently returns an empty list; `TELEGRAM_ALLOWED_USERS` is the bot-level allowlist and must contain every owner's id |
| `models.yaml` | operator | Logical model registry: ids without dots (`default`, `alt`), each ladder hop's `model:` an upstream name present in that space's cliproxy `/v1/models`. `agent.yaml model:` references a registry id. Hot-reloaded by model-relay |
| `media.grant` | operator | hub-media's broker grant id — not a secret; consumed only through `hubctl up` compose interpolation, never mounted |
| `cliproxy/` | operator | Dedicated-proxy material (`config.yaml`, `api-key`, `auths/`) when `cliproxy: dedicated` — per-owner accounts, never copied between spaces |
| `telegram-auth.<env>.env` | operator | Telegram QR auth app API pair when `telegram_auth` is on (default for telegram stacks) |
| `profile.lock` | host | init-stamped provisioning baseline: profile name, version and sha256 of each stamped file — the 3-way reference for `hubctl profile diff|apply`. Hand-editing it cannot widen an org assignment (the org member/default wins at render) |

## Provisioning profiles (SPEC-0083 R9)

A profile is a starting-state bundle resolved per space. Profiles live at
`<hub-root>/policies/profiles/<name>/` and `<org>/profiles/<name>/`; an org
copy shadows a platform profile of the same name per file, and `extends`
chains resolve leaf→root:

```
profile.yaml    # {description, extends, version}
agent.yaml      # runtime template (fallback: policies/agent-default.yaml)
workspace.yaml  # tool wishes (fallback: policies/workspace-default.yaml)
grants.yaml     # capability-rule template — drafts only
SOUL.md         # persona layer (never stamped into the user file)
skills.yaml     # standard skills: [name, name@revision, ...]
layout.yaml     # extra dirs: {dirs: [a, b/c]}
```

Assignment resolution: `hubctl init --profile <name>` → the member's
`{role, profile}` entry → `OrganizationSettings.default_profile` → `default`
(which resolves to the legacy `*-default.yaml` templates). Members keep the
legacy scalar form `alice: member` or the map form
`alice: {role: member, profile: sales}`. Init stamps `agent.yaml` and
`workspace.yaml`, creates `skills/`, writes `profile.lock` and materializes
`layout.yaml` dirs (entries containing `..` are rejected). `hubctl org
sync-members --org acme --org-dir <dir> [--spaces root] [--dry-run]` creates
missing member spaces idempotently and never overwrites.

The effective SOUL layers in order: channel contract → organization SOUL →
profile SOUL → user SOUL → skill catalog. `skills.yaml` pins order first in
the effective catalog; pinning a skill the space cannot run is a render-time
error. `grants.yaml` only becomes authority through
`hubctl profile grants` (renders a draft `CapabilityPolicy` revision with the
rules as `defaults`, ceiling untouched) followed by the confirmed
`hubctl capability --kind policy --confirm` publish — the store still
rejects anything outside the organization ceiling. `hubctl profile diff` /
`apply` three-way the stamped files against `profile.lock`: untouched files
update, customized files are kept and reported.

`tools:` backends: `native` (upstream toolset in-runtime — no per-call
admission), `toolhub` (managed executor, per-call admission + audit +
os.Root scopes), `mcp[:server]` (ToolHub-mediated connector), `mcp-raw:name`
(raw `mcp:` definition rendered into `mcp_servers`; managed mode permits
only org-scoped), `off` (explicit deny; absent = denied). `access: ro`
strips every mutation path the backend can honor — write tools denied in the
executor, `browser` loses mutation tools, `mcp-raw` gains `tools.exclude`;
on `native` it is a validation error (upstream grants toolsets whole).

Every `native:*` governance section is deny-by-default (SPEC-0083 R3.1): a
`tools:` wish only marks visibility — the toolset renders when an active
ToolHub grant covers `native:<name>` (`hubctl governance --kind grant`),
otherwise the read fails closed. The native `skills` toolset is never
grantable: the hub-built effective view at `/skills` replaces upstream
loading in both modes, and the curator stays disabled.

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
- `generated/` — merged models.yaml (`model-registry/<env>/models.yaml`), the
  effective skills view `generated/skills-effective/` (platform > org > user,
  mounted read-only at `/skills` — sources are `config/skills/`,
  `<org>/skills/` and `spaces/<user>/skills/`; `skills/` is the user-scope
  store — bind-mounted rw at `/skills-store` in the runtime and the
  communication gateway (skill credential forms persist under its private
  `.state/` — credentials, settings and the hub-journaled usage counters
  `.state/usage/<skill>.json` — never content), read-only at `/skills-store` in toolhub for one-call
  credential leases, org scope rw at
  `/skills-store-org` on org-bound spaces only; the same view mounts
  read-only at `/skills-effective` in toolhub and the communication gateway
  for enum projection and `/name`/`$name` expansion, and principal-scoped
  CLI cells bind it read-only at `/skills` — its files carry no exec bits,
  so scripts run only from a scratch copy; the file/document/image
  executors expose it as the `skills:` read-only root), feature-gated plugins (`generated/plugins/<name>` mounts read-only
  into `/opt/hermes/plugins/` — `source "bundled"`, kind "backend",
  auto-loads, agent-untouchable).
  Under the user store, `skills/.sources/<id>.git` are the per-owner bare
  git mirrors that pin each installed-from-git skill to a commit (source id
  = sha256(repo-url)[:16]); a published skill's `.origin.json` records
  `{source_id,url,ref,commit,path,tree_sha,scan,fetched_at}` as hub-private provenance —
  it never enters the effective view or a draft tree. `hubctl skill
  sources remove` deletes a mirror and drops update tracking for the skills
  it fed.
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
