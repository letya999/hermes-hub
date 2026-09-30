---
description: Issue 123 — configured web search/extract providers plus a bounded deep-research skill.
last_verified: 2026-10-01
---

# CHG-0058 Web search and bounded deep research

## Change

- Add an optional `web:` block to `settings.yaml` that renders into the Hermes
  `web:` config section: `backend`, `search_backend`, `extract_backend`,
  `keyless_fallback`, `keyless_rescue`, `extract_char_limit`, `provider_tier`,
  `cache_enabled`, `cache_ttl_minutes`, `cache_exempt_hosts`. Only set fields
  are emitted; upstream defaults apply to the rest.
- Backend names validate against the pinned upstream provider set (tavily, exa,
  parallel, perplexity, firecrawl, searxng, brave-free, ddgs, keenable, xai,
  nous); search-only providers are rejected for `extract_backend` and
  extract-incapable names for `search_backend`. `provider_tier` accepts only
  `free`/`paid` on exa, parallel, firecrawl, keenable.
- `web.research` carries bounded deep-research limits (max_rounds, queries_per_round,
  pages_per_round, max_pages, deadline_minutes) with hard ceilings; the
  `web-deep-research` skill reads them from the mounted read-only config.
- Provider credentials stay out of prompts: `secrets.<env>.env` already delivers
  every non-gateway key to the runtime env; the self-env allowlist gains the env
  names of configured providers so the protected chat form can deliver them.
  `Doctor` warns when a configured provider's required env is missing.
- New bundled skill `config/skills/web-deep-research`: brief → bounded
  search/extract rounds → notes file → cited report with a `Limitations`
  section for failed/blocked sources. Browser only when extraction cannot
  satisfy a needed source (anonymous `browser_guest`, never the login profile).
- Init secrets template and docs list every upstream provider env name.

## Contract

Issue #123, [SPEC-0035](../../../specs/active/SPEC-0035-web-research.md).
Hermes pin `869228cab4a8276d3b4c78da9d9939670c47bd0f` (`0.21.0`).

## Verification

`go test ./internal/stack`, `just check`. No provider mutation is tested;
anonymous paths (ddgs, keyless ring) work without keys.
