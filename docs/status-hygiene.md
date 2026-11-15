---
description: Documentation freshness contract — status truth, last_verified discipline and the per-PR bookkeeping checklist.
last_verified: "2026-11-15"
---

# Documentation status hygiene

Documentation that disagrees with the codebase misleads every future session.
This page is the normative contract for keeping records honest. `devcheck docs`
(part of `just check`) enforces the mechanical half; the content half is on the
author of each change.

## The contract

1. **Statuses reflect the codebase, not the plan.** A CHG is `done` only when
   its code is merged. An ADR is `accepted` only after owner acceptance.
   A spec status column entry says what is true today (`Frozen`,
   `Superseded`, `delivered`, `implemented`, `Draft`), not what was hoped.
2. **`last_verified` means verified.** Set it to the date you actually
   re-checked the document against the code — normally the PR merge date.
   Bumping the date without reading is a violation; the
   `verifiedDriftAllowlist` in `internal/devcheck/devcheck.go` exists only
   because real re-verification is still owed on those files.
3. **Merge day = bookkeeping day.** On the same commit or PR that lands work:
   - every finished change moves `.work/in-progress/CHG-*` → `.work/done/`
     with `status: done`, `phase: done`, `updated: <merge date>` and an index
     row under the matching Done section;
   - work that is blocked, partial or deferred does **not** get marked done —
     park it under `.work/done/` with `status: deferred` and a `deferred:`
     date, and move its index row to "Deferred / parked";
   - ADR/SPEC index statuses are re-checked (`proposed` → `accepted` once
     accepted/delivered, `superseded` when replaced);
   - `last_verified` is bumped **only on documents whose content you
     re-verified in that PR**; editing a living doc obliges re-verifying it.
4. **Never edit a doc you did not check.** If you cannot re-verify a document
   in this PR, do not touch it — flag the stale part in the PR description
   instead.

## Status vocabularies (closed sets — do not invent new words)

| Record | Allowed statuses |
|---|---|
| CHG `state.yaml` | `in_progress`, `done`, `deferred`, `abandoned` |
| ADR index row / `Status:` line | `accepted`, `proposed`, `answered`, `superseded`, `partly superseded` — always with the successor or delivery marker when applicable |
| SPEC index row | `Active`, `Delivered`, `Draft`, `Superseded → SPEC-NNNN`, `Partly superseded → SPEC-NNNN` — supersession always names the successor spec explicitly |

## Traceability

Requirement chains must never dead-end: `devcheck docs` rejects any
`ADR-NNNN`/`SPEC-NNNN` reference in a CHG `state.yaml` or in the ADR/SPEC
indexes that does not resolve to a real file. When you supersede a record,
name the successor (`Superseded → SPEC-NNNN`, `superseded by ADR-NNNN`) and
add an `Amended by`/`Amends` back-pointer to the older record so agents can
walk the chain in both directions.

## What each record type means

| Record | Status source of truth | Date rule |
|---|---|---|
| `.work/CHG-*` | `state.yaml` `status`/`phase` + index section | `updated:` = last real work on it |
| `docs/adr/ADR-*` | index row prefix + `Status:` line in body | `last_verified` freezes at acceptance — it is the record date, never bumped |
| `specs/active/SPEC-*` | Status column in `specs/index.md` | `last_verified` = last content re-verification; `frozen:` stays |
| `docs/*.md` living docs | content itself | `last_verified` ≥ last commit touching the file (gate-enforced unless allowlisted) |

## Gate checklist for a release / merge PR

- [ ] `.work/in-progress/` contains only genuinely active work
- [ ] moved CHGs keep working links (`in-progress/` → `done/` inside `](...)`)
- [ ] `specs/index.md` and `docs/adr/index.md` statuses match delivered state
- [ ] every edited `docs/*.md` has `last_verified` = merge date and was
      content-checked against the code it describes
- [ ] `go run ./cmd/devcheck docs` is green

## Reopening a parked record

Move the folder back to `.work/in-progress/`, restore `status: in_progress`,
remove the `deferred:` line, and move its index row back under "In progress".
