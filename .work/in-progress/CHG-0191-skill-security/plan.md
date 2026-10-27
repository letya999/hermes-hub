# CHG-0191: Skill security pipeline (M24.4)

Chain: `specs/active/SPEC-0083-agent-skills-platform.md` R4.* -> this plan ->
`state.yaml`. Issue: #359. Decision: ADR-0084 D6/D7/D11.

## Goal

Every tree entering a store — published or drafted — passes one deterministic
scanner. Severity-leveled findings (file/line/rule, never content), sticky
taint on drafts, a publish gate that refuses critical findings, quotas and an
audit trail without skill bodies.

## Slices

- S4.1 `internal/skillstore/scan`: rules for secrets, download-and-execute,
  exfil hints, destructive shell, privilege escalation, hidden unicode/bidi,
  long base64 blobs, instruction hijack, hub-specific paths (`HUB_TOOLHUB*`,
  broker/credential paths, `org_actions`).
- S4.2 stricter `scripts/` profile: shebang/interpreter allowlist, network
  calls flagged, binaries rejected outside `assets/`.
- S4.3 draft.json provenance: author, session, source refs, tainted + taint
  sources, bounded scan report (exists — extend with findings).
- S4.4 publish gate: critical refuses with file/line/rule; script-bearing or
  tainted trees never publish without the draft+approve path.
- S4.5 quotas — already enforced (256 files / 1 MiB file / 8 MiB tree /
  64 KiB SKILL.md / 500 skills / 50 drafts-day); regression tests exist.
- S4.6 audit: `internal/audit` ledger at `<store>/.state/audit.jsonl`;
  kinds skill.draft_created/published/rejected/rolled_back/archived/
  restored/published + skill.scan — metadata only.
- S4.7 `scan.RedactSecrets` + `hubctl skill install` redaction warning.
