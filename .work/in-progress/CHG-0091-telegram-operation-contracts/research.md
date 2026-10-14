# Research and code evidence for CHG-0091

Reviewed 2026-10-13 on dev e059aa8 plus uncommitted CHG-0088/0089/0090 work.
No production code, deployments, account data or transcripts are copied here.
Source findings below describe the inspected code, not a fresh live acceptance.

## Local evidence and navigation

Serena activated the local project with its Go language server. Symbol/reference
queries traced inspectMany, sniffImage, imageFormat, createSchedule, nextExpression,
hubCommunicationEnv, RequestApproval and validArtifactRel. LSP diagnostics on the
inspected media/approval files returned none; stack/schedule diagnostics returned
style hints. This does not replace build, race or integration checks.

Semble search and find_related connected the file-transfer specs, watcher research,
tool descriptions, routine dispatcher and private agent executor. Searches were
scoped to the local repository. Ripgrep verified callers, tests, pinned versions,
HTTP handlers and the absence of media_group_id support. No new LSP server was
installed and no whole-repository refactor was performed.

| Finding | Verified code boundary | Consequence for design |
|---|---|---|
| Telegram photo without file_name uses file ID | communication/media.go handleFile | Synthetic naming improves intake; inspection must not rely on suffix |
| inspectMany and imageFile call extension-based imageFormat | media/images.go | Fix both callers, reuse existing sniffImage/imageBounds, leave output format explicit |
| hub child env omits model key/config override | stack/hermesconfig.go hubCommunicationEnv; stack/render.go Config | Explicit minimal forwarding in new and existing-config paths |
| Direct routine description is generic; ToolHub description lists modes | agenttools/tools.go Server versus capability.go HubToolsDefinition | Share reviewed semantics; no new scheduler |
| Watch state has prev/current and builds was/now output | communication/watchers.go settleProbe; TestWatcherProbeLifecycle | Agent refusal is not evidence of an unsupported watcher |
| nextExpression supports only once/cron; creation samples time.Now independently of validation | communication/schedules.go; http.go | Resolve after once with one clock and preserve its receipt |
| Duplicate create errors; new IDs are independent | handleRoutines/createSchedule; AgentExecRequest lacks trusted intent ID | Receipt-based same-ID replay; no claim of cross-ID semantic dedup |
| Queued approval suppression checks terminal run only | communication.go deliverOne; approval_messages.go | Add current-ID/decision/expiry/audience gate and cleanup-state distinction |
| Auto-delivery buckets are narrower than instructions | runtime/artifacts.go validArtifactRel; HTTPRunner/Server | Correct wording; keep explicit MEDIA route and originals |
| Message and ExecuteRequest carry no group/multi-inbound contract | communication Message/Job; runtime ExecuteRequest | Album collection is a subsequent HTTP/recovery slice |

## Primary references and choices

1. [MCP Python SDK v2.0.0 stdio](https://github.com/modelcontextprotocol/python-sdk/blob/v2.0.0/src/mcp/client/stdio.py): subprocess env is a safe baseline plus explicitly configured env. Use minimal forwarding, not parent-env copying. Context7 resolved /modelcontextprotocol/python-sdk and returned v2.0.0 transport documentation confirming this boundary.
2. [Pinned Hermes config loader](https://github.com/nousresearch/hermes-agent/blob/869228cab4a8276d3b4c78da9d9939670c47bd0f/tools/mcp_tool_config.py) and [transport](https://github.com/nousresearch/hermes-agent/blob/869228cab4a8276d3b4c78da9d9939670c47bd0f/tools/mcp_tool_transport.py): _load_mcp_config interpolates configured env; unresolved references stay literal; _build_safe_env filters and merges server env before spawning. A loader/transport canary must cover both steps. The [pinned lock](https://github.com/nousresearch/hermes-agent/blob/869228cab4a8276d3b4c78da9d9939670c47bd0f/uv.lock) selects mcp 2.0.0. Main-branch examples are not a dependency-upgrade recommendation.
3. [Go image documentation](https://pkg.go.dev/image#DecodeConfig): decoded header exposes dimensions and detected format; resource limits must be checked before full decode. Context7 /websites/pkg_go_dev_go1_25_3 corroborated the stable API; the local module uses Go 1.27.2. Reuse the existing WebP decoder rather than adding one.
4. [Go time documentation](https://pkg.go.dev/time#ParseDuration): duration arithmetic and persisted calendar deadlines are distinct; monotonic process readings are not persisted schedule state. Normalize relative reminders to UTC once, retain source intent for retries.
5. [AWS Builders' Library on idempotent APIs](https://aws.amazon.com/builders-library/making-retries-safe-with-idempotent-APIs/): client request ID distinguishes repeats; same ID with different parameters is a conflict. Adopt receipt comparison under the existing local lock. This is a pattern reference, not a reason to add AWS, DynamoDB or a queue.
6. [MCP tool contract](https://modelcontextprotocol.io/specification/2025-11-25/server/tools): descriptions, input schemas and structured results are discoverable metadata; annotations are not trusted authorization. Fix discoverability while preserving host-owned policy. Native listing and projected listing need separate checks.
7. [Telegram Bot API](https://core.telegram.org/bots/api#message): media_group_id relates messages within a chat; document filename/MIME are optional and getFile may not retain them. Preserve available metadata and check actual bytes. The [keyboard API](https://core.telegram.org/bots/api#editmessagereplymarkup) also offers markup-only edits; retaining existing delete-based cleanup avoids a new UI path in the core repair.
8. [PTB maintainers' media-group patterns](https://github.com/python-telegram-bot/python-telegram-bot/wiki/Frequently-requested-design-patterns#how-do-i-deal-with-a-media-group): each album member is a separate message; timer collection can miss late members, manual completion trades convenience for certainty. Borrow the problem framing, not its Python library. The proposed Go collector must declare its heuristic window and late behavior.
9. [changedetection.io API](https://github.com/dgtlmoon/changedetection.io/blob/master/docs/api-spec.yaml): extraction/filtering, snapshots and notifications are separate surfaces. The Hub already has its smaller required shape; importing that service or adding JSONPath/CSS/browser probing is outside this repair.
10. [Compose config](https://docs.docker.com/reference/cli/docker/compose/config/): validate resolved configuration without starting services. Keep CHG-0090's synthetic matrix and quiet validation; it does not prove subprocess interpolation or model vision support.

## Requested community survey

- [DEV: Saleor's environment configuration experience](https://dev.to/saleor/dynamic-configuration-for-mcp-servers-using-environment-variables-2a0o) is an author account of using a launch wrapper. It suggests checking the process boundary; its shell/env-file wrapper is unnecessary for the Hub's existing rendered env allowlist.
- [Medium: MCP stdio walkthrough](https://medium.com/@lalit.sirsikar/from-single-agent-to-multi-agents-using-mcp-stdio-aea085b1155c) was inspected as a tutorial. It adds no stronger identity/recovery guarantee than the SDK source and is not the authority for this design.
- [Substack: designing idempotent writes](https://agentblueprint.substack.com/p/designing-idempotent-write-operations) motivated checking regenerated identifiers and independent read-back. Provider behavior is established separately by API/source contracts. No universal semantic or physical exactly-once guarantee is adopted.
- X searches for MCP env, media groups and agent idempotency returned no usable indexed primary post. No X claim is used. An authenticated X session was not opened.
- GitHub research used official SDK source, pinned Hermes files and maintainer documentation. The local ai-repo-safety github-guard does not support contents/tree resources and its allowlist rejected the upstream repo. Its configuration was not changed; public-source reading followed the owner's explicit GitHub research request through web/public URLs. No private GitHub material was fetched.

## Rejected approaches and remaining limits

No new workflow engine, DB, queue, provider, native vision, shell rename fallback or
blanket environment inheritance. No watcher reimplementation: the false refusal is
discovery/agent-path evidence, not a missing baseline/diff algorithm. No broad
workspace auto-send, automatic memory-file exposure or global text-based dedup.

Chosen bounds (relative delay 1s-365d, album quiet 2s/hard 10s) are proposed operating
choices, not values prescribed by sources. Review them with the requirements before
freezing. Receipt/clock choices have deterministic fixtures; actual model semantics,
Telegram buttons and transport uncertainty require separately authorized acceptance.
Research found no basis to mark the linked issues fully closed.
