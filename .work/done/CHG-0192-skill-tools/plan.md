# CHG-0192: M24 #360 S5 — ToolHub skill tools + draft approval cards + hubctl

Issue: #360 (milestone 25 / M24.5). Spec: SPEC-0083 R5.*; ADR-0084 D3/D6/D7/D9.

## Goal
Five tools — `skill`, `skills_search`, `skills_list`, `skill_view`, `skill_manage` —
on ToolHub, identical in managed and unmanaged mode, per-call admission + audit,
principal from the authenticated envelope only. Every write produces a draft;
publication is a human confirmation via the protected-link machinery.

## Slices
- S5.1 `internal/agenttools/skills.go` over `internal/skillstore`; dispatch in
  `call.go`; raw-map per-action field validation (foreign keys rejected before
  any mutation); `base_revision` optimistic concurrency on patch/edit;
  cross-principal access = not-found.
- Store additions: `DraftMeta.Action` (publish|archive|restore),
  `AbsorbedInto`, `RestoreStamp`; `PublishDraft` honors non-publish actions;
  `Store.RestoreAt(name, stamp)` for version-picked restore.
- Mounts/env: user store `spaces/<u>/skills` → `/skills-store` (rw),
  org store `<org>/skills` → `/skills-store-org` (rw, org-bound only);
  effective view stays ro at `/skills`. Env `HUB_SKILLS_STORE`,
  `HUB_SKILLS_ORG`, `HUB_SKILLS_EFFECTIVE`. Same in unmanaged compose,
  supervisor runArgs and managedRunArgs.
- S5.2 `HubToolsDefinition()` +5 ToolSpecs (version bump), sections
  `hub:skills` (read) / `hub:skills_manage` (mutate), alias map
  `skills`→`skills`, `skills_manage`→`skills_manage`; `skill.name` enum
  injected at projection from the space's effective index (advisory;
  executor re-validates authoritatively).
- S5.3 communication `POST /v1/skill-drafts` mints a protected confirm
  (pattern: credential_forms.go); page `/skill-drafts/<id>?nonce=` shows
  card (summary/diff/findings/provenance) with Publish/Reject; POST calls
  runtime `/v1/skill-apply` which mutates the store inside the runtime
  container (has `/skills-store` rw). Replay/cross-user fails. Undo link
  re-armed after publish → `Rollback` to base revision.
- S5.4 `propose_to_org` creates the draft in the org store; publish requires
  the approver to be org admin/owner (`/org/scope.yaml` members), checked at
  apply time; author attribution kept in DraftMeta.
- S5.5 `hubctl skill drafts|show|approve|reject|rollback|archive|restore|
  promote` (most exist from M24.2; add `promote` = publish org proposal).
- S5.6 `internal/systemguide/SKILL.md` + `internal/stack/prompt_contract.go`
  document the surface.
- S5.7 `internal/e2e/registry.json` E2E-098 → real cases.

## Red-first tests (issue-mandated)
skill body w/ resource list only; name enum reject; manage→draft not live;
foreign action field reject; stale base_revision conflict; publish requires
owner confirmation; org draft requires org admin; identical managed/unmanaged;
cross-principal not-found; model-supplied principal/org ignored; confirm
replay/cross-user fail; draft cannot self-publish.
