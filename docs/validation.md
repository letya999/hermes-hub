---
description: What counts as evidence, and the standing list of what is not yet proven. Dated per-change evidence lives in .work/done/CHG-*/evidence.md.
last_verified: 2026-11-07
---

# Validation policy and acceptance boundary

## What counts as evidence

- **Unit/race tests** prove code behavior only; a fixture or httptest double
  is never a provider call, a live account, or delivery on a real channel.
- **Docker gates** (`just docker-check`, `hermes-contract` probe) prove the
  image + pinned Hermes HTTP contract; they use fixtures, not providers.
- **Live evidence** means a real account/workspace/device with a real
  provider call and a recorded artifact (message id, resource id, receipt).
  "Verified locally" without that wording means fixtures only.
- Record `just check`, affected test results and CI separately, with the tested
  SHA and run URL. Local format/docs/vet does not prove full-suite or coverage
  acceptance. Unpublished code has no remote gate result. Historical gate
  runs with exact image ids live in `.work/done/CHG-*/evidence.md` — this
  file keeps only the *standing* boundary of what remains unproven.

## Standing unproven boundary

Things a reader must NOT assume work until separately proven live:

- **M32 #526/#527** — the existing gateway/managed-supervisor Docker canaries
  use a ToolHub stub. They do not prove integrated memory capture, automation
  and dreaming under faults. The M32 gate now has a real ToolHub startup,
  managed-runtime isolation and capture retry leg, but its container execution
  is still unverified. It now includes actual private/group prompt checks,
  a dedicated group home and open-session tools.use/memory.search/memory.remember
  revocation/regrant fences. These additions compile but have not run in Docker.
  Startup/recovery, reminder clock during a communication
  outage, deferred fixture delivery, pending-result pause/revoke fences and
  assignment source reconcile/CAS/pause/resume/removal recovery passed without
  Docker. Dreaming also passed the real relay/PDP/vault path for UTC/New York,
  a spring DST gap, admission/commit-time right revoke, owner independence,
  host restart, unchanged completed-day Git HEAD, draft-only intents and
  deferred local channel delivery. Its clock and upstream model are controlled.
  Regression tests also deny mismatched/missing profile revisions and an
  incomplete apply, including one started after the model response.
  Process-crash, memory-disable/config-edit and autumn DST overlap acceptance
  remain open. Group/memory-revoke/crash, due agent/assignment/watch
  work and final-source CI remain required. Current scoped
  live evidence is recorded in CHG-0288.
  Cold managed file create/read/remove now passed for both existing owners
  after the runtime-lease fix; supervisor status confirmed idle with zero
  leases afterward. This does not prove the full watch recovery/fault matrix.
  Distinct-bearer confirmation notice/callback and managed-binding regressions
  passed. A real gateway/ToolHub/storage test also passed primary/sibling
  runtime principal isolation under a forged header. The real MCP/gateway/writer
  chain also passed owner/foreign/self/stale/replay/revoke confirmation, durable
  callbacks across gateway restart and ToolHub startup recovery after content
  landed before Git commit/receipt. Telegram transport and live-run authority
  are HTTP fixtures there; abrupt process-kill and container execution remain open.

- **#74 full lifecycle** — tool list changes reach an open MCP session via
  `notifications/tools/list_changed` (wire + pinned-Hermes fixture proven);
  production delivery, every failure/recovery case and a live provider
  account remain open.
- **#110 unprepared registry install** — a live unprepared *github-search*
  result completed the generic install; an Official MCP Registry or
  marketplace candidate has not.
- **#73 ToolHive/VPS** — production-pinned ToolHive sidecar/resource
  enforcement on Linux VPS is unmeasured; stock ToolHive lacks all required
  create-time security flags (docker_fallback is the working path).
- **Live provider logins** — Google Workspace remote MCP (needs Developer
  Preview Cloud project), Slack workspace login, Jira/Confluence tokens,
  real Telegram Bot delivery: fixtures only, no real account verified.
- **Telegram account MCP** — a pinned Telethon workload passed a live
  self-message send/reply/delete on an owner-approved account; the read-only
  prepared Broker entry path and provider-side session termination are not
  proven.
- **SSH** — tool surface proven against a real OpenSSH container; a live
  agent call on a real deployment + production broker audiences for
  `ssh-<alias>` bindings remain open (issue #42 acceptance).
- **Voice** — STT/TTS sidecars verified e2e on the live stack once
  (silero→whisper round-trip); routine Telegram voice delivery unproven.
- **SQL connectors** — each stage proven separately (recipe, Squid ACL,
  companion forward, real postgres SELECTs through CONNECT); end-to-end
  admission on a real stack in one shot is not proven.
- **hub-media video** — durable job + provider-poll resume are tested;
  a real fal/remote provider video job is not exercised.
- **vMCP session continuity** — ToolHive v0.48/0.49 vMCP returned
  `404 Session terminated` on session-header reuse; recorded unavailable,
  not claimed.
- **ADR-0031/32** — default-deny capability boundary is accepted; all
  T01–T22 rows evidenced in CHG-0065 pending owner acceptance. The
  per-guarantee enforced-vs-target delta lives in
  `.work/in-progress/CHG-0065-capability-enforcement/acceptance-matrix.md`.

## Per-change evidence

| Change | Evidence |
|---|---|
| CHG-0055 SQL connectors | [.work/done/CHG-0055-sql-connectors/evidence.md](../.work/done/CHG-0055-sql-connectors/evidence.md) |
| CHG-0052 document/image | [.work/done/CHG-0052-document-image-tools/evidence.md](../.work/done/CHG-0052-document-image-tools/evidence.md) |
| CHG-0047 SSH | [.work/done/CHG-0047-ssh-capability/evidence.md](../.work/done/CHG-0047-ssh-capability/evidence.md) |
| CHG-0037 ToolHub transport | [.work/done/CHG-0037-toolhub-closeout/evidence.md](../.work/done/CHG-0037-toolhub-closeout/evidence.md) |
| CHG-0026/SPEC-0023 control plane | [.work/done/CHG-0026-user-binding-journey/evidence.md](../.work/done/CHG-0026-user-binding-journey/evidence.md) |
| CHG-0027/SPEC-0024 onboarding | [.work/done/CHG-0027-m53-e2e/evidence.md](../.work/done/CHG-0027-m53-e2e/evidence.md) |
| CHG-0061 voice | [.work/done/CHG-0061-voice-readiness/evidence.md](../.work/done/CHG-0061-voice-readiness/evidence.md) |
| CHG-0023 assistant | [.work/done/CHG-0023-personal-assistant/evidence.md](../.work/done/CHG-0023-personal-assistant/evidence.md) |
| CHG-0022 credentials | [.work/done/CHG-0022-credentials-connector-platform/evidence.md](../.work/done/CHG-0022-credentials-connector-platform/evidence.md) |
| CHG-0015 job mappings | [.work/done/CHG-0015-durable-job-runtime-mappings/evidence.md](../.work/done/CHG-0015-durable-job-runtime-mappings/evidence.md) |
| CHG-0012 Hermes contract | [.work/done/CHG-0012-hermes-api-contract/evidence.md](../.work/done/CHG-0012-hermes-api-contract/evidence.md) |
| CHG-0014 threat model | [.work/done/CHG-0014-threat-model/evidence.md](../.work/done/CHG-0014-threat-model/evidence.md) |
| CHG-0015 migration map | [.work/done/CHG-0015-migration-map/evidence.md](../.work/done/CHG-0015-migration-map/evidence.md) |
| CHG-0016 scale-to-zero | [.work/done/CHG-0016-scale-to-zero-runtimes/evidence.md](../.work/done/CHG-0016-scale-to-zero-runtimes/evidence.md) |
| M1 compatibility | [.work/done/CHG-0018-communication-runtime-migration/evidence.md](../.work/done/CHG-0018-communication-runtime-migration/evidence.md) |
| CHG-0019/0020/0021 ToolHub M2 | `.work/done/CHG-001{9,20,21}-*/evidence.md` |

## Hosting evidence boundary

Store/HTTP/controller contract tests exercise real Go boundaries with synthetic data. They do not establish runsc/Kata resource enforcement, actual volume-subpath support, sish TLS/reconnection or browser CSP/cookie behavior. Those remain separate native acceptance checks; issue #147 cannot close from static-only or mocked executor success. See [hosting](services/hosting.md).
