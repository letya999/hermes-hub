# Repository-driven ToolHub

1. Pin each GitHub default branch to an exact commit and review source metadata.
2. Keep prepared connector differences in reviewed catalog data; use one generic Node/Go build, Broker, workload and projection path.
3. Preserve direct URL installation without registries and offer bounded read-only discovery.
4. Verify owner isolation, reinstall, rotation, egress, Hermes reconnect and safe live reads for the four exact sources.
5. Publish evidence to the related issues and run required quality gates.

## Local acceptance, 2026-09-22

- Four exact-source URLs completed isolated generated builds and real MCP initialize/tools/list without registries.
- Broker-backed Notion page, Calendar calendar/event, GitHub identity/repository and GitLab whoami reads passed. No provider payload or credential value entered tracked files.
- Notion reinstall reused its binding. GitLab credential rotation reused the owner connection and passed whoami afterward.
- Google OAuth was explicitly authorized by the owner; Broker checkpointed token state after its writer exited. A live Calendar workload used one read-only credential mount and one writable owner-state mount from the dedicated Broker tmpfs volume.
- Hermes' own MCP test connected to ToolHub and discovered 211 tools, including all four connector families. A controlled restart applied projection revision 42 from the same owner home; the prior session ID remained available. In-flight turn continuity was not exercised.
- Live discovery returned prepared entries for all four sources and four total candidates for GitHub. Broader name-only GitHub search remains issue #110.
- `just check` passed at 85.05% own Go statement coverage. `just docker-check` exited 0; its optional STT fixture remained unavailable in the image. No dependency changed, so `just security` was not required.
- Issue #37 was narrowed to the exact Calendar source; issue #126 was created for Notion. Evidence comments were posted to #115, #116, #110, #74, #118, #40, #37 and #126.

Production rollout is separate issue #116. [Acceptance evidence](acceptance.md) records the local validation limits.
