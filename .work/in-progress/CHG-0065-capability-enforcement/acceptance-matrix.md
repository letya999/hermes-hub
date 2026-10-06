# T01–T22 acceptance traceability — CHG-0065

Each row names the reproducible evidence that exists **today** on
`feat/capability-profile-122`, the layer it runs at, and what is still open.
"Unit" means a Go test; "fake-docker" means the re-exec contract harness
(command argv + protocol, not container isolation); "canary" means real
Docker; "pinned" means the `869228c` Hermes image probe. A row is not
claimed at a layer it has not run.

Source pin: `869228cab4a8276d3b4c78da9d9939670c47bd0f`.
Local probe image: `hermes-hub:0.3.0-dev`
`sha256:1c34df2b6fa648e3ec49b71e5ea5e47d60c5a5600c2ea8da168f758571e57f4a`.

| ID | Evidence today | Layer | Open |
|---|---|---|---|
| T01 zero profile CLI/API/channel/cron/child | `devcheck hermes-capabilities` probe: 27 platform resolvers + forged calls + cron per-job `enabled_toolsets` + platform/`enabled=None` fallbacks all yield zero tools; delegate child inherits denial; forged `hosted_room_dispatch`+`_room_execution_policy` rejected at relay | pinned + Go | Live managed lifecycle via supervisor canary only; not every channel launched under real config |
| T02 missing/malformed policy, membership, store | `capabilityProfileLocked` denies on all listed causes; malformed-record and snapshot-fence tests; restart reconstruction from history | unit | — |
| T03 unavailable/new leaf, changed group/schema | Materialization rejects edited native/MCP/extension/hook/memory/install config; group revision fencing (`checkGroupRevisions`); definition digest mismatch denies | unit + pinned (hook/plugin discovery) | Dynamic leaf inventory limited to attested config diff |
| T04 duplicate name/alias/group expansion/backend replacement | Selection uniqueness (`validateStructure`), ambiguous-binding `ErrConflict`, digest-pinned revision enforcement, group snapshots at fixed revisions | unit + MCP session | — |
| T05 hidden name through list/search/describe/prompts/invoke | Open MCP session proof: zero control surface, exact grant visibility, unauthorized lookup/call never reaches backend; projected descriptions carry scope only | unit + protocol | prompts/resources pagination surfaces |
| T06 concurrent sessions, forged identity/token/audience/generation | Foreign session ID rejected (404), exact runtime-owner binding checks, generation-bound tokens, cross-user separation in shared store | unit + live transport | Load-scale concurrency beyond test harness |
| T07 default install/config/lifecycle, forged confirmation | `Confirmation{issuer,at,digest}` required on policy/profile/grant writes; `hubctl` `--confirm` gate prints digest; stale revisions conflict; self-install denied without grant | unit + control flow | Operator UX beyond CLI |
| T08 org ceiling/defaults, user allow/deny, expiry, dev/prod | `capabilityRuleDecision` property/table tests: ceiling intersection, deny precedence, `expires_at`, environment pinning; live dev deployment 2026-10-06: two managed principals on one stack with separate policies (`telegram-8275678764` rev1→2 re-confirmed, `telegram-480637186` rev1); MCP probe per token: 480637186 → 80 tools scoped to its own github connection, `local`/`test-owner-2` → 0 tools | unit + live deployment | — |
| T09 absent/wrong/shared credential, revoked lease | `resolveLocked`: missing connection / wrong owner / revoked or degraded connection deny; injection only after admission; live 2026-10-06: unauthenticated → 401, no-grant principals → `unknown tool`, forged cross-connection and wrong-digest tool names → `unknown tool`; per-owner connections with distinct `credential_ref`s; active binding pinned to `connection_revision` 2 denies once conn revoked (rev6); real provider read (`github get-me`) returned the owner's account through 480637186's admitted binding, admit→allow in audit ledger | unit + live wire + live provider | — |
| T10 delegation/composite/routine after parent revoke | Delegate child inherits denial (pinned probe); composite subcalls share admission path; routine calls pass the same `Call` dispatch | unit + pinned | Queued-job wake reauthorization on real schedules |
| T11 revoke vs concurrent call, stale caches | Open-session revoke proof; `ReverifyEffective` export fence; race tests on store | race + open MCP | In-flight irreversible remote effects reported, not undone (by design) |
| T12 stale save/restart, audit disk failure | Shared file fence, atomic snapshot + ordered history rollback on failed write, `HUB_AUDIT_LEDGER` required for dispatch | fault-injection unit | Operator-stop-during-storage-failure path |
| T13 mount/network topology | managed-network canary: ToolHub reachable, broker/sibling DNS+IP denied, no default route; supervisor canary: internal per-user network, relay-only control route, secret/mount isolation | canary (real Docker) | Multi-supervisor/host port namespaces |
| T14 traversal, symlink/junction swap, rename, hardlink | `ScopedSub` `os.Root` sub-roots; symlink inside scope and swapped scope directory denied; file-scope narrows to parent dir; exec-pack defense-in-depth prefix check | unit + fs | Hardlink and rename-race not separately probed (os.Root boundary covers link targets outside root; no explicit canary) |
| T15 read/create/edit/delete separation | Distinct capability tuples per operation; conversion needs create authority on O_EXCL destination; denied op leaves canary unchanged | unit + file integration | media/artifact alias coverage beyond tested ops |
| T16 hostile shell/code: state/key/proc/socket/net | `DockerScratchExec` hardened spec: `--network none --read-only --cap-drop ALL no-new-privileges uid 10001`, tmpfs scratch/outputs/tmp, mem/cpu/pids bounds; `scratch-workload-canary` on real image (uid, ro rootfs, no workspace/state/socket/network, bounded disk) incl. CI containers job | canary (real Docker) + fake-docker | Runtime-side exec-pack lease on managed topology covered by supervisor canary's docker-exec channel |
| T17 result export, stale revision, missing scratch | Create-only bounded export under admitted scope; `ReverifyEffective` fence stops export on revoked binding or bumped revision; malformed tar/input rejected; real-image export proven in `TestDockerScratchExecPausedQuarantine` post-quarantine run (`echo > $OUTPUTS/proof.txt` → applied create-only) | fake-docker + real Docker | — |
| T18 revoke/expiry, background procs, controller failure | Stop confirmed via inspect before success; unconfirmed stop quarantines that binding only; binding-local quarantine isolation; `ErrIsolation` on unconfigured; real-Docker fault injection `TestDockerScratchExecPausedQuarantine`: paused executor makes `rm -f` fail, inspect still shows it → ErrIsolation, durable lease file persists, replacement lease denied, confirmed removal restores execution | real Docker fault-injection + fake-docker | Lease-expiry under real long-running code (bounded by lease ctx + quarantine) |
| T19 write/install hooks/plugins/skills/config + restart | Sealed read-only tmpfs extension roots; `HookRegistry` two-process canary; preflight attests effective YAML; `.env`/`.op.env`/managed-scope refusal | pinned + canary + unit | — |
| T20 migration/restart/rollback | `PreviewCapabilityProfile` old-to-new diff on the shared evaluator (added/removed/changed + quarantined denies); rollback test: stale revision conflict, only reviewed selections restored, no legacy resurrection; live: both telegram spaces run schema-3 `workspace.yaml`+`agent.yaml` (legacy `settings.yaml.unmanaged.bak` preserved), `capability_changes` keeps both policy revisions with separate operator confirmations, `audit.jsonl` records live managed dispatch with pinned revisions | unit + live deployment | — |
| T21 each replacement, selected account | Exactly one implementation enforced (`ambiguous capability binding` conflict + digest pin); native path disabled by materialization; per-selection `connection_id` scopes account; live 2026-10-06: 480637186's catalog exposes only its own github connection's tools, `test-owner-2` disabled connection yields zero tools, real `get-me` provider call returned the correct account | unit + live provider | Per-connector calls exercised for github only; other providers by owner manual verification |
| T22 egress redirects/DNS/IP/private/metadata | Model relay: exact chat route, single pinned model ID, redirects rejected, proxy env ignored; ToolHub relay redirect denial (unit + wire canary); link-local metadata denied in supervisor canary | canary + unit | Arbitrary connector egress policies per future executor |

## Honest limits

- CI `containers` job (`just docker-check-prebuilt`: smoke, hermes-contract,
  capability-contract, managed-network/supervisor canaries,
  scratch-workload-canary) is green on the latest image — GHCR sha-tag runs
  through 2026-10-06. The scratch paused-stop fault injection
  (`TestDockerScratchExecPausedQuarantine`) passed on real local Docker the
  same day and is wired into both docker gates for the next CI image run.
- T09/T21 live-provider evidence: automated wire-level deny proofs plus one
  real read call (github `get-me`) on 2026-10-06; remaining providers were
  verified manually by the owner on the live deployment — recorded as
  owner-verified, not re-derived here.
- In-flight remote side effects are reported, not undone (by design); operator
  UX is CLI-only; multi-supervisor/host port namespaces are out of scope for
  the single-supervisor deployment.
- Deliberate relaxations, accepted: `mcp-raw` in managed mode has no per-call
  ToolHub audit (org-scoped definitions only); `access: ro` on whole native
  toolsets is rejected rather than partially enforced.
- This matrix records implementation progress only. `implementation_status`
  moves to release-ready once the maintainer accepts the recorded evidence.
