# #526 live acceptance, 2026-11-07

Source at the start of the local run: `3710949a0ba4742d2d4eaebb48281218c8ea4ee4` plus the #526 worktree diff. The diagnostic Docker image `hermes-hub:0.5.0-prod` was `sha256:098b90767f2a8f8dee812048c270a4c178e01076c89eeafc27d70255ed0a90ff`. It contained temporary checkpoint logging in supervisor; those lines were removed from the source after the diagnosis. Neither image nor run is remote CI acceptance of a clean commit.

`hubctl build` used BuildKit. `hubctl space apply --confirm` published two synthetic users, `zzm32f` and `zzm32g`, from their authored passports. `hubctl up --no-build` started real Compose services. Their managed Hermes contexts started through the real supervisor and Docker daemon. The local model provider was an HTTP fixture; the real model relay processed its requests. This fixture required `allow_private: true` in ignored generated model registries so the relay could reach `host.docker.internal:18888`. An authored-only model provider acceptance remains open.

The first apply exposed a publish/order error: it attempted reconciliation before the generated Compose inputs existed. The fix publishes the first revision without actions; `hubctl up` starts services afterward. A shared `cliproxy` rebuild also used a nonexistent suffixed Compose service; service name resolution now uses the bare shared name. A removed synthetic sibling remained in the enrolled ToolHub bearer set; enrollment now replaces that set from current sibling spaces. These paths have regression tests.

The fixture initially omitted `toolhub-relay` and `egress-relay`, and the supervisor refused to start Hermes until those authored services were published and up. Two old synthetic contexts were left behind by the previous isolated fixture; the supervisor classified them as owned orphans and refused capacity. Only those four named synthetic runtime/control containers were removed, then supervisor restarted. No project-wide Docker prune was used.

Observed live results:

| Check | Result |
| --- | --- |
| Broker `/healthz` over synthetic TLS | HTTP 200 |
| ToolHub MCP projected catalog | 8 projected tools for each synthetic user |
| Unauthenticated MCP | HTTP 401 |
| Memory call without exact run | Denied by ToolHub run-context authority |
| Two concurrent private managed runs | Both streamed `running`, then `completed`; current generation matched |
| `memory_card remember` during each run | Succeeded for both users |
| `memory_search` for own marker | Succeeded for both users |
| Foreign user's bearer with the other run | Denied for both directions |
| Model relay via fixture | HTTP 200 after local fixture transport override |

The `cliproxy` and `communication-hub` synthetic Compose services later entered restart loops because their fixture inputs were incomplete; their initial Compose health is not evidence of sustained service health. The full six-authored-inputs/no-effective-override requirement is not met by this model fixture. No external provider login, message send, or publication was performed.

Still open in #526: confirmed exact edit and stale-base/replay/revoke fences; durable capture under lost acknowledgment/restart and bounded per-user work; group/private concurrency beyond the two private runs; routine/assignment/watch CRUD, claims, pause/revoke, gateway-down execution and deferred delivery; per-user local-day dreaming, recovery, revoke-before-commit and digest drafts. These require further real-container acceptance before #526 can be closed.
