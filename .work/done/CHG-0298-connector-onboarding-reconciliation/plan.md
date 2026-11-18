# CHG-0298: Connector onboarding reconciliation

## Incident

On 2026-11-11, a GitHub MCP credential form was submitted successfully but
the owner was told that credentials were still missing. The exact onboarding
advanced to awaiting-confirm while its install operation retained
awaiting_user; an older awaiting-confirm onboarding made name lookup ambiguous.
Broker's spent form correctly refused a second POST, which looked like a lost
submission because no fresh Telegram confirmation arrived.

## Change

## Approved follow-up (2026-11-11)

The owner approved the deeper state-machine audit plan. Implement in this order:

1. Make catalog enable idempotent (one profile revision per real change) and
   retain immutable history without rewriting it on every registry mutation.
2. Replace SQLite snapshot rewrites with row deltas, retain integrity and stale
   writer fences, and return the temporary 64 MiB next-write limit to 32 MiB.
3. Enforce onboarding ownership, revision and allowed transition edges at the
   common store boundary. Derive install operation progress from onboarding.
4. Persist notification intent with onboarding; retry delivery after restart.
   Use a separate confirmation action version so delivery metadata cannot
   invalidate a protected approval.
5. Consume the existing Broker event feed with a durable cursor and gap repair;
   status must be a saved read, not an unbounded provider workflow. Isolate slow
   admissions per onboarding, bound calls, and preserve credentials on transient
   failures.
6. Verify crash windows, duplicate/late events, cancellation, two-owner isolation,
   unchanged rebinds and large historical registries. Run affected local gates,
   deploy only changed services and record actual live evidence separately.

No new service, queue or dependency is required. Existing credentials and history
must survive migration. A successful mock is never live integration acceptance.

## Initial incident correction

The durable journal shows status backend errors around 0.3 seconds and two
install handler successes around 9 seconds; it does not attribute the entire
five-minute model turn to ToolHub. The 32 MiB error was observed later during
catalog rebind and is a separate storage defect.

## Earlier partial change

1. Reconcile an exact connector status and repeated install against the Broker
   request before reporting phase. Ambiguous names return exact candidate IDs
   and phases; no automatic selection or deletion of another onboarding.
2. Emit the protected confirmation notice when a Broker credential is accepted.
   Serialize the ready transition so concurrent polling cannot mint competing
   confirmation proofs.
3. Add an explicit write action to resend the current setup link for an exact
   owned onboarding. A resend uses a fresh delivery ID while preserving the
   stable wizard URL and never handles credential values.
4. Update tool description, prompt guidance, docs and immutable catalog; rebind
   and respawn active managed spaces. Verify local regressions and a safe live
   read/status path without confirming or deleting the owner's connection.

## Acceptance

- Form accepted -> one awaiting-confirm transition -> protected Telegram
  confirmation notice; a repeated status reflects that phase.
- Two pending GitHub onboardings by name fail with exact candidates; an exact
  ID succeeds. An explicit resend is a new queued notice with the same wizard
  link, never a new install or credential request.
- Broker failure is surfaced, not converted to success or a fresh token ask.
- No personal credential, auth file or transcript enters diagnostics.

## Implementation and live verification (2026-11-12)

ADR-0098 records the accepted design. Local affected-package gates cover ToolHub,
its API, communication, channels, models, migration, agent tools, catalogs, stack,
supervisor and both CLI entrypoints. A concurrent refresh regression now uses an
explicit read barrier instead of relying on goroutine scheduling.

The native database backup passed integrity verification. A separate backup copy
loaded, converted and reloaded with the identical logical state; the conversion
write took 41 ms. Live catalog rebind preserved onboarding rows, added four policy
and profile history records, then repeated with no digest or history change.
Both managed profiles project release 33 / agent tools 18.3.15. Four nonzero Broker
cursors are durable. Both managed runtimes were actually respawned onto the new
runtime image; an obsolete generated dev-image pin was retained as a rollback copy.

Five sequential calls in a live MCP session returned the saved confirmation state
in 8вЂ“16 ms. Explicit resend succeeded through MCP. The final Telegram outbox record
has a message receipt and confirmation button; no redundant waiting model job was
queued. Live verification also found and fixed missing persistence of ordinary
keyboard receipts. Recent service logs contained no panic, storage I/O failure or
onboarding/removal reconciliation error in the inspected window.

Protected owner confirmation and a subsequent provider read remain unverified;
no model action substitutes for that consent. This change remains in progress
until that acceptance step. Source is dirty and unpublished, so local evidence
does not claim remote CI acceptance. Private diagnostic artifacts remain ignored.

## Follow-up: prompt loading and credential recovery (2026-11-12)

A live request series made zero tool calls. The generated skill catalog cut a multilingual description mid-codepoint; the upstream SOUL reader returned no content, losing the complete channel contract. Repair Unicode truncation, reject invalid materialization before atomic replacement, and verify with the actual upstream loader. Reauth also wrongly depended on enabled/degraded state and could reuse an already completed operation; cover unfinished setup, expired forms, repeated cycles, exact failed targets and inaccessible old Broker references. Expose the existing authorization revocation through the curated edit action and include pending rotation targets. Test fixtures are isolated; real model verification must use a mock delivery channel and read-only connector calls to avoid sending more unsolicited notices.

The follow-up also found a root-render enrollment regression: in-place map rewrites reset their mode to 0600 while consumers run under the agent UID plus operator GID. Preserve the bind inode and write 0640 with the operator group; verify on Linux. Existing maps were repaired during rollout. All affected local package gates passed, and a real model probe with mock delivery called the advertised connector tools. The stable repeat completed without connector writes or Telegram notices. Provider login and remote CI remain separate acceptance steps.

## Live Telegram follow-up (2026-11-12)

The owner submitted a fresh GitHub form but the protected confirmation notice
was rejected by communication-hub with HTTP 403 on every retry. The active
gateway's sibling routing policy still named the previous artem_two revision;
ToolHub's signed actor used the newly published revision. The gateway compared
those versions before queuing the button, even though ToolHub performs the
final current-policy admission at callback time. Accept the signed actor's
policy for this service-authenticated notice while still requiring the same
principal, context and runtime; cover a foreign actor and runtime bearer in
regression tests. The owner-visible `record not found` for two old onboarding
IDs did not reflect ToolHub downtime; an owner-scoped old-policy list/status
regression passes locally. Live delivery and provider read still require
separate verification after rollout.

## Delivery receipt and resend contract (2026-11-12)

The next live resend exposed a second independent defect: `RandomNonce` emits
32 hexadecimal characters, while the gateway validated `link-resent.event_id`
as an identity ID requiring a leading letter. A digit-leading nonce therefore
caused deterministic HTTP 400 and repeated durable retries. The gateway now
uses the nonce format validator from the producer's API package, with positive
and malformed-ID regressions. The repaired gateway queued version 13 and
Telegram acknowledged the new notice.

The previous `link_sent` field only meant gateway queue admission, which hid a
separate uncertain Telegram send. The channel's durable outbox now exposes
the current notice version through an owner-scoped service endpoint;
`connector_status` reports `delivery_status` without URL or credential data.
The model contract requires a confirmed `delivered` receipt before saying a
link arrived, and uses `resend` for an absent link instead of resetting a
submitted credential with `reauth`. Release 36 / agent tools 18.3.18 carries
that result contract. Live receipt and managed projection were verified.

## Routing policy convergence (2026-11-12)

The shared gateway's hand-edited `communication.users.yaml` still named
`artem_two`'s old policy, while the enrolled runtime token and ToolHub profile
used the new one. This rejected the sibling's normal control call with 401.
The gateway now resolves the sibling's policy from the enrolled token map on
each routed request, including Telegram, Slack and mock ingress; unreadable,
mismatched or ambiguous maps disable that sibling route. The primary runtime
and the trusted service peer keep their separate authentication rules. The
automation routing renderer uses the same enrolled policy, and marks an
unenrolled sibling disabled. The operations-plane route also resolves the
current user. After the stale-policy live probe passed, the generated WSL
route and both hand-edited local/WSL files were synchronized to the current
policy; no credential material was changed.

Regression tests cover a stale authored policy, live policy rotation without
gateway restart, org-scoped enrollment, ambiguous enrollment and the existing
service-peer case. Final `just check` and `just test` for communication, stack
and serviceauth passed. A rebuilt native WSL gateway passed Compose readiness;
five live `connector_status` calls returned `delivery_status=delivered` in
34–150 ms, and a real model invoked the status tool through an isolated mock
chat and gave the same answer. The mock did not send to Telegram. An owner
confirmation and successful GitHub provider read are still outstanding.
