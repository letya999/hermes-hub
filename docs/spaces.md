---
description: spaces/<id>/ layout — six authored passports, generated configs, secrets/env files, and state dirs.
last_verified: "2026-11-06"
---

# Spaces

In a managed space, `passports/tools.yaml` selects reviewed connector tools
through cataloged `via: mcp` toolsets. Removing a toolset removes its model
projection after apply while its operational connection and credentials stay.
Raw MCP declarations stay under `tools.connectors`; a `via: off` entry for
one server deselects that server without removing its declaration. The
published revision drives runtime rendering, and a raw MCP selection change
respawns the managed runtime.

One `spaces/<id>/` directory is one principal's deployment unit: its config,
secrets, generated artifacts and state. `hubctl init`/`render`/`up` work on a
`--dir spaces/<id>` target. `hubctl init` creates a user scope;
`hubctl group-init` creates a new dedicated group scope. Both keep their own
home. Org-level material lives in the referenced organization directories.

## Hand-edited inputs

Managed spaces author `passports/{stack,config,models,workspace,tools,grants}.yaml`.
`hubctl space apply` validates and publishes one immutable revision of all six.
Tool selection and intended grants are separate documents.

**Managed (published) spaces**: once `hubctl space apply`
publishes a revision, the six passports under `generated/passports/
<revision>/` are the sole runtime authority — render, supervisor spawn,
sibling enrollment, `hubctl up/chat/doctor/trace` and execution selection
resolve through `stack.ReadEffective`, which opens the revision without
`agent.yaml`, `settings.yaml`, or the old `workspace.yaml` and `models.yaml`
files (SPEC-0095 R1.2). A present `current` pointer without a loadable
revision fails closed. The unpublished legacy reader remains until the
CHG-0283 cutover is finished; it is not a managed-space authority.

| File | Owner | Contents |
|---|---|---|
| `scope.yaml` | operator | Host-owned `kind` (`user`, `group`, `organization`), `id` and optional organization binding; group init refuses existing personal scopes |
| `agent.yaml` (schema 3) | retired | Legacy unpublished reader only; it cannot extend a published managed revision |
| `workspace.yaml` (schema 3) | capability intent | unmanaged: the `tools:` map, `ingress:` channels, `workspace:` mounts plus optional `goal`/`bootstrap_revision`, `mcp:` raw server definitions. **Retired under a published revision** — `tools.yaml`/`workspace.yaml`/`grants.yaml` passports replace it |
| `settings.yaml` (schema 1, legacy) | — | pre-split file; still parses for unmigrated spaces — `features`, `native_toolsets`, `disabled_mcp` migrate into the pair at load — but mixing old keys with the new pair is rejected. **Retired under a published revision.** `internal/stack/defaults/{agent,workspace}.yaml` seed new spaces |
| `models.yaml` | operator | unmanaged: logical model registry (ids without dots, ladder hops into cliproxy `/v1/models`). **Retired under a published revision** — the `models.yaml` passport (`services`/`routes`/`default`) is the only registry the relay reads |
| `SOUL.md` | owner | The persona/agent-home document — see docs/agent-home.md |
| `secrets.<env>.env` | operator | Provider keys for this space (`TELEGRAM_BOT_TOKEN`, `GOOGLE_*`, `GITLAB_TOKEN`, …) — named by feature `requires`. Never committed beyond the local operator flow |
| `runtime.<env>.env`, `communication.<env>.env`, `model-relay.<env>.env` | operator | Per-service env, one file per environment |
| `runtime.auth`, `media.auth`, `model-relay.auth`, `toolhub-tokens.json`, `supervisor.auth` | operator | Bearer files — auth between services, never literal secrets in compose |
| `communication.users.yaml` | operator | Sibling Telegram identities for the gateway; `id`/`runtime_id`/`telegram_ids`/`policy_version` must match the rendered stack policy or tool projection silently returns an empty list; `TELEGRAM_ALLOWED_USERS` is the bot-level allowlist and must contain every owner's id |
| `media.grant` | operator | hub-media's broker grant id — not a secret; consumed only through `hubctl up` compose interpolation, never mounted |
| `cliproxy/` | operator | Dedicated-proxy material (`config.yaml`, `api-key`, `auths/`) when `cliproxy: dedicated` — per-owner accounts, never copied between spaces |
| `telegram-auth.<env>.env` | operator | Telegram QR auth app API pair when `telegram_auth` is on (default for telegram stacks) |
| `profile.lock` | host | init-stamped provisioning baseline: profile name, version and sha256 of each stamped file — the 3-way reference for `hubctl profile diff|apply`. Hand-editing it cannot widen an org assignment (the org member/default wins at render) |

In an organization directory, `settings.yaml` is the host-owned organization
policy for members, features and organization actions. `scope.yaml` is only its
kind/ID marker; changing it does not alter the running policy. The retired
`settings.yaml` row above describes a user space, not an organization directory.

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
`workspace.yaml` during the ongoing cutover, and creates the six authored
`passports/*.yaml` from per-file profile overrides or embedded defaults.
`profile.lock` records the six passport baselines; `hubctl profile diff|apply`
updates only unchanged authored passports and never publishes them. Init also
creates `skills/` and materializes `layout.yaml` dirs (entries containing
`..` are rejected). `hubctl org
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
`apply` three-way the six authored passports against `profile.lock`: untouched files
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
For a supervised runtime, the selected and granted native set is also the
`api_server` platform set. The renderer removes an upstream disabled composite
only when it overlaps a selected native toolset; unrelated native sets stay
disabled. This is required for the pinned Hermes API path to register the
selected tool in an actual run.

```yaml
tools:
  terminal: native            # upstream toolset inside the runtime
  file:    {via: toolhub, only: [file_read, file_list], paths: [docs, inbox]}
  browser: mcp:playwright     # ToolHub-mediated connector
  gitea:   mcp-raw:gitea      # renders mcp.gitea below
  skills:  toolhub            # skill catalog reads; skill_edit covers drafts+publish
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
- `home/` — the agent's home; `workspace/` — the admitted workspace scope:
  `inbox/{people,services}/` user- and service-file intake (read-only to the
  agent), `work/`, `projects/`, `artifacts/{documents,images,videos,archives,
  services}/`, `outbox/`; `archive/` — read-only extracted archive.
- `vault/` — the git-backed memory vault when `memory_vault` is on
  (ADR-0086): `memory/` markdown (`daily/`, `cards/`, `summaries/`,
  `CORE.md`, `REVIEW.md`, `schema.json`) committed by the governed
  writer, runtime sidecars under `runtime/` (`.okignore`d, never
  committed: `prov.db`, `index/`, `night/` day states + `truth_pending`,
  `memory/forget.jsonl` soft-forget strikes, `memory/writes.jsonl` the
  card-write audit journal, `memory/import/` the staged native-import
  drafts, `memory/scope.key` the hub-generated replay mint material,
  `memory/provider.json` the rendered external-provider record and
  `memory/replay.cursor` the migration checkpoint). Under M32 the vault
  is ToolHub-owned: the runtime has no `/vault` mount, no `vault:` file
  root and no shared run-scope stamps — reads (`memory_search`, pre-turn
  CORE/recall) and writes (`memory_card`, capture) pass the ToolHub
  boundary's verified envelope, published memory switch and exact live
  private run (SPEC-0098). ToolHub's protected dreaming routine reads
  `config.memory.nightly_rollup` and `config.runtime.timezone` from the
  published passport. At the local `"HH:MM"` (default 04:00) it folds
  the just-closed day —
  promotions, directives, REVIEW journal, owner digest, intent→draft
  assignment proposals — then closes any finished week/month/year whose
  summary is missing. Two opt-in settings bound the leaf tape after
  that: `memory_leaf_fold_bytes` (>0) folds audit-kind bodies inside a
  completed oversized leaf (interactive blocks stay verbatim), and
  `memory_leaf_retention_days` (≥2) expires leaves past the window once
  their rollup + daily summary provably exist — kept days and actions
  journal to `REVIEW.md` and the morning digest (SPEC-0085 R7.5). With
  `knowledge_ui` on renders no `open-knowledge` container (M32/#520):
  ToolHub owns the vault, the upstream editor cannot act as a
  governed-API client, so the feature is explicitly unavailable —
  `ui_port`/`ui_bind` stay valid but publish nothing. There is no
  `mcp__open_knowledge__*` surface; edits outside the governed writer
  are impossible, and `memory_card op=propose` plus owner confirmation
  is the only edit path (SPEC-0098). Vault content and `.ok/` state
  are never deleted — the surface is suspended, not removed.
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
`drafts`, `memory_vault`, `knowledge_ui`, `memory_provider` —
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


## Applying authored passports

`hubctl init` now stamps all six files from embedded defaults or a profile's
`passports/<name>.yaml` overrides. Profile inheritance resolves each file
independently. `models.yaml` is intentionally blank until the owner selects
a route and enrolls its credential; apply rejects that incomplete model.
The old init files remain during CHG-0283 and are not the intended final
configuration authority.

`hubctl space apply --dir spaces/<user> --env dev [--confirm]` compiles the
six authored files in `passports/` and diffs the result against the published
revision. Each `stack.yaml` must declare `identity.user` equal to the space
name and a boolean `identity.infra`. `grants.yaml` may contain `allow: {}` to
disable all granted operations; tool selection does not add rights. The host
publisher accepts only active self grants already issued in the protected
ledger. A later tools selection change requires `tools.configure` in both the
current published allow and that ledger.
For `identity.organization`, every authored grant must also fit the protected
organization policy's `right_ceiling`; apply verifies its exact active profile,
generation and environment before publishing. An absent policy or ceiling
denies requested rights.
`config.memory.enabled` is required in authored sets and controls whether the
vault feature is active independently of tool selection. The same
`config.memory` block carries `nightly_rollup` (`HH:MM`),
`leaf_retention_days` (0 or at least 2) and `leaf_fold_bytes` (nonnegative).
Published values override the old `agent.yaml` memory schedule and retention
fields; absent values reset to their defaults.
Managed spaces also declare `tools.binding.profile_id` and a positive
`tools.binding.generation`; the published binding supplies the runtime and
ToolHub identity even when `agent.yaml` disagrees or is absent.
Missing authored passports fail; `passport-src/` is no longer read by this
command. The unpublished legacy reader remains pending CHG-0283 removal.
For a deployed `spaces/<user>` path, managed reads and infra-owner selection
reject a published set whose `metadata.space` or `stack.identity.user` names
another user, even if no `scope.yaml` exists.
The default dry-run prints a JSON receipt: every changed leaf with its
before/after (credential refs render as `<ref:name>`, never material), the
effect class (live/respawn/recreate/rebuild) and owning consumer, plus the
ordered minimum action plan — nothing is written. `--confirm` writes the
immutable six-passport bundle, gates `current` as pending while refreshing
the mounted config view, renders generated files and derives the bound
ToolHub profile and rights from that candidate. Only then does it atomically
move `current` to the new revision. Managed config readers reject pending or
mismatched revisions; ToolHub list/call/rights endpoints also reject an
unfinished apply journal until reconciliation completes.
The first apply publishes and activates the revision without starting services;
`hubctl up` starts them after images and service secrets are provisioned.
After activation, reconcile rebuilds only the service images that
changed, recreate only the affected containers, run one compose
`--remove-orphans` sweep when instances were dropped (a removed
`stack.services` entry removes exactly that container), mark the runtime
for a deferred supervisor restart-request when its boundary moved, and run
registered live hooks. On failure, reconcile repoints `current` at the previous
revision and replays affected service actions and live hooks against the
restored render. `rolled_back` is false if any replay fails. A render or view-refresh failure also
restores the previous `current` pointer and mounted passport view. When a
previous revision exists, rollback also re-renders its generated service
files. The restored profile ceiling includes only previous passport rights
that still have an active issued grant; a concurrent grant revocation stays
revoked. A failed rights restore reports rollback divergence. The durable
`generated/passports/apply-journal.json` records the environment, previous and
candidate revision, profile IDs, phase and affected service names. A later
`space apply` first restores the previous view and rights, then replays possibly
affected service actions. Repeated recovery is idempotent. If a first apply
may have touched services, an image build was interrupted, or replay fails,
the journal stays in place and `current` is pending until operator repair;
ToolHub admission remains closed. A dev apply does not alter the prod sibling.

`settings_read` reads the active config passport revision and reviewed values
through ToolHub. `settings_edit` sends one setting with that revision as
`base_revision` through ToolHub to the authenticated supervisor publisher.
It requires current `config.edit_own` in both issued and published grants,
refuses an unpublished authored draft or stale base, and writes a new
six-passport revision plus the authored `passports/config.yaml` leaf. An
omitted/empty value removes the leaf. Successful publication finishes its
service effects before returning; the changed setting survives a runtime
respawn and re-render. `self-settings.json` and retired `agent.yaml` fields
cannot override the published config.

Config authority inventory: `passports/*.yaml` are the six editable sources;
`generated/passports/<revision>`, `generated/config-view/<env>` and rendered
Hermes/Compose files are derived consumers; `generated/passports/current` and
the apply journal are protected publication state; ToolHub profiles and the
issued-right ledger are protected admission state. The runtime settings
executor and self-settings overlay reader/writer are removed. Legacy
`agent.yaml`, `settings.yaml`, `workspace.yaml` and `models.yaml` remain inert
for managed effective reads; unconfigured spaces still use their documented
legacy provisioning paths until migration.

| Config authority | Writers | Readers and effect |
|---|---|---|
| Six authored `passports/*.yaml` | init/profile apply; `cmd/hubctl/space_settings.go` writes one `config.yaml` leaf after governed publication | `passport.ReadAuthored` and `CompileAuthoredDocs` validate the complete candidate |
| Immutable revision, `current`, config view, apply journal | `passport.Revision.Prepare/Activate`, `cmd/hubctl/space_apply.go` and recovery | `passportview.LoadEffective`/`LoadRevision`, `stack.ReadEffective`/`ReadEffectiveRevision`; journal fences ToolHub |
| Rendered Hermes, Compose and runtime policy | `stack.RenderRevisionEnvironment` and `MaterializeHermesConfig` | `supervisor.materializeHermesConfig` and runtime attestation read the published render |
| Bound ToolHub profile and issued grants | `migration.DeriveSpaceRights`, protected governance operations | ToolHub MCP/rights admission evaluates the bound profile plus live issued ledger |
| Retired overlay and legacy managed config | no effective writer or reader; direct runtime settings tools deny | old `self-settings.json`, `agent.yaml`, `settings.yaml`, `workspace.yaml`, `models.yaml` cannot change managed effective config |

The rendered compose set is commanded by `stack.services`: `per-space`
instances become local containers, `shared` instances render only on the
infra space, `supervised` keeps `hermes-runtime` as a spawn contract. A
declared per-space instance with no rendered container — or an instance
name that resolves to no catalog service — fails the render; the compose
file never silently shrinks.

Publishing also refreshes the runtime's `/config`: a stable read-only
directory bind at `generated/config-view/<env>/` whose individual files are
replaced atomically per revision — the six passports, `prompt-pointer.txt`
and the space's `SOUL.md` — instead of Docker single-file binds that never
see updates. The view's `revision` marker must match `current` for managed
host reads. Already running external services can observe their existing
binds until reconciliation; service effects finish before ToolHub admission
reopens.

`config.yaml` commands the runtime contract: `runtime.timezone`/`locale`
and `context.{compaction,parallel_lanes}` land in the rendered environment
and the managed context policy; `ports.<name>` publishes exactly its owning
service (`oauth-callback` → hermes-runtime, `browser-vnc` → the browser
sidecar, `slack-events` → communication-hub) and `GOOGLE_OAUTH_REDIRECT_URI`
re-derives from `ports.oauth-callback`; `supervision` renders
`runtime.<env>.policy.json` beside the env files — the supervisor re-reads
it per decision, so `idle_stop`, `restart` and `watchers` apply live without
a respawn; `channels` names become ingress transports, `voice` wires the
STT/TTS endpoints and credential `{ref:}` names resolve through the existing
secret path at service start; `memory.backend` flips the Honcho contract.

Multi-space live acceptance was exercised against ten disposable managed
spaces (`m29-acc-01..10`, `infra: false`, dedicated `cliproxy`), each with a
distinct `tools:` selection and capability profile. After migration,
capability records were published into the shared ToolHub store (policy +
profile + `agent-tools` binding per principal), tokens enrolled through the
infra space render, and each space's `toolhub` relay answered a live MCP
`initialize`/`tools/list` handshake with exactly its own bounded core —
four-tool file surfaces, a three-tool read-only file surface, document,
assignment, routine/service/settings, artifact/hh and mixed profiles, and a
43-selection profile capped at a 20-tool direct core. All ten model relays
returned real provider completions; unauthenticated MCP requests answered
401 and a non-admitted tool call answered `unknown tool`. See
docs/services/toolhub.md for the publication sequence this requires.

Two derivations matter for real spaces. `stack.services` lists the services
the space's managed topology *consumes*, not only the containers it runs: a
secondary space (`infra: false`) resolves catalog `depends_on` transitively,
so its compiled service set includes the shared mesh (toolhub →
credential-broker, communication-hub → supervisor, model-relay → cliproxy)
hosted on the infra space. And `cliproxy` is the built-in model provider —
ladders may reference `provider: cliproxy` without a `providers:` entry (the
relay admits it implicitly); the migrator then declares `cliproxy` inside
`models.services` so `models.default` still resolves inside the compiled set.

## Hosting policy

Opt-in `hosting` uses `passports/config.yaml` for `hosting.slots` (up to four), optional digest-pinned `hosting.image` and explicit `hosting.runtime` (`runsc` or `kata`). `passports/tools.yaml` selects hosting separately from grants. Published sites live in per-user/env named volumes, not the full workspace mount. See [hosting](services/hosting.md).
