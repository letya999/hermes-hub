# CHG-0213: Org knowledge brain — read-only shared OK (M20 stage 10, optional)

Chain: ADR-0086 D8/D10 -> SPEC-0085 R10 -> this plan -> `state.yaml`.
Previous stage: CHG-0212.

## Goal

An organization gets a shared "company brain": one `open-knowledge`
instance rooted at org shared material, exposed to member runtimes
read-only through `read_only_mcp`/`tools.include` — collective knowledge
without granting any user runtime write access to org docs.

## Read before coding

1. CHG-0209/0156 (service + MCP split exist); `internal/stack/scope.go`
   `OrganizationSettings`, `read_only_mcp`; SPEC-0004 org overlay;
   `docs/spaces.md` org mounts (`org:docs` ro).

## Slices

### S10.1 Org OK instance render (R10.1)

Anchors: `internal/stack/compose.go` org-scope service rendering;
`internal/stack/scope.go` org features.

Change:
1. Org `features: [knowledge_ui]` in `scope.yaml` renders an
   `open-knowledge` service on the shared network, volume = org shared
   docs dir (read-only mount), single replica, private only.
2. Org scope validation: `knowledge_ui_write` rejected on org scope —
   org material is read-only to user runtimes, period.

Tests (red first):
- `TestOrgOKServiceRenders` — only when org feature enabled.
- `TestOrgWriteFeatureRejected`.
- `TestOrgVolumeReadOnlyMount`.

### S10.2 Member access wiring (R10.1)

Anchors: `internal/stack/scope.go` `ReadOnlyMCP`; member render path.

Change:
1. Members with org membership get the org OK endpoint as a read-only
   `mcp_servers` entry (tools.include read surface only) named
   `org-knowledge`; deny any write verb at governance layer.
2. Name namespaced so a user-space `open-knowledge` and
   `org-knowledge` coexist without collision.

Tests:
- `TestMemberGetsOrgReadEntry` / `TestOrgWriteVerbDenied`.
- `TestUserAndOrgEntriesCoexist`.

## Red-team checklist

- Member attempts `write` on org-knowledge → deny receipt; the org
  volume itself is mounted ro in the service (defense in depth).
- Org instance must not read user vaults — mount source is the org dir
  only.
- Org feature off → member renders nothing (deny-wins, no dangling
  entries).

## Acceptance

A10.1–A10.2: member runtime finds org docs via `search`/`exec`; every
write verb denied at governance layer; org volume ro in compose.

## Out of scope

- Org-side human editing workflow (org admins edit the shared dir on
  host for now; an editing surface is a later decision).
- Per-member org content ACLs beyond read-all-org-docs.
