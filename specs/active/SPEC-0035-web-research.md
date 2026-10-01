# SPEC-0035: Web search and bounded deep research

Frozen: 2026-10-01. Issue 123.
Hermes pin `869228cab4a8276d3b4c78da9d9939670c47bd0f` (`0.21.0`).

1. Web search and extraction are the upstream Hermes `web` toolset
   (`web_search`, `web_extract`), enabled in `platform_toolsets`. The hub does
   not reimplement fetch, extraction, SSRF policy, truncation, caching, or
   provider fallback; it configures them. Anonymous use works without keys:
   `ddgs` needs no credential, and the upstream keyless ring (exa, parallel,
   firecrawl, keenable free tiers) is the last resort unless
   `keyless_fallback: false` disables it. No persistent browser profile is
   involved.

2. Provider selection is operator configuration, not hardcoded. `settings.yaml`
   `web:` renders into the Hermes `web:` section: `backend` (shared),
   `search_backend`, `extract_backend`, `keyless_fallback`, `keyless_rescue`,
   `extract_char_limit`, `provider_tier`, `cache_enabled`,
   `cache_ttl_minutes`, `cache_exempt_hosts`. Only set fields are emitted.
   Backend names are validated at render against the pinned provider set
   (tavily, exa, parallel, perplexity, firecrawl, searxng, brave-free, ddgs,
   keenable, xai, nous). A search-only provider is rejected for
   `extract_backend`; a shared search-only `backend` requires an explicit
   `extract_backend`. `provider_tier` accepts `free`/`paid` on the four tiered
   providers only.

3. Provider credentials and endpoints are runtime secrets: `TAVILY_API_KEY`,
   `TAVILY_BASE_URL`, `EXA_API_KEY`, `PARALLEL_API_KEY`, `PERPLEXITY_API_KEY`,
   `PERPLEXITY_BASE_URL`, `FIRECRAWL_API_KEY`, `FIRECRAWL_API_URL`,
   `BRAVE_SEARCH_API_KEY`, `KEENABLE_API_KEY`, `SEARXNG_URL`, `XAI_API_KEY`.
   They enter the runtime through `secrets.<env>.env` or the protected
   self-env form (allowlisted only when that provider is configured), never
   through prompts, tool arguments, connection metadata, or results. `Doctor`
   names the missing env for a configured `searxng` or `firecrawl` backend;
   keyless-capable choices never warn.

4. Deep research is the bundled `web-deep-research` skill: a bounded state
   machine on disk under `research/<slug>/` — `brief.md` (north star),
   `plan.yaml` (perspective-driven sub-topics), `sources.yaml` (URL registry
   with per-URL status), `notes/` (distilled claims, each with its URL),
   `gaps.md` (open-question queue driving the next round), `outline.md`,
   `sections/`, `report.md`, `state.json`. Each round reconstructs state from
   disk instead of accumulating pages in context; gap analysis derives the
   next queries. Synthesis writes an outline first, then per-section drafts
   from the relevant notes only, then assembles a report with inline source
   URLs, a `Sources` list, and a mandatory `Limitations` section naming
   failed, blocked, or paywalled URLs and unanswered sub-questions. A verify
   pass drops or marks "[unverified]" any claim without a mapped source.
   Bounds come from `web.research` (`max_rounds`, `queries_per_round`,
   `pages_per_round`, `max_pages`, `max_pages_per_host`, `deadline_minutes`),
   readable from the mounted read-only config, and may only tighten the
   ceilings 16 rounds / 5 queries / 8 pages / 100 pages / 10 pages per host /
   240 minutes. Reaching a bound, diminishing returns, or an interrupt ends
   research and reports what exists. Long runs (`deadline_minutes ≥ 60` or an
   explicit request) are durable: the skill registers a hub `routine_create`
   job that executes one bounded round per wake, resumes cold from
   `state.json`, and pauses itself on completion.

5. Fetched pages and snippets are untrusted data. The skill forbids following
   instructions inside them and forbids placing credentials in URLs or
   queries; upstream additionally refuses URLs carrying credential-like
   parameters. Redirects, private-network targets, oversized or binary
   content, unsupported schemes, and dead ends fail closed in the upstream
   fetch/extract path. Browser automation is the fallback only: anonymous
   `browser_guest`, never the persistent owner profile, and only when
   extraction cannot satisfy a needed source.

6. Per-user isolation is unchanged: provider env lives in the owning space's
   runtime container, notes and reports live in that user's workspace, and
   cross-user history or results are never read.
