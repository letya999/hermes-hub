# CHG-0289: Fast local verification and CI handoff

## Goal

Restore a small local edit loop while preserving all full-suite, coverage,
security and container acceptance in GitHub Actions. Owner requested this
on 2026-11-06 after auditing eight Codex and eight Devin sessions.

## Scope

- Local `just check`: format, docs, vet; explicit affected-package `just test`.
- Full local fallback `check-full`; remote race/coverage gates remain mandatory.
- Default Docker verification consumes exact-HEAD CI images; explicit build fallback.
- Refuse missing/mismatched images before retag; independent reusable CI role caches.
- Diagnose Windows process launch refusals without disabling host protection.
- Prepare small Devin work stages in the existing three worktrees.

## Acceptance

- Fast gate and affected devcheck tests pass; timings and scope recorded separately.
- CI YAML/actionlint preserves coverage floors, race/shuffle and all container gates.
- Missing/stale/uninspectable image never replaces the local verification tag.
- Source commit and CI run URL required for remote acceptance; unpublished code is pending.
- Probity remains unchanged; no development Docker deployment is modified.

## Non-goals

M32 product changes, new worktrees, provider calls, antivirus exclusions/removal,
implicit publication, or claiming that local green proves remote/image acceptance.

## Evidence

See state.yaml and devin-handoff.md. Diagnostic logs remain ignored under
`.work/tmp/quality-audit/`; do not commit transcripts or personal data.
