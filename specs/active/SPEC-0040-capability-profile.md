# SPEC-0040: Standard user-scoped capability profile

Frozen: 2026-10-02. Issue 122.
Hermes pin `869228cab4a8276d3b4c78da9d9939670c47bd0f` (`0.21.0`).

1. The user-scoped catalog is the materialized `hermes-effective.<env>.yaml`
   plus the ToolHub projection for that owner. It is a function of exactly
   four host-side inputs: `settings.yaml`, the organization overlay
   (SPEC-0004), the validated self-service state file, and the owner's
   effective ToolHub bindings (SPEC-0023). Same inputs produce the same
   file; nothing in the catalog is sampled, negotiated, or discovered at
   call time.
2. A capability reaches Hermes through exactly one surface: a native
   `platform_toolsets` entry, a hub-rendered `mcp_servers` entry, an
   owner `mcp_servers` entry, or the ToolHub remote endpoint. No
   capability is published twice, and no MCP server wraps a native
   toolset. Owner `mcp_servers` names may not reuse a hub-owned name
   (`hub`, `browser`, `browser_guest`, `toolhub`, or a connector feature
   name) or a native toolset name (`terminal`, `file`, `web`, `skills`,
   `todo`, `cronjob`, `messaging`, `memory`, `session_search`,
   `google_meet`, `vision`, `image_gen`).
3. Matrix: availability `default` renders in every Init profile,
   `opt-in` only when the feature/binding is enabled.
   | Capability | Surface | Availability | Effects | Credential | State | Network | Heavy |
   |---|---|---|---|---|---|---|---|
   | Workspace files, archive, terminal | native `terminal`/`file`/`todo` + `hub` MCP (`workspace`) | default | read+write inside `/workspace`; `/archive` read-only | none | workspace mount | no | bounded sizes/locks |
   | Safe runtime ops (memory, skills, sessions, cron) | native `memory`/`skills`/`todo`/`session_search`/`cronjob` | always | per-user | none | `hermes/` state | no | no |
   | Web search/extract | native `web` toolset | always on this pin; `web` feature gate lands with SPEC-0035 | read | provider keys optional | no | yes | per-provider bounds |
   | Bounded deep research | bundled skill | SPEC-0035 (`deep_research`) | read | same as web | on-disk rounds | yes | round/page caps |
   | Browser | `browser`+`browser_guest` MCP | `browser` (default) | read/navigate | none | persistent + in-memory profiles | yes | Chromium |
   | Browser actions | same servers, mutation tool list | `browser_act` opt-in | mutating | none | same | yes | explicit instruction |
   | SSH hosts and tunnels | `hub` MCP ssh tools | `ssh` opt-in; `ssh_write`/`ssh_shell`/`ssh_tunnel` stacked | read; write/shell/tunnel separate | Broker lease or `file:` keys | none | yes | allowlists, PTY bounds |
   | Documents | `hub` MCP document tools (SPEC-0033) | `workspace` | read/create/edit/convert | none | workspace | no | size/page caps |
   | Image inspect/convert | `hub` MCP image tools | `workspace` | read/convert | `OPENAI_API_KEY` (inspect) | workspace | model endpoint | concurrency/size caps |
   | Image generation/edit | `hub` MCP image tools | `image_gen` opt-in | mutating artifact | `OPENAI_API_KEY` or `FAL_KEY` | workspace | provider | concurrency 1 |
   | SQL and bundle connectors | ToolHub endpoint | opt-in binding | per recipe read/write split | Broker | per-owner workload | yes | workload bounds |
   | Account connectors (google, slack, github, gitlab, atlassian, telegram_user) | ToolHub endpoint | opt-in/self-service | read; `*_write`/`telegram_write` separate | Broker/OAuth | per-owner | yes | per connector |
   | Channels (telegram, slack_app) | gateway transport, not a runtime tool | opt-in | inbound only | gateway-owned secrets | ledger volume | yes | n/a |
   | Desktop/Drafts companions | owner `mcp_servers` via control flow | opt-in | per server | bearer token | none | yes | no |
   | Meet captions | native `google_meet` | `meet` opt-in | read | Google session | meet state | yes | explicit join |
   | Speech transcription | native `stt` local | `transcription` opt-in | read | none | model cache | download once | CPU model |
   | Own-workload diagnostics | diagnostics op (SPEC-0034/0031) | always, owner-scoped | read | none | diagnostics store | no | bounded queries |
   | HeadHunter | `hub` MCP hh tools | `hh` (default) | read; apply is `hh.apply` org/user action | `HH_TOKEN` | none | yes | receipt-bounded |
4. Always-on rows are fixed in code; nothing user-writable adds a toolset.
   Opt-in rows render nothing until enabled: no `mcp_servers` entry, no
   env reference, no mount, no self-env key. A credential `Requires` name
   appears in the rendered env only while its feature is enabled
   (`FAL_KEY` exists only for `image_gen` provider `fal`).
5. Mutation effects are separate grants: `browser_act`, `*_write`,
   `ssh_write`/`ssh_shell`/`ssh_tunnel`, `telegram_write`, `hh.apply`.
   Each still requires a concrete owner instruction at call time; a
   grant is not an instruction. Organization scope additionally requires
   the matching `org_actions` entry, and owner `mcp_servers` are replaced
   by organization-approved entries.
6. Revocation removes the capability, not the history. Dropping a
   feature or self-service flag drops its rendered entries on the next
   materialization and its tools on the next process start. A ToolHub
   revoke deletes the binding and bumps the projection revision, so the
   connector disappears without restarting Hermes. Deleting a credential
   fails the next call closed. Workspace files, memories and diagnostics
   persist; they are deleted only by their own explicit operations.
7. User boundaries hold at every surface: mounts, Hermes state, ToolHub
   bindings, Broker leases, diagnostics and workspaces resolve only from
   the invoking `spaces/<user>`; shared image layers and the shared
   network share no credentials, grants, writable state or projections.
8. Resource-heavy rows (browser, image_gen, transcription, ssh shells,
   deep research) are opt-in or carry explicit concurrency/size/deadline
   bounds; Init defaults add no always-on heavy capability.
9. Operator path: enable a host feature in `settings.yaml` + `hubctl up`,
   or a self-service connector from chat; `hubctl doctor` reports the
   missing `Requires` keys. Removal is the symmetric edit or revoke;
   `hubctl secret delete` removes the credential. Recovery is a normal
   `up` after fixing settings; no capability persists outside these
   paths.
10. Fixtures and httptest doubles prove render and contract invariants
    only. A live run against the pinned image remains the deployment
    gate recorded in validation.md.
