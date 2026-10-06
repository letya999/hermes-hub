# SPEC-0041: Default-deny capability policy

Status: accepted and frozen requirements baseline, 2026-10-02. Issue 122.
Implementation progress is tracked in CHG-0065; subsequent requirement
changes require an explicit successor/amendment. These requirements describe the
target, not the behavior currently shipped by SPEC-0040.

Decision: [ADR-0031](../../docs/adr/ADR-0031-default-deny-capability-policy.md).
Isolation/proof: [SPEC-0042](SPEC-0042-capability-isolation-and-proof.md).
Execution plan: [CHG-0065](../../.work/in-progress/CHG-0065-capability-enforcement/plan.md).

## Requirement precedence

On adoption, replace SPEC-0040's availability/default matrix, static four-input
catalog, multiple direct delivery surfaces and restart-only native revocation.
Replace SPEC-0023's default-allowed self-install and blanket control-tool exposure.
Narrow the unrestricted self-env/self-service paths of SPEC-0005 and SPEC-0008.
Preserve stable identity, exact-owner bindings, credential brokerage and the
bounded handlers in SPEC-0010, SPEC-0017 through SPEC-0020, SPEC-0025 and SPEC-0033.
SPEC-0032 SSH remains opt-in; its commands and tunnels must obey this common gate.

## CP-01: Zero baseline and profile resolution

Absent, empty, malformed, expired or unverifiable authorization must never mean
all tools. A fresh user has zero agent capabilities. No file, skills, diagnostic,
search, memory, lifecycle, install or terminal exception is implicit. An invalid
policy fails readiness/admission; it does not silently fall back to defaults.
Essential harness operation (reasoning and replying through the bound channel) is
not a grant to read/send through arbitrary channels or invoke provider tools.
Automatic memory loading/consolidation, speech/vision preprocessing, scheduled
scripts and provider fallback do not escape the policy by running without a
visible tool call. They need their own granted capability or remain disabled.

The operator supplies an organization ceiling and an organization default profile.
The default grants are inside the ceiling. User grants may add capabilities inside
the ceiling; user restrictions subtract them. Standalone users have an
operator-managed personal ceiling/default with the same empty starting state.

For an operation, evaluate the complete tuple of authenticated principal, context,
runtime generation, environment, capability, selected implementation revision,
action, resource, connection and limits. Authorization requires a matching
unexpired allow inside every applicable boundary and no matching explicit deny.
Do not combine an action from one grant with a resource from another to manufacture
a broader grant. Missing membership, revision or ownership fails closed.

Organization defaults are applied per user; they never share state or credentials
implicitly. A membership/default/ceiling change increments the effective revision
and follows CP-09. Dev and prod identities/state are distinct even for one user.

## CP-02: Complete inventory and admission

Maintain a generated inventory for the exact Hermes source pin, image digest,
approved extension revisions and admitted connector definition revisions. Inventory
is not the subset visible with today's credentials or installed optional packages.
Record unavailable/conditional entries too. Inspect trusted registry/source
metadata and actual session schemas; do not execute unreviewed packages in the
control process just to discover them.

The inventory covers:

| Surface | Required treatment |
|---|---|
| Native leaf tools and composite/platform/posture toolsets | Resolve membership to exact leaves; record aliases and conditional registration; reject cycles/ambiguity |
| CLI, API server, channels and background runs | Explicit profile on every supported entry; untested entry points disabled |
| MCP tools and bridges | List/search/describe/call, resources/templates, prompts, sampling/elicitation and task methods reviewed separately |
| Local hub and ToolHub control tools | Every operation inventoried, including diagnostics, lifecycle and generic invoke |
| Skills and extensions | Native/bundled/org/user skills, scripts, plugins, hooks, context engines, memory/model/media providers and command-backed configuration |
| Indirect execution | Delegation, code execution, browser scripting, cron/routines, SSH, package/skill/plugin installers and child processes |

An update creates an inventory diff. Newly discovered tools, group members, effects,
schemas, descriptions or implementations receive no old permission automatically.
Unreviewed additions are quarantined. A changed existing definition suspends that
binding until review; unrelated unchanged bindings may continue. A mismatch between
the permitted final schemas and the actual outgoing agent surface fails run
admission before disclosure or invocation. Never equate a zero tool count in a
credential-free probe with a complete inventory or a security proof.

## CP-03: Canonical identity, replacement and naming

Extend existing definition/binding records with the minimum information needed for:

| Field/concept | Meaning |
|---|---|
| Capability ID | Stable logical operation, independent of server/tool display name |
| Implementation reference | Exact native adapter or reviewed hub/connector definition revision and digest |
| Operation/effects/resources | Reviewed actions, scopes, destinations and bounded argument constraints |
| Presentation | Unique MCP-safe tool name and reviewed schema/description for the selected implementation |
| Group revision | Explicit immutable list of capability IDs; organizational convenience, not authority itself |
| Policy/binding revision | The current authorization and selected implementation used for this call |

Use clear operation-oriented names, for example `workspace_read`, `workspace_edit`
and `github_issue_read`; these are target naming examples, not existing APIs.
One logical capability has one selected implementation per effective profile.
Reject ambiguous names or aliases instead of choosing a backend by discovery order.
An explicitly selected second account is a scoped connection, not an accidental
native/MCP duplicate. Implementation changes require review and a new binding
revision; same-name tools never inherit authorization solely by name.

Effects and constraints come from reviewed definitions, not self-reported MCP
`readOnlyHint` or descriptions. A generic command/SQL/browser tool is not considered
read-only merely because of its name. Use downstream read-only identities and
argument/resource restrictions when those are required to substantiate the grant.

## CP-04: One policy, projection and dispatch

Use the existing Go ToolHub as the single managed MCP endpoint for each runtime.
The authenticated transport determines identity; arguments cannot choose owner,
policy, host path, credential locator, administrative role or arbitrary backend.
Agent credentials have no operator authority. A session identifier is not proof of
identity, and cached authentication does not replace current authorization.

Only authorized capabilities appear in tools/list, search, describe, resources,
prompts, skill recommendations, generated instructions and agent diagnostics.
Unknown and forbidden lookup responses must not reveal a hidden catalog entry.
Already received history and model pretraining cannot be erased by revocation;
subsequent system-generated catalogs and guidance must omit the revoked capability.

Authorize every call before dispatch and credential injection. Apply the same
decision to direct calls, the generic invoke path and every composite subcall.
Approval of a composite does not authorize its otherwise forbidden children.
Unsupported protocol operations are rejected, not forwarded opportunistically.
Server-initiated sampling/elicitation and async tasks cannot acquire extra authority.
Capability transport metadata needed for an empty MCP session is permitted; no
business or diagnostic tool is implicitly exposed by that exception.

Native code may be selected only after the compatibility gate proves an equivalent
dispatch check, resource boundary and revoke behavior on the pinned Hermes. A
configuration-only or in-process visibility check is not that proof. If no supported
upstream seam can establish it, keep the native path disabled and use a managed
implementation or an explicitly reviewed upstream change.

## CP-05: Grants, user control and consent

Administration is a human-authenticated host/control operation, outside the agent
MCP token's authority. Record issuer, subject, organization, action/resource scope,
implementation/revision, issue/expiry times, reason and grant status. Reuse current
stable IDs and grant storage rather than inventing another identity system.

The agent cannot grant, widen, disable or revoke its own or another user's rights.
Submitting a request for access, if offered, is itself an opt-in capability and has
no effect until the human control flow completes. Human-driven narrowing is allowed
through that flow; returning a model-generated `authorized=true` is insufficient.

Installation, update, removal, activation, deactivation, configuration edits and
connection/credential lifecycle are distinct grants. None exists by default.
Optional self-service requires a current explicit grant plus authenticated human
confirmation bound to the exact requested change, principal, resource, digest,
expiry and one-time nonce. Changed arguments invalidate confirmation. Credentials
are entered only through protected broker/OAuth flows, never through the model.

A configuration grant allows validated changes within the user's ceiling. It does
not allow arbitrary YAML that injects shell hooks, changes MCP endpoints, enables
native tools or exposes secrets outside the selected policy. The human operator
may publish a new approved configuration; the agent never edits the effective file.

## CP-06: Credential and resource authority

Reuse broker delivery after authorization. Select the connection server-side and
require exact-owner binding or an explicit shared-credential policy. A shared
credential does not imply shared workload state. Runtime identity credentials,
operator credentials and upstream provider credentials are different authorities;
do not forward the incoming MCP bearer to a provider.

Grant network destinations, file roots and expensive-work limits explicitly.
Provider availability, an environment variable, login state or credentials in a
catalog never grants use. Read access to private data plus external delivery can
enable exfiltration; policies must constrain both access and delivery destinations.
Do not claim information-flow prevention merely from independently granted tools.

## CP-07: Initial mapping and optional organization bundle

All rows start disabled. This table states intended routing, not automatic grants.

| Capability family | Target decision |
|---|---|
| Files, documents, image conversion, artifacts | Scoped hub executors; separate read/create/edit/delete and scope grants |
| Skills | Separate list/view and install/update/remove; approved immutable content; scripts need their execution grants |
| Terminal, execute_code, delegation, SSH | Separate grants and SPEC-0042 isolation; children inherit a subset, never broader authority |
| Web/deep research | Reviewed managed web implementation; disable native duplicate; reconcile with issue 123 before integration |
| Browser | One reviewed managed route; distinguish navigation/data exposure/actions/script execution and account state |
| Cron/routines | Hub owns schedules; reauthorize on each wake; native cron may be selected only after equivalent proof |
| STT, vision, image generation/edit | Reviewed hub routes; implicit input preprocessing and outbound artifacts are covered too |
| Todo/tasks | Disabled until a selected native or connector-backed implementation is reviewed; issue 183 retains channel checklist/provider choices |
| Memory and session search | Disabled; explicit per-user/context access and provider review before enablement; issue 184 |
| Meet | Disabled everywhere until explicit admission/grant |
| HH | Remove from core/default feature semantics; optional separately reviewed connector, no embedded business service |
| Diagnostics and connector lifecycle | Explicit grants; operator diagnostics remain available outside the agent projection |
| Model/CLIProxy configuration | Human control flow only; organization/personal/mixed routing remains issue 185; no agent self-switching |

Organization bundle candidates from the owner's request: GitLab, GitHub, Atlassian,
Slack, personal Telegram, one of txttsql/dbhub, Grafana, Prometheus, DataLens,
Google Calendar and Notion. Later candidates: Metabase, Sentry, Google Analytics,
Yandex AppMetrica/Metrica, Gmail, Drive, Docs and Figma. A recommendation is not a
trusted definition or grant: each needs an exact source/digest, real API evidence,
reviewed effects/egress, broker contract and per-user binding. Do not install both
SQL alternatives by default. Catalog discovery is itself profile-filtered.

## CP-08: Children and automation

A child agent, script, background job, routine or composite receives only the
intersection of parent authority, current user policy and its explicitly requested
subset. Store identifiers/revisions, not credentials or frozen reusable permissions
in schedules. Reauthorize when a queued job starts, each subcall dispatches and a
lease renews. Cancelled/revoked generations cannot acquire fresh executor leases.
Skill content, retrieved documents and provider output are untrusted inputs, never
approval evidence or a source of capabilities.

## CP-09: Revocation and audit

Persist grant/binding changes with the existing concurrency fence and increment an
authoritative monotonic revision before reporting admission revoked. Every later
admission must observe that revision; unavailable policy state denies the call.
Update projections/notifications promptly, but do not depend on client refresh for
security. An in-flight call admitted earlier may already have dispatched an
irreversible remote effect: request cancellation and report its actual/unknown
outcome. Do not automatically replay mutating calls after an uncertain result.

For local code workloads, distinguish `admission_revoked` from
`execution_stopped`; stopping/reconciliation follows SPEC-0042. A response must not
claim complete revocation while a previously admitted workload is unconfirmed.

Log policy/grant/implementation changes and admitted/denied/completed calls with
issuer/subject, scope, IDs, revisions, correlation ID and redacted reason/outcome.
Never log raw credentials, private arguments, file contents or transcripts.
Call authorization (including sensitive reads) and its durable admission record
share a recoverable transaction boundary; audit failure cannot silently permit
an unaudited new call or grant. Agent workloads cannot edit this ledger. An out-of-band operator
may stop execution during a storage failure; recovery records that intervention.

## CP-10: Migration and acceptance

Migration produces a human-reviewable old-to-new capability/grant/implementation
diff and quarantines unknown entries. Existing installs, credentials and old
self-service flags do not auto-create permissions. Preserve workspace/history;
do not delete assets just because access is removed. Publish the new profile only
with matching runtime configuration, inventory and executor revision. Old runtime
generations are drained/fenced before activation.

Rollback may restore the previous proven managed revision or stop the managed
runtime. It must not silently restore legacy always-on/self-install permissions.
Legacy deployments remain explicitly outside this guarantee until migrated.
Acceptance is the traceable test matrix in SPEC-0042; passing render tests or an
aggregate coverage threshold alone is not acceptance.
