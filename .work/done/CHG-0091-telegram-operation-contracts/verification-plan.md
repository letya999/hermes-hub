# Acceptance design for CHG-0091

Contract: [SPEC-0052](../../../specs/active/SPEC-0052-telegram-operation-contracts.md).
All ADD/UPDATE cases below are planned, not executed. Expectations follow the draft
contract; existing KEEP cases protect accepted predecessor behavior. Production
implementation is absent. Test review by test-critic precedes execution after writing.

## Fixtures and independent observations

Use existing Go tests, httptest servers, temporary os.Root directories/spools,
fake transport boundaries and clock seams. Synthetic PNG/JPEG/WebP encode distinct
shapes/colors. Two file originals have distinct exact byte bodies and independently
computed SHA256. Fake model endpoints decode request data URIs and record headers,
MIME, image count/order and call count; they do not fabricate a vision success claim.

Routine clocks start at 2026-10-13T12:00:00Z. Local API tests authorize a fixture
owner and a different valid owner. Assert forbidden queue/model/provider effects,
not just response strings. Replay operates on a reopened spool, preserving its
single-writer rules. No tests use personal workspace/state, accounts or tokens.

## Core scenarios

| ID / decision | Requirement, setup and action | Independent expected result and fault distinguished | Level / location |
|---|---|---|---|
| T01 ADD | R1: send Telegram photo without file_name; stage/fetch JPEG bytes; inspect | Safe name, identical saved bytes, actual JPEG provider MIME, one call; fails suffix-only intake/inspection | communication media/inbound tests + media tests |
| T02 UPDATE | R1: PNG named .jpg, JPEG without suffix, WebP input to inspect/edit; corrupt header/body case | Actual byte format determines MIME; corrupt input causes zero provider calls; reuse imageFile coverage | media media_test.go, inspect_many_test.go |
| T03 KEEP/UPDATE | R1: two distinct authorized images then a set containing an invalid/foreign member | One ordered combined call for valid set; none for invalid set; unchanged size/pixel/1-8 boundaries | media inspect_many_test.go; agenttools scope tests |
| T04 ADD | R2: render and materialize old hub config; set selected key/config locator and unrelated fake parent key | Required references are explicit; unrelated key is absent; inactive features do not require credentials | stack hermesconfig_test.go |
| T05 UPDATE | R2: real pinned loader reads synthetic home/config and starts hub stdio; call routine and image fixture | Control auth/principal and selected multimodal model/MIME observed at fixture; no manual env expansion bypass | stack hub_stdio_integration_test.go + container native gate |
| T06 ADD | R2: repeat T05 with absent model credential or unresolved config/control reference | Specific unavailable/configuration error; no provider request, no credential/placeholder echo | stack/agenttools/media; Docker fixture |
| T07 UPDATE | R3: tools/list on direct MCP and selected ToolHub surface | Descriptions agree on kinds, regex capture, baseline and prev/current; missing grant still omits/denies tool | agenttools capability/schema tests |
| T08 KEEP/UPDATE | R3: baseline A, unchanged A, B, HTTP 500, regex miss, revoke before next delivery | 0,0,1 logical notification; notification carries A/B; baseline survives failures; revoked effects absent; no agent queue | communication occurrences_test.go and existing M10 tests |
| T09 ADD | R3: clean public URL with surrounding whitespace; URL containing invisible/control character or credentials | Whitespace-only normalization accepted; malformed destination refused explicitly; no hidden destination rewrite | communication validation/HTTP tests |
| T10 ADD | R4: create after:180s at fixed T; tick at T+179s and T+180s | Persist once:T+180s; none early; one logical reminder at deadline; no ordinary job/model; display uses server response | communication schedules/occurrences/HTTP tests |
| T11 ADD | R4: after:0s, after:-1s, after:1.5s, overflow, after:1s, after:8760h, after:8760h1s and invalid after:1d; after on update/other kind | Exact proposed bounds; unsupported combinations fail before mutation; old once/cron remain unchanged | schedules validation tests |
| T12 ADD | R4: exact repeated create after response loss, two concurrent calls, restart and retry after due | One receipt/commit/deadline; replay observes original receipt/current status; no moved deadline or second occurrence | HTTP/spool persistence/concurrency tests |
| T13 ADD | R4: same ID changed text/time/audience, other valid owner collision; update/delete followed by create replay | Same-owner mismatch 409; no foreign receipt; replay never restores paused/deleted state; new tombstone preserves receipt | schedules/HTTP authorization tests |
| T14 KEEP/UPDATE | R4: legacy schedule/tombstone with no receipt; once firing, deletion, overdue, uncertain send | Compatibility retained; duplicate/retired create remains conservative; logical dedup distinct from transport uncertainty | existing occurrence/stabilization and tombstone tests |
| T15 UPDATE | R5: approval A queued; save once or deny while run remains running; separately replace A by B | No actionable A is sent in either sequence; B remains eligible; current terminal-only guard fails this case | communication approval_lifecycle_test.go |
| T16 ADD | R5: queued approval expiry, cancellation, foreign audience, changed current policy or mapping read corruption | No wrong/stale keyboard; corruption is visible uncertainty/error, not CompleteDelivery success; authorization stays intact | approval lifecycle/send tests |
| T17 KEEP/UPDATE | R5: save decision, lose native control response, restart, reconcile; fail cleanup once | Saved != applied; no second execution; receipt survives failure and resolves only exact request after application | control_test.go; approval_messages tests |
| T18 ADD | R5: Telegram keyboard send returns message ID, local receipt write fails, worker/restart recovery | No normal blind resend after known success; durable send boundary/uncertain status retained; no foreign deletion | gateway delivery fault-injection test |
| T19 UPDATE | R6: fresh root artifacts/a.md versus direct documents/b.md; explicit MEDIA for a.md | Only b auto-delivers; explicit a delivers exact bytes; marker stripped; no general workspace scan | runtime artifacts_test.go; instruction contract test |
| T20 UPDATE | R6: attach two original files; fail second send then recover | Original hashes unchanged; exact sent bytes; first acknowledged artifact not resent; second failure visible | communication artifact_test.go; runtime stage tests |

Preserve existing TestReminderOccurrenceDeliversWithoutAgent,
TestWatcherProbeLifecycle, TestInspectManyOneOrderedRequestAndNoPartialSend,
TestScanArtifactsBoundsWindowAndSafety and ownership/revocation suites. A passing
test named for a feature is not its live acceptance. Do not add mirror tests for
every helper or change existing expectations merely to match a faulty implementation.

## Separate album scenarios

| ID / decision | Setup/action | Required observation | Level |
|---|---|---|---|
| A01 ADD | Two photos same group with different message IDs arrive out of order; duplicate update | One ordered trusted group job; no duplicate member; two exact inputs in one model call | communication collector + runtime HTTP integration |
| A02 ADD | Same group ID in other owner/chat/topic/context; restart during collection/admission | Groups remain separate; recovery reuses stable admission/receipts; no foreign fetch/job | spool persistence, authorization, runtime contract |
| A03 ADD | Ninth member, shared byte cap exceeded, failed member fetch, late member after freeze | Explicit bounded failure/late outcome; no truncated comparison, no fileless run, no automatic rerun | collector/runtime failure paths |
| A04 ADD | Quiet window reset by member; hard deadline reached; reopen persisted unfinished record | Admission follows proposed bounds without unbounded timers; incomplete status remains honest | controlled-clock collector tests |

## Agent and actual-channel acceptance

Reuse E2E-020/021 (inspection), E2E-083 (watch) and current routine/file runners;
check the current registry IDs before editing. Add plain-language relative reminder
and current approval sequences instead of substituting exact tool-call instructions
for user intent. Existing routineAgentCase's far-future absolute fixture does not
test the relative-time bug; imageAgentCase's workspace fixture does not test photo
intake. API/status/outbox/native state is the oracle, not assistant prose.

- **Images:** owner sends a synthetic photo normally, then two distinct files and
  requests comparison. Confirm actual tools called and independently known content.
  Actual model recognition PASS requires a real configured model; a fake endpoint
  proves request structure only. Album PASS is separate from explicit file comparison.
- **Watcher:** owner requests a public controlled A/B fixture at one-minute interval;
  change only the chosen capture. Observe baseline silence, one A/B notification,
  no runtime/model tick jobs and no notifications after pause/delete. Local test
  fixtures use the existing injected fetch seam; do not disable production SSRF.
  A public clock API is an optional demonstration, not the reproducible oracle.
- **Reminder:** owner asks for three minutes; independently read receipt/deadline,
  schedule and occurrence count. Observe no early output, one saved text and zero
  agent jobs at firing. Simulate response loss only in synthetic tests. A new
  schedule ID is not a transport retry; disclose this limit in the report.
- **Approval:** exact protected test-file write asks approval; once writes one
  synthetic file, deny writes none; stale/repeated/foreign decisions fail closed.
  Next read-only tree must not resume the previous write. Actual Telegram callback
  and resumed pinned-native run both need evidence.
- **Files:** request two synthetic originals, save received attachments, compare
  hashes, inspect durable per-artifact progress. No personal memory export fixture.

Every run owns random fixture IDs/paths and cleans only them through authorized
operations; no foreign stack or broad Docker/volume cleanup. Missing tool selections,
tokens, public fixture or model are BLOCKED with a reason. Live external sends need
the owner's actual instruction; this design authorizes none.

## Completion and gates

After implementation and test-critic review: focused packages, just check with
>=85% own Go statement coverage, config matrix and built/pulled Docker contracts.
Use existing native, capabilities and network gates; new album HTTP changes require
authorization/concurrency/recovery regressions. Run just security if dependencies
change; none are planned. Keep mock, Docker and live reports distinct and outside
Git when they contain personal deployment data. Update docs only to achieved behavior.

For this design phase, check artifact links/metadata and repository source gates.
Their success does not establish T01-T20/A01-A04 or actual-channel acceptance.
