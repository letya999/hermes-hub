---
description: Pinned ai-stp research correction and explicit Hermes compatibility decision for issue 181.
last_verified: 2026-10-11
---
# ai-stp compatibility (#181)

Reviewed upstream commit
[2e966346ffd4b5e3cc830df2afaae58668d707d1](https://github.com/ai-engineers-guild/ai-stp/tree/2e966346ffd4b5e3cc830df2afaae58668d707d1),
not the moving main branch. Production Python CLI is `ai-stp-cli` version 0.0.43
([manifest](https://github.com/ai-engineers-guild/ai-stp/blob/2e966346ffd4b5e3cc830df2afaae58668d707d1/apps/cli/pyproject.toml)).
Rust `ai-stp-v2` is an opt-in preview, not the production replacement.

[README at the pin](https://github.com/ai-engineers-guild/ai-stp/blob/2e966346ffd4b5e3cc830df2afaae58668d707d1/README.md)
defines nine component kinds. Memory and rules are content, not extra kinds.

| Kind | Meaning for a future Hermes adapter |
|---|---|
| instruction | Untrusted text; cannot grant capabilities or change governance |
| skill | Reviewed skill content with exact source/version and owner activation |
| mcp | Existing ToolHub prepare/admit/enable pipeline; never arbitrary CLI install |
| hook | Executable behavior; unsupported until separately isolated and reviewed |
| command | Slash-command content; cannot overwrite deterministic Hub controls |
| agent | Agent definition; unsupported without verified Hermes format |
| plugin | Bundle with executable effects; no automatic import |
| setting | Harness-specific settings; only the existing reviewed key allowlist |
| cli | Executable program; existing isolated workload admission, never host install |

Anonymous public discovery is implemented in the upstream
[CLI catalog client](https://github.com/ai-engineers-guild/ai-stp/blob/2e966346ffd4b5e3cc830df2afaae58668d707d1/apps/cli/src/ai_stp_cli/cloud/catalog.py)
and [API router](https://github.com/ai-engineers-guild/ai-stp/blob/2e966346ffd4b5e3cc830df2afaae58668d707d1/apps/api/src/ai_stp_api/slices/catalog/router.py):
GET `/catalog/components` and `/catalog/setups` are public reads. Account actions,
private/synchronized content and publication are separate authenticated workflows.
This review checked source contracts; it did not log in, install upstream or claim
a successful live deployment. Author/component verification labels are provenance,
not proof that executable content is safe.

The pin lists supported harnesses but does not declare Hermes support. Therefore
the decision remains research-only: no guessed native config conversion, no
installer and no new provider. A future adapter must pin immutable versions,
verify every referenced artifact, map only documented formats and use existing
ToolHub consent/admission. Upstream code is AGPL-3.0-or-later; Hub's original code
is AGPL-3.0-only. No upstream source is embedded in this change.

This corrects the historical [phase-A note](../.work/done/CHG-0080-m10-phase-a/ai-stp-research.md):
SPEC-0047 concerns file transfer and does not prohibit catalog discovery. Absence
of a verified Hermes adapter is the relevant compatibility limit.
