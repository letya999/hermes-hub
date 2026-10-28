# CHG-0211: Knowledge-UI human access edge (M20 stage 8)

Chain: ADR-0086 D9 -> SPEC-0085 R8 -> this plan -> `state.yaml`.
Previous stage: CHG-0210. Next stage: CHG-0212.

## Goal

The owner opens their OpenKnowledge editor through a loopback-published
port plus SSH tunnel — same posture as browser/oauth ports today — with
collision diagnosis and an explicit hook for the future M18 public edge.

## Read before coding

1. `docs/operations.md` port model; `internal/stack/config.go`
   (`oauth_port`, `browser_port` handling); `internal/stack/compose.go`
   publish rendering; CHG-0209 service env.

## Slices

### S8.1 `ui_port` + loopback publish (R8.1, R8.3)

Anchors: `internal/stack/config.go` settings; `compose.go` ports list.

Change:
1. `agent.yaml` `ui_port` (default: next free in the existing per-space
   port scheme) renders `127.0.0.1:<ui_port>:8080` on the
   `open-knowledge` service when `knowledge_ui` is on; matching
   `OK_EXTERNAL_URL=http://localhost:<ui_port>`.
2. Port collisions diagnosed exactly like browser/oauth ports
   (compose/doctor error naming the conflict).

Tests (red first):
- `TestUIPortLoopbackOnly` — publish target is `127.0.0.1`, never `0.0.0.0`.
- `TestUIPortCollisionDiagnosed` — two spaces same port → named error.
- `TestExternalURLMatchesPort`.

### S8.2 Tunnel docs + M18 hook (R8.2, R8.3)

Anchors: `docs/operations.md`; `docs/integrations.md` capability table.

Change:
1. Operations section: `ssh -L <ui_port>:localhost:<ui_port>` flow,
   non-public default, no auth beyond host access — stated honestly.
2. A named edge placeholder in render output/comments
   (`# m18: edge host routing hook`) so the exposure milestone has a
   defined seam, not an archaeology dig.

Tests:
- docs-check passes; link from spaces.md capability table added.

## Red-team checklist

- `ui_port` must not overlap reserved ranges (oauth/browser/slack) —
  validation covers it.
- OK banner "external access enabled, no server-side auth" is expected
  and explained in docs — never silenced by weakening checks.
- No `OK_ALLOW_EXTERNAL` on any non-loopback-published variant in M20.

## Acceptance

A8.1–A8.3: tunnel session opens the editor; second space renders a
different port; `ss`/compose inspect shows loopback-only publish.

## Out of scope

- Real public edge, SSO/OAuth for the editor (M18 milestone).
- Mobile/cloud agent access to `/mcp` (remote-agents story).
