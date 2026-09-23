# Live evidence and remaining acceptance

These are local acceptance results for CHG-0036. The code has not been committed or deployed to production.

| Requirement | Current evidence |
|---|---|
| Four exact repositories, generated recipes, no registries | Live isolated build and initialize/tools/list passed for all four; private review receipts in .local/prepared-acceptance. |
| Discovery | Live ToolHub discovery returned a prepared result for each source; the GitHub query returned four total candidates. Owner-bound selection and five-candidate cap have regression coverage. Broader name-only GitHub search remains in issue #110. |
| Notion safe read | Broker-backed API-post-search plus API-retrieve-a-page passed; response validated in memory without logging private page content. |
| Notion reinstall | New request key reused bind-629f1516df5e1e2b3960f1a7; owner connection count stayed one. Snapshot binding count unchanged (includes stale legacy version). |
| GitHub safe reads | Broker-backed get_me and get_file_contents of this repository's README passed. Embedded resource forwarding regression passed. |
| GitLab | VPN enabled by owner. Real whoami passed, then protected Broker rotation retained the same owner connection at revision 3 and whoami passed again. Cold MCP startup also passed after generic readiness fix. |
| Calendar | Owner-authorized Google OAuth completed with the existing client. Broker checkpointed the owner token state. Real ToolHub `list-calendars` and `list-events` both passed without exposing response content. |
| Isolation | Automated mount-path/mode, resource-owner authorization, credential/binding and discovery ownership regressions. During a live Calendar call, Docker showed two mounts from the owner Broker tmpfs volume: one read-only credential file and one writable state directory. A second user's live provider account was not used. |
| Hermes reconnect | The API `/v1/runs` false positive was removed. The Go runtime watcher now schedules a controlled Hermes restart from the same owner home and records applied revision to avoid loops. Live revision 42 applied, Hermes gateway PID changed inside the same runtime container, the old owner session ID still returns 200, and runtime health returned healthy. Hermes' own `mcp test toolhub` connected, discovered 211 tools and listed all four projected connector families. This proves cold reconnect and fresh Hermes tool discovery, not preservation of an in-flight turn. |
| Gates | Final `just check` passed with 85.05% own Go coverage. Docs check and `git diff --check` passed. Targeted Opengrep scan of the changed Go packages passed. Final `just docker-check` exited 0 after stack volume changes; the optional `hub-stt` fixture remains unavailable in the shipped image. |

The deployment override and rollback metadata are private under
.local/prepared-acceptance. No provider content, credential value, token state or
browser profile belongs in this evidence file.
