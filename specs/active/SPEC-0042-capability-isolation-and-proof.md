# SPEC-0042: Capability execution isolation and proof

Status: accepted and frozen requirements baseline, 2026-10-02. Issue 122.
Implementation progress and acceptance evidence are tracked in CHG-0065.

Decision: [ADR-0032](../../docs/adr/ADR-0032-capability-execution-isolation.md).
Policy contract: [SPEC-0041](SPEC-0041-default-deny-capabilities.md).
Plan: [CHG-0065](../../.work/in-progress/CHG-0065-capability-enforcement/plan.md).

## IS-01: Threat model and guarantee

Assume malicious instructions from a model, user document, MCP response or skill;
forged calls; guessed hidden names; a compromised connector; and arbitrary code
inside an explicitly granted terminal workload. These actors must not cross
user/context/environment boundaries or change their authorization. The trusted
base is the host/kernel/container runtime, reviewed control services, pinned Hermes
harness and operator-approved extension/executor code. Host root and compromised
control administrators are outside the guarantee. A permitted read can disclose
its content to the model; authorized outbound channels need their own policy.

The baseline supports a trusted host running isolated agent workloads. A deployment
requiring protection against container escape needs a separate VM/host boundary
and its own acceptance; shared-kernel Docker alone must not be advertised as that.

## IS-02: Storage, processes and network

Keep canonical `spaces/<user>` identity and distinct dev/prod state. Physical
control storage may be reorganized, but must never be a parent writable mount
shared with the agent. The following access matrix is normative; concrete volume
names are an implementation detail resolved and reviewed in the Compose diff.

| Data/service | Hermes | File/media executor | Terminal/code | Control owner |
|---|---|---|---|---|
| Grants, bindings, catalog, audit | No filesystem access | None | None | Only the owning control service |
| Broker encryption/signing/admin keys | None | None | None | Broker only |
| Runtime identity / selected model credential | Only required scoped client authority | Separate scoped executor identity; narrowly needed model calls via adapter | None | Issuance/rotation only |
| Effective configuration and executable extensions | Immutable approved view | None | None | Publish reviewed revisions |
| Hermes private sessions/state | Own context only | None | None | Lifecycle/backup under explicit operator authority |
| Workspace/archive/org roots | No direct native file route; no broad live write mount | Only selected roots, operations and user context | Approved input copies + scratch | No blanket gateway workspace mount |
| Docker socket/controller admin endpoint | None | None | None | Dedicated controller/supervisor only |
| Other runtimes or executor identities | No lateral access | No lateral access | No lateral access | Explicit lifecycle control only |

Use separate volume mounts, service identities and process isolation, not file
permissions on a shared writable directory alone. A read-only secret is still a
readable secret. Hermes needs a mutable data home, but hooks/plugins/skills and
other import/config sources must be separately protected against replacement,
rename, symlink and restart-based activation from that data home.

Code executors have no network by default. Reviewed egress is enforced outside
the workload, including DNS/IP changes, redirects, IPv4/IPv6, metadata endpoints,
host gateways and proxy bypass. Resource destinations are derived from policy,
not arbitrary model-supplied URLs. Hermes may reach its selected model adapter and
ToolHub; private executors accept only authenticated dispatch from the proper
gateway. Sharing a Docker network does not establish these properties. Native
stdio MCP, browser/debug ports and alternate localhost listeners are included.
The agent-facing ToolHub path exposes only authenticated `/mcp`; credential,
OAuth and control routes stay on the control network. Infra-owner and secondary
users receive the same route without attaching the ToolHub control process to
an agent network.

## IS-03: Files and indirect writes

Resolve a logical root ID server-side to an approved handle and operate through
`os.Root` or an equivalent traversal-resistant primitive supported by the target
platform. Never authorize by a cleaned path string followed by an unrelated open.
Own workspace, organization material, archive and another user's shared root are
different resources with independent grants. The initial organization/archive
policy is read-only; absent sharing grants deny even path enumeration.

Separate list/search/read/create/edit/delete permissions. Directory deletion,
rename/move and overwrite must be classified by their actual source/destination
effects; no convenient alias may bypass delete/edit rights. Updates use the
existing revision/concurrency checks to prevent stale replacement. Imported files
are bounded regular content; foreign hardlinks, device nodes, unsafe archive links
and unexpected mounts cannot establish access outside the assigned roots.

Document/image conversion, extraction caches, generated artifacts, image editing,
export and `artifact_remove` use the same resource/action policy. A granted
conversion that creates a file needs create authority at its destination; it
cannot silently edit an existing path. Keep output, depth, count, page and time
limits. Error messages must not disclose another user's paths or contents.

## IS-04: Terminal/code and isolation lifecycle

The initial terminal/code implementation uses the existing workload controller to
create an exact-scope disposable executor. Require a reviewed image/entrypoint,
non-root identity, no privileged mode/host namespaces/devices/Docker socket,
read-only base image, dropped capabilities, no-new-privileges, appropriate seccomp,
bounded memory/CPU/PIDs/disk/output/time and a finite executor lease. These settings
are necessary controls, not proof that an image or kernel is secure.

The executor gets explicitly authorized input copies, scratch and a sanitized
environment. It gets neither a live writable workspace nor Hermes state/config,
model credentials, connector credentials or operator/gateway bearer tokens. If
read scope is path-limited, materialize only that authorized subset, not its parent
directory. Stop/free the workload and private scratch on completion/expiry according
to the retention policy; export only authorized artifacts.

Scratch writes/deletes are local computation. Applying results to a workspace is
a separate file operation with current grants, original revision and bounded
content checks. Deletions require explicit delete authority and cannot be inferred
from a file missing in the scratch copy. Avoid live-workspace shell mode in v1;
it would invalidate a promise of edit permission without delete permission.

Revocation first fences new dispatch and lease renewal, then requests stop. Record
`execution_stopped` only after the controller confirms the process tree is gone.
An unreachable/unconfirmed executor is quarantined and remains visibly pending;
do not issue a replacement lease or report success while it might still act.
Apply the same boundary to execute_code, browser scripts, child agents, SSH shells
and routine script modes, or leave those paths disabled. Killing local processes
does not undo an already accepted external side effect.

## IS-05: Runtime and extension admission

Render every supported Hermes launch surface explicitly, especially `api_server`
used by `/v1/runs`. Reject unapproved CLI overrides, session-injected MCP servers,
platform defaults and alternate config homes. Regenerate effective configuration
only on the control side. Persisted schedules and child sessions do not retain
old authority after a policy change.
Managed runtime startup must not load legacy self-managed environment or service
state, and its self-env update endpoint must reject writes before changing state.
Managed Hermes must not mount organization documents wholesale or publish legacy
connector callback ports; those routes require separately authorized control
or file capabilities.

The pinned-image compatibility test checks actual outgoing schemas and invocation
behavior, not just the configuration file. Verify no extra native tools, automatic
MCP merge, tool-search bridges, provider/context-engine tools, kanban modes or
plugin tools appear outside the resolved profile. Test absent/empty/malformed
lists and newly registered tools. A nonempty native deny list based on composite
names is not sufficient evidence of leaf-tool denial.

No default plugin/skill installation, lazy package install, shell hook, Python hook
or config-backed command may introduce execution. Approved skills can be listed
or read only with those grants; their scripts, declared environment passthrough
and linked resources acquire no authority from the skill text. System prompts and
instructions are generated from the allowed projection, not an unconditional
description of all installed services. Operator observability remains outside the
agent projection and must not become a hidden agent escape route.

## Acceptance matrix

Each row needs a reproducible test/evidence reference in the implementation CHG.
Fixtures use synthetic users and canary data, never production secrets. Unit tests
must test negative side effects as well as error codes. Pinned-image tests exercise
the real Hermes tool registry/dispatcher; protocol stubs do not prove that boundary.

| ID | Requirement / scenario | Required observable result | Evidence layer |
|---|---|---|---|
| T01 | CP-01 zero profile across CLI/API/channel/cron/child | No agent capability schemas or implicit business actions; forged direct calls rejected | Go + pinned Hermes |
| T02 | CP-01 missing/malformed policy, invalid membership, unavailable store | Run or dispatch denied; no fallback defaults | Go + runtime |
| T03 | CP-02 optional unavailable/new native/plugin/MCP leaf; changed group/schema | Inventory detects it; no inherited grant; unknown tool never disclosed/dispatched | Inventory + pinned Hermes |
| T04 | CP-03 duplicate name, alias, group expansion, backend replacement | No ambiguous routing or transferred grant; exact revision is enforced | Go + MCP |
| T05 | CP-04 hidden name through list/search/describe/prompts/resources/invoke, pagination or notifications | No catalog leak or shared-cache contamination; denied lookup/call reaches no backend | Go + protocol |
| T06 | CP-04 concurrent user sessions, forged identity/session/token, wrong audience or generation | No cross-owner data, grant, credential, notification or call | Go + live transport |
| T07 | CP-05 default install/config/lifecycle, forged confirmation/replay | No install, config write, grant or binding change; audit denial | Go + control flow |
| T08 | CP-01 org default/ceiling, user allow/deny, expiry, dev/prod | Exact permitted tuples only; deny wins; no cross-grant scope combination | Go property/table tests |
| T09 | CP-06 absent/wrong/shared credential and revoked lease | Injection only after authorization into the selected owner workload | Broker + executor |
| T10 | CP-08 delegation/composite/routine after parent revoke | Child cannot widen rights; queued/renewed work denied | Go + pinned Hermes |
| T11 | CP-09 revoke vs concurrent call, stale connection/list/cache | New admissions deny after committed fence; admitted calls have explicit outcome | Race tests + open MCP session |
| T12 | CP-09 stale store save/restart, audit disk failure, expiry during queueing | No resurrected grant or unaudited new call; recoverable ordered records | Go fault injection |
| T13 | IS-02 infra-owning and secondary user mount/network topology | Neither accesses control state, sibling runtime, controller or broker admin | Generated Compose + Docker canaries |
| T14 | IS-03 traversal, symlink/junction swap, rename race, hardlink/import | No access outside selected roots; Windows and Linux cases covered | Go race + target filesystem |
| T15 | IS-03 read/create/edit/delete separation and conversion/artifact aliases | Denied operation leaves canary content unchanged; no wrong-root outputs | File/media integration |
| T16 | IS-04 hostile shell/code attempts state/key/proc/socket/network access | Only input copies/scratch reachable; control canaries unchanged/unread | Real isolated Docker workload |
| T17 | IS-04 result export, stale revision, missing scratch file | Only permitted reviewed changes applied; no implicit deletes | Executor + file integration |
| T18 | IS-04 revoke/expiry with background processes and controller failure | New leases denied; stop confirmed or quarantined/pending, never false success | Docker fault injection |
| T19 | IS-05 write/install hooks/plugins/skills/config, then restart | No unapproved import, env passthrough, new server or tool | Pinned Hermes + Docker |
| T20 | CP-10 migration/restart/rollback and previously installed tooling | Reviewed grants only; assets preserved; no legacy permission resurrection | Synthetic migration integration |
| T21 | CP-03/07 each replacement and selected account | Exactly one implementation, expected effects/credentials, native duplicate denied | Real adapter + pinned Hermes |
| T22 | CP-06 egress redirects/DNS/IP changes, private/host/metadata destinations | Policy enforced at actual connection; no default unrestricted outbound path | Network canaries |

## Release gates and evidence limits

- `just check`: race/shuffle tests, own Go coverage >=85%, formatting, static
  checks and documentation/workflow validation. Add meaningful isolation,
  authorization, concurrency and HTTP-contract regressions with implementation.
- `just security` for dependency changes; broker gates if its module is touched.
- `just docker-check` for the pinned baseline, plus T01-T22 evidence relevant to
  each phase. The existing Docker gate is necessary but does not implement this
  new acceptance matrix by itself.
- Two synthetic users plus an organization, both infra owner and secondary
  runtime, dev/prod separation, zero profile and one explicitly granted profile.
- Record source/image digests, policy/inventory revisions, exact test command,
  platform, result and remaining limitations. Do not check in credentials,
  transcripts, deployment state or personal data.
- Real provider mutations, account login and rollout require their own concrete
  user instruction. Until then, label provider acceptance unverified; neither a
  mock nor a research citation qualifies as live integration evidence.

Full acceptance requires all applicable negative paths and positive authorized
flows. If a native or executor boundary fails, keep it disabled and the milestone
open. A documented limitation is not a passing security gate.
