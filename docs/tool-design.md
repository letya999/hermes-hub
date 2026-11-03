---
description: Normative tool-design rules — one catalog, closed verb vocabulary, typed schemas, rights-vs-schema separation, bounded results, context budget, machine-checkable conformance.
last_verified: "2026-11-03"
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
| `surface` | A delivery channel: `mcp__toolhub__*` (managed), `mcp__hub__*` (unmanaged), `tool_search`/`tool_call` (extended). A surface is a projection of the catalog, never a second authoring site. |
| `effect` | `read` or `write` declared on the tool spec; drives MCP annotations, access narrowing and evidence requirements. |

## 1. Inventory — one catalog, one author

**1.1 One ToolSpec is the only source.** Every model-visible tool exists exactly
once in `HubToolsDefinition()`. All surfaces — the managed `mcp__toolhub__*`
projection, the unmanaged `mcp__hub__*` stdio server, and the extended
`tool_search`/`tool_call` path — are generated from that spec. Hand-written schemas
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

**2.2 Family = plural domain noun.** The catalog r5 closed family list is
`files`, `terminal`, `documents`, `media`, `skills`, `routines`, `watches`,
`assignments`, `goals`, `context`, `settings`, `ssh`, `exposure`, `sites`,
`web`, `browser`, `research`, `connectors`, `models`, `sessions` — twenty
placeable families plus the pinned `platform` layer. Two families never own
the same resource; each family maps to one rights domain (`files →
workspace.*`, `ssh → remote.*`, `assignments`/`watches`/`routines →
tasks.*`).

**2.3 Indivisible placement and budget.** `core` holds at most 20 members;
overflow moves a *whole* toolset to `extended` in stable catalog order.
`platform` (`platform_guide`, `platform_status`, `platform_bootstrap`,
`platform_usage`, `tool_search`, `tool_call`) is always pinned in core —
never operator-selectable or removable.

Check: `checkTools` + `TestToolsetCoreLimitAndExtendedInvokeRevoke`.

**2.4 Extended is live.** `tool_search(query)` + `tool_call` expose extended
toolsets with per-call admission identical to direct calls. `tool_search`
searches name, description, parameter names and `definition_id` — never a
substring match on the tool name alone.

## 3. Naming

**3.1 Format.** `<noun>_<verb>` snake_case, ≤ 40 characters. The noun is the
family's singular (`site_*` inside `sites`, `routine_*` inside `routines`,
`ssh_*` inside `ssh`). Documented exceptions are the pinned platform tools:
`tool_search`, `tool_call`, `platform_guide`, `platform_status`,
`platform_bootstrap`, `platform_usage`.

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
tool. Catalog r5 resolved the documented collisions: `watch_*`/`routine_*`/
`assignment_*` are separate families, `image_inspect`/`images_inspect` merged
into `media_inspect`, and `service_*`/`connector_*` unified under
`connectors`.

**5.3 One dispatch, identical semantics.** `ExecCall` is the only execution
path; direct projection and `tool_call` route through it with identical
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
epoch, not the published snapshot. `tool_call` and direct calls are identical
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
`extended` behind the pinned platform layer. Connectors and rare families are
not core residents.

**8.2 Description is the contract: ≤ 2–3 sentences.** What it does; when *not*
to use it (with the alternative named); what it returns or which side effects
it has. `action`-multiplexed tools document each enum value. Examples only
where parameters are non-trivial. Banned: imperative flattery ("use this for
everything"), marketing, prose that repeats the schema.

**8.3 Modular prompt contract.** Tool guidance in the system prompt is
assembled from the space's active families only — a denied family is never
mentioned (no hallucinated references, no wasted tokens).

**8.4 One disclosure mechanism.** The long tail is reachable through one path
(`tool_search`+`tool_call` on the ToolHub surface, or the native tool-search
API where upstream provides it); the model never sees two competing discovery
APIs. Programmatic tool calling is an optional optimization on top, not a
requirement.

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

## Appendix A. Catalog r5 conformance

The catalog r5 release (ADR-0094) closed the violations this charter recorded
for r4. Current state:

| Rule | r4 violation | r5 state |
|---|---|---|
| 3.2 | `delete`/`remove`/`clear`/`cancel`/`unpublish`/`disable` — six removal verbs | `remove` + `action` on `edit` — enforced |
| 3.2 | `get`/`read`/`view`/`list`/`status`/`catalog`/`hosts`/`resumes`/`sample` | `read`, `list`, `status`, `search` — enforced |
| 3.1 | family `hosting` ↔ tools `site_*`; `terminal` ↔ `code_exec` | families `sites`, `terminal` — enforced |
| 4.1 | `plan`, `args`, `sources`, `steps` are JSON strings | typed `object`/`array` — enforced |
| 4.4 | `image_inspect`/`images_inspect` duplicate | `media_inspect`, `paths: [1..8]` — enforced |
| 5.2 | `watches` ⊂ `routines`; 11 `assignment_*` tools | `watches`/`routines`/`assignments` families — enforced |
| 6.1 | `ssh`/`exec`/`tasks` fall back to `tools.select` | `remote`/`tasks`/`exec` rights domains — enforced |
| 6.4 | no `readOnlyHint`/`destructiveHint` emitted | derived from `Effect` — enforced |
| 7.2 | `file_read` lacks offset/limit; `file_search` returns paths only | `next_offset`, match snippets — enforced |
| 8.2 | stub descriptions on `connector_*`, shared text on `site_*` | contract-grade descriptions — enforced |
| 8.3 | static prompt contract mentions every family | sections per active family — enforced |

## Appendix B. r4 → r5 rename map

Every retired name below is gone from the catalog and the projected surface.
The in-runtime dispatch still understands the r4 names as a compatibility
shim for pre-r5 state, but nothing catalogs, advertises or admits them under
the new selection model — they cannot be selected, searched or granted.

| Retired (r4) | Replacement (r5) |
|---|---|
| `system_guide`, `diagnostics` | `platform_guide`, `platform_status` (+ `platform_bootstrap`, `platform_usage`) |
| `tools_list`, `invoke` | `tool_search`, `tool_call` |
| `service_catalog`, `service_enable`, `service_disable` | `connector_search`, `connector_edit` actions |
| `connector_enable`, `connector_disable`, `connector_remove`, `connector_reauth`, `connector_allow_writes` | `connector_edit` actions (`name` arg maps the selector) |
| `code_exec`, `code_job_*` | `terminal_exec`, `terminal_job` |
| `image_inspect`, `images_inspect`, `image_convert`, `image_generate`, `image_edit`, `video_generate`, `stt`, `tts` | `media_inspect`, `media_convert`, `media_generate`, `media_edit`, `media_transcribe`, `media_speak`, `media_fetch` |
| `archive_*`, `artifact_remove`, `knowledge_*` | `file_*` roots (`root=archive|knowledge`) |
| `ssh_shell_*`, `ssh_tunnel_*` | `ssh_shell`, `ssh_tunnel` actions |
| `exposure_open`, `exposure_close`, `exposure_status` | `exposure_create`, `exposure_remove`, `exposure_list` |
| `context_compact` | `context_edit` action |
| `goal_set`/`goal_pause`/`goal_resume`/`goal_clear` | `goal_edit` actions |
| `skills_list`, `skills_search`, `skill_view`, `skill_manage` | `skill_search`, `skill`, `skill_edit` |
| `settings_get`, `settings_set` | `settings_read`, `settings_edit` |
| `site_status`, `site_unpublish`, `hosting` | `site_list`, `site_remove`, family `sites` |
| `watch_subscribe`, `watch_pause`, `watch_resume`, `watch_delete` | `watch_create`, `watch_edit`, `watch_remove` |
| `routine_get`, `routine_update`, `routine_pause`, `routine_resume`, `routine_snooze`, `routine_delete`, `routine_history`, `routine_preview` | `routine_read`, `routine_edit`, `routine_remove` |
| `assignment_update`, `assignment_apply`, `assignment_pause`, `assignment_resume`, `assignment_progress`, `assignment_cancel`, `assignment_complete`, `assignment_get`, `assignment_status` | `assignment_edit` actions, `assignment_read`, `assignment_list` |
| `hh_*` (job board) | connector recipe `hh` — not a family |
| `ssh_hosts` | `ssh_list` |

## Appendix C. Denied by design

Names in this list are refused at the boundary, not merely missing: they can
never mint a toolset section or project a tool.

- `inbox_fetch` — `audience: internal`; the channel materializes files through
  it. Never projected, placeable or searchable by the model.
- Legacy control operations (`discover`, `prepare_source`, `status`,
  `required_credentials`, `confirm`, `enable`, `disable`, `revoke`, `remove`,
  `rotate`, `diagnostics`, `grant_request`, `install_shared`) — `audience:
  operator` on the ToolHub control surface only.
- `mcp-raw:<server>` — an ad-hoc raw MCP name is never a toolset; curation
  happens in `user_mcp`/`org_mcp`.
- Vendor/product verbs (`hh_apply`, `hh_resumes`, `ssh_hosts`) — banned by
  §3.4; the job board lives behind the `hh` connector recipe.
- Tool-level selection for `platform` — the pinned layer cannot be selected,
  excluded or capped to read-only; it is metadata, not a toolset.

## Appendix D. Rule → check conformance

Every normative rule above has a named, automated check. The lints live in
`internal/agenttools` (catalog-as-authored), the parity tests in
`internal/catalog` (generated == committed bytes), `internal/passport`
(selection boundary) and `internal/toolhub` (live projection/admission).

| § | Rule | Named check |
|---|---|---|
| 1.1 | one catalog, one author | `catalog.TestPlatformCatalogParityAndPinnedToolset` |
| 1.2 | three audiences; internal/operator unreachable | `catalog.TestR5Vocabulary` (`inbox_fetch` is `internal`, catalog members are `model`), `agenttools.TestEveryCatalogedHandlerToolDispatches` |
| 1.3 | footprint ladder (`handler:`/`runtime:`/`flow:`) | `agenttools.TestExecutorRoutingContract`, `toolhub.TestRemoteExecutorDispatch` |
| 1.4 | immutable vocabulary | `catalog.TestPlatformCatalogParityAndPinnedToolset`, `agenttools.TestCatalogDeniesRetiredNames` |
| 2.1 | toolsets, not tools | `passport.TestPassportCatalogBoundaryRejected` |
| 2.2 | family = plural noun, closed list | `catalog.TestR5Vocabulary`, `stack.TestEveryR5FamilyAcceptsToolhubVia` |
| 2.3 | indivisible placement; core ≤ 20; include ⊥ exclude | `passport.TestToolsetCoreLimitAndExtendedInvokeRevoke`, `toolhub.TestToolsetCoreLimitAndExtendedInvokeRevoke` |
| 2.4 | extended lives through `tool_search`/`tool_call` | `toolhub.TestDirectAndToolCallAdmissionParity` |
| 3.1 | `<noun>_<verb>` ≤ 40 chars | `agenttools.TestCatalogVocabularyLint` |
| 3.2 | closed verb dictionary | `agenttools.TestCatalogVocabularyLint` |
| 3.3 | unknown/colliding names fail closed | `passport.TestPassportCatalogBoundaryRejected` |
| 3.4 | no vendor or endpoint names | `agenttools.TestCatalogVocabularyLint`, `agenttools.TestCatalogDeniesRetiredNames` |
| 4.x | typed schemas, stable per release | `catalog.TestPlatformCatalogParityAndPinnedToolset` (schema drift breaks byte parity) |
| 5.2 | no competing tools | `agenttools.TestIntentSelectionEval` (a score tie fails the intent) |
| 5.3 | one dispatch, identical semantics | `agenttools.TestEveryCatalogedHandlerToolDispatches`, `toolhub.TestDirectAndToolCallAdmissionParity`, `agenttools.TestKnowledgeMCPTransportParity` |
| 6.1 | real `requires`; `tools.select` only on `tool_call` | `agenttools.TestEveryModelToolRequiresRight`, `catalog.TestEveryToolDeclaresRequires`, `catalog.TestHubToolsRequiresAreCatalogRights` |
| 6.2 | per-call admission; epoch revocation bites next call; PEP bounds | `toolhub.TestDirectAndToolCallAdmissionParity`, `toolhub.TestConcurrentRevocationCannotPassAdmission`, `agenttools.TestExecCallReadOnlyGate`, `agenttools.TestExecCallScopeBoundaries`, `agenttools.TestToolPolicyDispatchDeny` |
| 6.4 | MCP annotations derive from `Effect` | `toolhub.TestProjectedToolAnnotationsMatchEffect` |
| 7.1, 7.4 | result envelope; errors teach r5 names | `agenttools.TestRoutineErrorsTeachR5Names`, `toolhub.TestAgentExecErrorSurvivesGateway` |
| 7.2 | bounded output | `agenttools.TestContextStatusToolReportsBoundedView`, `agenttools.TestRoutineListRequestsBoundedDefaultPage`, `agenttools.TestRoutineGetIncludesBoundedHistoryAndPreview` |
| 7.3 | evidence for side effects | `agenttools.TestRoutineMutationRequiresObservedRevision`, `agenttools.TestAssignmentToolRequiresRevision` |
| 8.2 | description budget; stuffing penalized | `agenttools.TestDescriptionBudgetLint`, `agenttools.TestIntentSelectionEval` (density-normalized scoring) |
| 8.3 | modular prompt contract | `passport.TestPromptPointerNamesR5PlatformPair` |
| 8.4 | one disclosure mechanism | `catalog.TestR5Vocabulary` (pinned `platform` member list), `toolhub.TestConnectorSurfaceR5Names` |
| 9.2 | eval-driven iteration | `agenttools.TestIntentSelectionEval` |

**r5 baseline** — deterministic local report from
`agenttools.TestIntentSelectionEval`:
`catalog=r5 intents=40 selection_accuracy=1.000 ambiguous=0 arg_fill=n/a
retry_rate=n/a token_cost=n/a`.
The scorer is keyword/IDF-based with a fixed intent suite; arg-fill accuracy,
retry rate and token cost require a live model and are honestly `n/a` here.

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
