# Issues #39 and #115 acceptance (2026-10-07)

This record distinguishes live behavior from tests. No account secrets, phone
numbers, message content, or session material are retained here.

## #39 — personal Telegram

| Criterion | Evidence | Result |
| --- | --- | --- |
| Selected source, runtime recipe, Broker handoff and direct GitHub path | `internal/toolhub/prepared/catalog.json` pins `letya999/telegram-mcp` at `26f1632`; bare repository URL selected that prepared entry in two live owner onboardings. Restricted build and authenticated MCP admission completed. | Passed |
| Fresh login without chat secrets | The Communication Hub QR page completed a fresh owner login and transferred its StringSession to the reviewed Broker request. Bot chat carried only the local invitation. | Passed |
| Bot and personal credentials separate | The bot transport uses its own token; the personal connector uses Broker contract `telegram-session`, API ID/hash, StringSession and expected numeric user ID. | Passed |
| Per-owner workload and state isolation | Two live owners completed `list_accounts`; one owner was denied the other's onboarding. Their running MCP workloads used separate Docker networks and private `/run` and `/tmp` tmpfs, with no shared writable mounts. | Passed for the StringSession mode |
| Read-only default and separate writes | The prepared source fixes `TELEGRAM_EXPOSED_TOOLS=read-only`. The separately granted account adapter sent a self-message, replied and deleted both messages on the owner-approved test account; Telegram returned message IDs and deletion pts receipts. | Passed through the explicit write adapter; prepared Broker binding itself is read-only |
| Restart and reconnect | After the dev supervisor and gateway were restarted, the existing binding stayed enabled and the bot completed live account and history requests. | Passed |
| Revoke, reinstall and remove | On the test owner, revoke hid the old tools and denied calls. A new Broker onboarding enabled reads again. Removing the revoked installation left the replacement working. | Passed |
| Upgrade to another reviewed source pin | A Go regression proves a version change revokes the previous binding/grant, admits the new binding and retains the previous definition. A live pin change has **not** been demonstrated: remote HEAD remains `26f1632`; an older unprepared commit failed `tools/list` before authentication and was removed. | Open |

## #115 — prepared bundle

| Criterion | Evidence | Result |
| --- | --- | --- |
| Twelve stable IDs discoverable | The running ToolHub returned nine `prepared` and three `blocked` bundle IDs through `discover`; `sql` resolved through its reviewed alias. | Passed |
| One operation starts generic onboarding | `prepare_source` with the Telegram repository URL entered the normal review, build, Broker, preflight, confirmation and owner-binding path for two live owners. Other ready entries use the same catalog-backed path in tests. | Passed for the contract; provider login was tested live for Telegram |
| Immutable, reviewed manifests | All ready entries have exact pins, licenses, Broker contracts, runbook links and handoffs. Catalog validation and the source/build tests passed. The three unavailable entries state missing evidence and offer no selectable prepared candidate. | Passed |
| Owner-scoped credentials and state | Telegram live two-owner isolation and the generic two-owner Docker workload gate passed; stateful entries select per-owner workloads. | Passed at the tested boundaries |
| Upgrade and rollback | The pinned-version regression retains the prior definition and cuts its binding before admitting the replacement. The operator runbook requires an explicit new pin and retained review evidence. | Tested; live Telegram pin upgrade remains open under #39 |

## Operating limits

- The prepared Telegram entry accepts StringSession. SQLite session-file import is
  not integrated with Broker state; the QR page creates a fresh StringSession.
- Broker revoke stops local access. Terminate the Telegram device separately in
  Telegram Devices to invalidate that authorization at Telegram.
- Local source-built Docker gates passed. Pulling the prebuilt GHCR image was not
  part of this branch's live acceptance.

The next acceptance step is an explicitly reviewed, distinct Telegram source
revision. Install it on the test owner through the same Broker flow, verify the
new binding and provider read, then confirm the former version is denied and its
artifact/review evidence remains available for rollback. Keep the main owner on
the current pin until that sequence succeeds.
