# SPEC-0035: Web search and bounded deep research

Frozen: 2026-10-01. Issue 123.
Hermes pin `869228cab4a8276d3b4c78da9d9939670c47bd0f` (`0.21.0`).

1. Web search and extraction are the upstream Hermes `web` toolset
   (`web_search`, `web_extract`), present in `platform_toolsets` only when the
   space `features` list enables `web`. The hub does not reimplement fetch,
   extraction, SSRF policy, truncation, caching, or provider fallback; it
   configures them. Anonymous use works without keys: `ddgs` needs no
   credential, and the upstream keyless ring (exa, parallel, firecrawl,
   keenable free tiers) is the last resort unless `keyless_fallback: false`
   disables it. No persistent browser profile is involved. The bundled
   `deep-research-embedded` skill additionally requires the `deep_research`
   feature (which itself requires `web`): a space without it mounts a
   `generated/skills` copy where the skill does not exist and renders
   `skills.disabled` for the name. A `web:` settings block on a space without
   `web` is a validation error. Platforms without an explicit
   `platform_toolsets` entry (e.g. `api_server`) fall back to upstream
   composites that include the web tools; when `web` is off the hub also
   renders `agent.disabled_toolsets: [web]`, which upstream subtracts at tool
   granularity on every platform, closing that fallback. Both features are
   init defaults and are org-removable through `scope.yaml` like any other
   feature.

   Multi-engine discovery and fan-out are a hub-shipped upstream plugin,
   `config/plugins/hub-web`, mounted read-only at `/opt/hermes/plugins/hub-web`
   only while `web` is enabled (bundled `kind: backend` auto-loads it; the
   space cannot alter tool code). It registers three tools into the `web`
   toolset: `web_providers` enumerates registered engines with per-engine
   status (ready/keyless/unavailable), configured and default flags;
   `web_cache` inspects, prunes, or clears the on-disk extract cache;
   `web_search_multi` runs one query against `providers` (a named subset of
   the `search_providers` allowlist plus the default, `["all"]` for the whole
   set, or omitted for the default backend), in parallel, bounded to 8
   providers and a 60-second wall clock. Configured-but-unusable engines are
   pre-filtered into `providers_skipped` without a request; engines that fail
   land in `providers_failed`; `partial_failure` marks mixed outcomes. Hits
   are tagged with every provider that returned them, deduplicated by
   normalized URL, ordered by best rank in configured provider order, and
   capped at 25 merged rows. Names outside the allowlist are rejected before
   any request leaves the runtime. The plugin also registers a bounded system
   prompt section (`hub-web-providers`, ≤4k) that freezes the configured set,
   the default engine, and per-engine status into each session prompt, so the
   model answers provider questions from the rendered table or
   `web_providers` — never from tool descriptions or memory.

2. Provider selection is operator configuration, not hardcoded. `settings.yaml`
   `web:` renders into the Hermes `web:` section: `backend` (shared),
   `search_backend`, `extract_backend`, `search_providers`,
   `keyless_fallback`, `keyless_rescue`, `extract_char_limit`, `provider_tier`,
   `cache_enabled`, `cache_ttl_minutes`, `cache_exempt_hosts`. Only set fields
   are emitted. Backend names are validated at render against the pinned
   provider set (tavily, exa, parallel, perplexity, firecrawl, searxng,
   brave-free, ddgs, keenable, xai, nous). A search-only provider is rejected
   for `extract_backend`; a shared search-only `backend` requires an explicit
   `extract_backend`. `provider_tier` accepts `free`/`paid` on the four tiered
   providers only. `search_backend` is the single default engine;
   `search_providers` is the allowlist that parallel fan-out may use.

3. Provider credentials and endpoints are runtime secrets: `TAVILY_API_KEY`,
   `TAVILY_BASE_URL`, `EXA_API_KEY`, `PARALLEL_API_KEY`, `PERPLEXITY_API_KEY`,
   `PERPLEXITY_BASE_URL`, `FIRECRAWL_API_KEY`, `FIRECRAWL_API_URL`,
   `BRAVE_SEARCH_API_KEY`, `KEENABLE_API_KEY`, `SEARXNG_URL`, `XAI_API_KEY`.
   They enter the runtime through `secrets.<env>.env` or the protected
   self-env form (allowlisted only when that provider is configured), never
   through prompts, tool arguments, connection metadata, or results. `Doctor`
   names the missing env for a configured `searxng` or `firecrawl` backend;
   keyless-capable choices never warn.

4. Deep research is the bundled `deep-research-embedded` skill: a bounded state
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

   Web artifacts land in exactly three places, all inside the owning space.
   Search responses are cached in-process (`tools.web_result_cache` memo,
   `web.cache_ttl_minutes` TTL, default 20 min) and never touch disk. Successful
   `web_extract` pages persist under `$HERMES_HOME/cache/web` (mounted per
   space as `spaces/<user>/hermes/cache/web`, files `<host>-<digest>.cache.md`
   plus `extract-index.json`): upstream TTL-gates reads but never deletes, so
   `web_cache` (`status`/`prune`/`clear`) is the cleanup path — prune drops
   expired, missing-file, and out-of-root entries and unlinks orphans;
   only `*.cache.md` files flat inside the resolved cache dir can ever be
   removed. Oversized tool outputs spill to `$HERMES_HOME/cache/spillover`
   (`spaces/<user>/hermes/cache/spillover`), reaped by upstream's hourly
   housekeeping at 24h. Deep-research artifacts are plain files under
   `workspace/research/<slug>/` (`spaces/<user>/workspace/research/<slug>`),
   deletable by the owner or the agent at any phase. No artifact path crosses
   a space boundary: the supervisor contract smoke asserts on the real
   spawned container that the plugin mount is read-only from the space's own
   `generated/plugins` and that `/state/hermes` binds to that space alone.
