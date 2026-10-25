---
description: Replace manual diagnostics export with automatic bounded logging.
last_verified: 2026-09-26
---

# CHG-0046 Automatic diagnostics

## Change

- Run a cursor-based Docker log collector in the shared ToolHub by default.
- Keep a 100 MB rolling combined text file in ignored `.local/` host storage.
- Include host supervisor output with a separate 10 MB bounded source file.
- Log HTTP operations in runtime, ToolHub, supervisor, communication and Broker.
- Record authorized Telegram input and delivered text, redacting credential flows.
- Allow `diagnostics: false` to disable the automatic file and message bodies.

## Verification

Run `just check` and Credential Broker checks. Live Docker collection and
recreation require Docker Desktop to be running after compaction.
