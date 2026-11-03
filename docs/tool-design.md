---
description: Normative tool-design rules — one catalog, closed verb vocabulary, typed schemas, rights-vs-schema separation, bounded results, context budget, machine-checkable conformance.
last_verified: "2026-11-04"
---

# Tool design charter

This is the normative contract for every model-visible tool the platform ships:
what a tool is, how toolsets and families are organized, how names, schemas,
descriptions, results and permissions must look, and how each rule is verified.
It binds the `hub-agent-tools` catalog, its ToolHub projection, connector
facades, and any new tool added later — whether authored by platform code or
reviewed for a connector pack.

A rule that cannot be checked by a test is a wish. Each section ends with the
check that enforces it (catalog test, passport compile check, parity test or
eval). For definitions see [glossary](glossary.md); for the effective catalog
see [tools](tools.md); for placement and budgets see ADR-0092 D6 and
[rbac](rbac.md); for the rights vocabulary see ADR-0093 D3 and SPEC-0092.

## 0. Vocabulary

| Term | Meaning |
|---|---|
| `tool` | One callable the model may invoke: name + input schema + description + behavior. The only contract the model sees. |
| `toolset` | The indivisible unit an operator selects in `tools.yaml` (`hub:<family>`, `native:<name>`, `connectors`, `user_mcp`, `org_mcp`). A toolset lives wholly in `core` or `extended` — never split. |
| `family` | A plural domain noun grouping `hub:` tools (`files`, `media`, `tasks`). The family is the capability-id prefix, the catalog toolset suffix, the `workspace.yaml` vocabulary key and the docs section name — one word for the same thing. |
| `member` | A tool that belongs to a toolset. `tools.include`/`tools.exclude` in `tools.yaml` operate on members only. |
| `audience` | Who a tool is for: `model` | `internal` | `operator`. Internal operations (e.g. `inbox_fetch`, legacy control ops) execute through ToolHub but never appear on the model surface or in placeable toolsets. |
| `right` | A `domain.operation` from `rights.yaml` (`workspace.write`, `connectors.enable`). A right is not a tool: each member declares `requires:` rights; a grant satisfies them. |
| `surface` | A delivery channel: `mcp__toolhub__*` (managed), `mcp__hub__*` (unmanaged), `tools_list`/`invoke` (extended). A surface is a projection of the catalog, never a second authoring site. |
| `effect` | `read` or `write` declared on the tool spec; drives MCP annotations, access narrowing and evidence requirements. |

## 1. Inventory — one catalog, one author

**1.1 One ToolSpec is the only source.** Every model-visible tool exists exactly
once in `HubToolsDefinition()`. All surfaces — the managed `mcp__toolhub__*`
projection, the unmanaged `mcp__hub__*` stdio server, and the extended
`tools_list`/`invoke` path — are generated from that spec. Hand-written schemas
or descriptions inside `Server()` or a facade are forbidden: they are the root
of every documented drift (80-field reflection schemas, divergent descriptions,
stub connector contracts).

Check: parity test — the stdio server and the projected definition produce
identical `{name, description, inputSchema, annotations}` per tool.

**1.2 Three audiences, not three mechanisms.** `ToolSpec.Audience` marks every
entry `model`, `internal` or `operator`. Internal operations execute through
the same dispatch but are excluded from model listing and from placeable
toolsets. An `internal` tool reachable by the model is a bug.

Check: catalog test — no `internal` toolset key appears in emitted
`toolsets.yaml` or in the model-visible inventory.

**1.3 Footprint ladder.** A new capability enters at the highest rung that
works: existing tool extension → skill / CLI / recipe → connector → core tool.
A vendor-specific API is never a core tool; it ships as a connector recipe
(e.g. `hh` is a recipe, not `hub:hh`).

Check: review + catalog diff — a new core member requires an explicit entry
in the family registry and a charter citation in its spec.

**1.4 Immutable vocabulary.** Renaming a toolset, tool or parameter is a
breaking change: it lands only in a new catalog release with rebind and
respawn. A silent rename is forbidden — removal and addition are separate
operations in separate releases where migration matters.

Check: `Emit` refuses drifted files; `catalog.Release` bump required.

## 2. Toolsets and families

**2.1 Operators select toolsets, not tools.** `tools.yaml` keys are toolset
names; `tools.include`/`tools.exclude` (mutually exclusive) perform point
surgery on real members only. Enforced by `checkTools` at passport compile.

**2.2 Family = plural domain noun.** The closed family list is
`files`, `exec`, `documents`, `media`, `skills`, `tasks`, `goals`,
`context`, `settings`, `remote`, `sites`, `connectors`, `system`. Two families
never own the same resource; each family maps to one rights domain
(`files → workspace.*`, `remote → remote.*`, `tasks → tasks.*`).

**2.3 Indivisible placement and budget.** `core` holds at most 20 members;
overflow moves a *whole* toolset to `extended` in stable catalog order.
`discovery` (system_guide, tools_list, invoke) is always pinned in core.

Check: `checkTools` + `TestToolsetCoreLimitAndExtendedInvokeRevoke`.

**2.4 Extended is live.** `tools_list(query)` + `invoke` expose extended
toolsets with per-call admission identical to direct calls. `tools_list`
searches name, description, parameter names and `definition_id` — never a
substring match on the tool name alone.

## 3. Naming

**3.1 Format.** `<noun>_<verb>` snake_case, ≤ 40 characters. The noun is the
family's singular (`site_*` inside `sites`, `task_*` inside `tasks`,
`remote_*` inside `remote`). Documented exceptions are the meta tools:
`tools_list`, `invoke`, `system_guide`, `system_status`.

**3.2 Closed verb dictionary.** One verb per effect; synonyms are forbidden:

| Verb | Effect | Banned synonyms |
|---|---|---|
| `list` | enumerate a collection | `ls`, `hosts`, `catalog`, `status` for lists |
| `read` | fetch one resource | `get`, `view`, `fetch`, `show` |
| `search` | query within a collection | `find`, `query`, `lookup` |
| `create` | create a new resource | `add`, `new`, `subscribe` |
| `edit` | any mutation of an existing resource, including lifecycle | `update`, `set`, `manage`, `pause`, `resume`, `cancel`, `complete`, `apply`, `open`, `close` |
| `remove` | terminal deletion | `delete`, `unpublish`, `clear`, `drop` |
| `exec` | run code or a command | `run`, `shell`, `terminal`, `code_exec` |
| `inspect` | analyze content without mutation | `analyze` |
| `status` | snapshot of system/resource state | `get` |
| `publish` | make a resource externally visible (sites only) | `deploy`, `serve` |

Lifecycle verbs never become tools: `task_edit(action=pause|resume|snooze)`,
`remote_tunnel(action=open|close)`. An `action` enum parameter lives only on
`*_create` or `*_edit` and enumerates every allowed value in the schema.

**3.3 Namespace collisions fail closed.** A short name is legal only when
unambiguous; words that exist both upstream and in the hub (`terminal`, `web`,
`skills`, `browser`, `image_gen`) require the explicit `hub:`/`native:` prefix.

**3.4 No vendor or endpoint names.** A tool name states user intent
(`site_publish`), never an endpoint, product or version (`hub_sites_v1_post`,
`hh_apply`). Bare nouns and plurals-as-verbs (`ssh_hosts`, `hh_resumes`) are
banned.

Check: name-lint test over the emitted catalog — regex + verb dictionary +
singular-prefix/family parity.

## 4. Schemas and parameters

**4.1 Typed parameters only.** JSON Schema types: `string` with `enum`/`pattern`,
`integer` with `min`/`max`, `boolean`, `object`, `array`. JSON-in-string
parameters are forbidden — nested JSON serialized into a string is the single
largest source of LLM argument errors (`plan`, `args`, `sources`, `steps`
must become `object`/`array`).

**4.2 Poka-yoke shape.** Flat input object, ≤ 2 nesting levels; `required` on
mandatory params; `additionalProperties: false` where feasible; `enum` on every
choice; safe `default` on optional params. Selector params are enums
(`root: workspace|archive|organization|skills|knowledge`), never free strings.

**4.3 Every parameter carries a description** naming purpose, format/units and
one example value — `limit: "max results returned (1–100, default 20)"`, not
`limit: "limit"`. For `action`, each enum value gets one line of semantics.

**4.4 One parameter, one type, one value.** No string-or-object unions, no CSV
inside strings, no paired `path`/`paths` tools — a collection is always
`paths: array` (1..N); `path` accepts exactly one.

**4.5 Schema stability inside a release.** Only optional parameters may be
added; a new required parameter, removal or a type change requires a new
catalog release.

## 5. Granularity and behavior

**5.1 A tool is one user intent, not one endpoint.** Altitude is the task
("publish a site"), not the HTTP verb. Read tools may stay fine-grained
(`file_read`, `file_list`, `file_search`); write tools aggregate the resource
lifecycle through `action`. Mirroring internal REST routes 1:1 into tools is
forbidden (the 11-tool `assignment_*` family is the canonical anti-pattern).

**5.2 No competing tools.** If two tools answer the same intent, the design is
wrong. Test: for every intent in the eval set there is exactly one obvious
tool. Known violations being consolidated: `watch_*`/`routine_*`/`assignment_*`
→ `tasks`, `image_inspect`/`images_inspect` → one tool, `service_*`/`connector_*`
→ `connectors`.

**5.3 One dispatch, identical semantics.** `ExecCall` is the only execution
path; direct projection and `invoke` route through it with identical
admission and behavior.

Check: parity test — the same `(tool, args)` pair returns the same result via
every surface.

**5.4 Predictability.** Result field names are uniform inside a family; read
tools have no hidden side effects; write tools are idempotent where retry is
dangerous (`expected_revision`, idempotency keys).

## 6. Permissions — schema is not permission

**6.1 Visibility is UX, rights are authority.** Toolset selection decides what
the model may see; `grants.yaml` decides what it may do. Every member carries
`requires: [<domain>.<operation>]` from `rights.yaml`; a tool projects and
executes only while those rights are present in effective grants at the live
epoch. A `tools.select` fallback is a stopgap, not a destination — new families
must land their own domains (`remote.*`, `tasks.*`, `exec.*`) in `rights.yaml`
before their tools ship.

**6.2 Per-call admission at the PEP.** A call is admitted only when the
toolset is selected, the member's `requires` are granted, and its `scope`
(`paths`/`hosts`/`urls`) checks pass — evaluated against the fresh grant
epoch, not the published snapshot. `invoke` and direct calls are identical
(`TestToolsetCoreLimitAndExtendedInvokeRevoke`).

**6.3 Declare only what is enforceable.** `access`, `scope`, `rules` are legal
only on surfaces that provably enforce them (`via: toolhub|mcp`); the same
fields on `native`/`mcp-raw` are a compile error. (Already enforced by
`checkTools`; this codifies it.)

**6.4 MCP annotations derive from the spec.** `readOnlyHint`,
`destructiveHint`, `openWorldHint` are emitted from `Effect` — never
hand-written per surface.

## 7. Results and errors

**7.1 One result envelope.** `{ok: bool, <data>, error?: {code, message,
retryable, hint}}` for every tool. `code` comes from a closed set:
`denied`, `invalid_args`, `not_found`, `conflict`, `unavailable`, `timeout`,
`pending_approval`, `unverified`, `internal`.

**7.2 Bounded output.** Dual line+byte caps on every result; truncation
returns `{truncated: true, next_offset}`. Payloads larger than the cap are
materialized into the workspace and returned as `{path, bytes}`, never inline.

**7.3 Evidence for side effects.** A write tool returns authoritative proof:
`revision`, `id`, `url`, `job_id`, plus a read-back where feasible.
`accepted|queued` never equals `delivered|completed`; a result that cannot be
verified is marked `unverified` — never a bare `ok`.

**7.4 Errors are instructions.** `hint` tells the model the next step
("revision mismatch → re-read the file"); internals, env and secrets never
appear in error output; diagnostics emit allowlisted fields only.

## 8. Context economy

**8.1 Small core.** `core` targets ~15–20 hot tools; everything else lives in
`extended` behind discovery. Connectors and rare families are not core
residents.

**8.2 Description is the contract: ≤ 2–3 sentences.** What it does; when *not*
to use it (with the alternative named); what it returns or which side effects
it has. `action`-multiplexed tools document each enum value. Examples only
where parameters are non-trivial. Banned: imperative flattery ("use this for
everything"), marketing, prose that repeats the schema.

**8.3 Modular prompt contract.** Tool guidance in the system prompt is
assembled from the space's active families only — a denied family is never
mentioned (no hallucinated references, no wasted tokens).

**8.4 One disclosure mechanism.** The long tail is reachable through one path
(`tools_list`+`invoke`, or native `tool_search` where upstream provides it);
the model never sees two competing discovery APIs. Programmatic tool calling
is an optional optimization on top, not a requirement.

## 9. Verification and change control

**9.1 Every rule has a check.** Parity (`Server()` vs catalog), verb-dictionary
lint, `requires` coverage (no member without a right), core budget,
`include`⊕`exclude` compile checks, eval intents. A new tool ships with the
checks its spec touches.

**9.2 Eval-driven iteration.** Descriptions and schemas are tuned by measured
selection/argument accuracy on realistic tasks — never by taste. Metrics:
tool-selection F1, argument-fill accuracy, retry rate, surface token cost.

**9.3 Contract change = one changeset** (repository rule): ToolSpec +
prompt-contract guidance + `docs/tools.md` + immutable catalog version bump +
rebind/respawn of managed spaces — atomically.

## Appendix A. Current-state conformance

| Rule | Violation today | Target |
|---|---|---|
| 3.2 | `delete`/`remove`/`clear`/`cancel`/`unpublish`/`disable` — six removal verbs | `remove` + `action` on `edit` |
| 3.2 | `get`/`read`/`view`/`list`/`status`/`catalog`/`hosts`/`resumes`/`sample` | `read`, `list`, `status`, `search` |
| 3.1 | family `hosting` ↔ tools `site_*`; `terminal` ↔ `code_exec` | families `sites`, `exec` |
| 4.1 | `plan`, `args`, `sources`, `steps` are JSON strings | `object`/`array` |
| 4.4 | `image_inspect`/`images_inspect` duplicate | one tool, `paths: [1..8]` |
| 5.2 | `watches` ⊂ `routines`; 11 `assignment_*` tools | `tasks` ×5 |
| 6.1 | `ssh`/`exec`/`tasks` fall back to `tools.select` | `remote`/`tasks`/`exec` domains in rights |
| 6.4 | no `readOnlyHint`/`destructiveHint` emitted | derived from `Effect` |
| 7.2 | `file_read` lacks offset/limit; `file_search` returns paths only | `next_offset`, match snippets |
| 8.2 | stub descriptions on `connector_*`, shared text on `site_*` | contract-grade descriptions |
| 8.3 | static prompt contract mentions every family | sections per active family |

## Sources

- Anthropic — [Writing tools for agents](https://www.anthropic.com/engineering/writing-tools-for-agents),
  [Code execution with MCP](https://www.anthropic.com/engineering/code-execution-with-mcp),
  [Advanced tool use](https://www.anthropic.com/engineering/advanced-tool-use)
  (Tool Search, Programmatic Tool Calling, Tool Use Examples).
- [MCP client best practices](https://modelcontextprotocol.io/docs/2025-06-18/develop/clients/client-best-practices);
  [Webex MCP best practices](https://developer.webex.com/mcp/docs/best-practices-for-building-mcp-and-a2a-servers)
  (naming, granularity, schema rules).
- Research: arXiv 2606.17519 (routing degradation), 2605.24660 (adaptive
  shortlist size), 2508.16260 (catalog scale vs. success).
- Local precedents reviewed: goose (`platform_extensions/developer` — five
  tools with full annotations), osinara (`resolveToolSurface` per-step),
  codex (`shell`/`apply_patch` + guardian approvals), pi-mono (eight-tool
  minimal core), nanoclaw (one `ncl` CLI + single guard), ironclaw
  (`tool-evidence.md`), eve (`eve__search` single entry point), open-dot
  (`describe`/`defaultDecision` per tool), open-duet (four tools + permits),
  openclaw (dynamic stripping of disabled-tool references).
