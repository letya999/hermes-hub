# SPEC-0031: Automatic bounded diagnostics

Frozen: 2026-09-26. Supersedes SPEC-0030's explicit-export requirement.

1. Diagnostics are enabled by default and can be disabled with
   `diagnostics: false` in the infra-owning space settings. No manual export
   command is required for ordinary collection.
2. Each stack writes one Git-ignored text file under its space
   (`spaces/<user>/diagnostics/hermes-diagnostics.txt`) receiving new
   retained logs only from that stack's containers: the owning Compose
   project label plus supervisor-spawned runtimes on the user's agent
   network. Stopped containers are drained once and marked; every Docker
   call and the whole collection pass are deadline-bound. Host supervisor
   logs are copied only for lines naming the space owner as a whole token.
   A per-container cursor prevents routine replay. The file rolls over at
   100 MB; Docker and host source logs are also bounded.
3. Service HTTP boundaries log method, bounded route and duration without
   request bodies, query strings or credential-bearing URL segments.
4. Authorized Telegram input, including edits, and delivered text replies are
   logged. Unknown senders are not. Credential input and one-time
   credential-form replies are redacted to preserve the existing secret
   handling boundary.
