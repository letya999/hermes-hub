# Changelog

## Unreleased

- Added the standard web capability (issue #123): `settings.yaml` `web:` now
  configures the upstream Hermes `web_search`/`web_extract` providers
  (tavily, exa, parallel, perplexity, firecrawl, searxng, brave-free, ddgs,
  keenable, xai, nous) with validated backend names, provider tiers, keyless
  and cache policy. Provider keys reach the runtime through secrets env or the
  protected self-env form, never prompts. Added the bundled
  `web-deep-research` skill: a bounded state machine on disk (perspective plan,
  source registry, gap-driven rounds, distilled notes, outline → sections →
  cited report with a verify pass) whose `web.research` limits can only
  tighten the ceilings (16 rounds / 100 pages / 10 pages per host / 240
  minutes); long runs continue across durable hub-routine wakes and resume
  cold from `state.json`.
- Added a prepared DataLens connector (issue #120): exact-source entry for the
  official `datalens-tech/datalens-mcp` @96b3d6b (MIT). Five-tool gateway with
  server-side OpenAPI `x-mcp-scope` enforcement — read/write/privileged stay
  distinct effects; org scope is env-pinned outside model arguments. Broker
  contract `datalens-auth` (organization ID + Authorization header, optional
  API URL override whose host extends egress). Fixed envs select static auth
  (no `yc` in the image), bound responses, and `NODE_USE_ENV_PROXY=1` so Node
  fetch honors the workload Squid ACL.
- Added reviewed `preflight_network` catalog flag: a prepared entry whose
  server must reach its API before answering `tools/list` (remote schema fetch
  at startup) runs the unauthenticated probe with egress and placeholder
  credentials. Unprepared sources keep `--network none`.
- Added a workspace document and image capability on the hub MCP profile:
  extract, create, edit, and convert for txt, md, csv, html, pdf, xlsx, and
  pptx; inspect and local png, jpeg, and webp conversion; opt-in image
  generation and edit with a configured provider, model, and delivery. pdf,
  xlsx, and pptx are simple text packages. doc and docx stay unsupported.
  CLIProxy image models use two calls: the images-endpoint ids stay on
  `/images/generations` and `/images/edits`, and the Gemini image ids use
  `/chat/completions`. The default model stays `gpt-image-2`.

## 0.3.0 � M1 native execution � 2026-09-12

- Removed the `hermes -z` executor and persistent-mode toggle after publication
  and real-runtime acceptance of the v0.2.1 compatibility artifacts.
- Kept native static execution for explicit selection and unmigrated cron;
  existing legacy selections read as static, and CLI-only contexts remain idle.
- Enforced supervisor drain for environment-selected migration and fixed Linux
  fixture host resolution. Homes, sessions, spool and uncertainty remain preserved.

## 0.2.1 — M1 compatibility release — 2026-09-12

- Added host-selected per-context supervisor rollout and rollback without moving
  Hermes homes, queued jobs, session/run mappings or delivery state.
- Added authenticated job admission/resume, cancellation, streamed events and
  approval fencing with durable uncertainty and automatic infrastructure recovery.
- Verified two-user isolation, idempotency, leases, automatic five-minute idle stop,
  cold session restoration, orphan ownership and due routine wake/sleep against
  pinned Hermes 0.21.0 in real Docker containers.
- Retained static/one-shot rollback for this compatibility release. Legacy removal
  follows acceptance of this released artifact; enabled native cron blocks migration.

- Added the channel-neutral `hub-communication` gateway with Telegram sender mapping,
  durable job/reply spool, scoped Hermes subprocesses, commands and best-effort secret
  message deletion.
- Added Telegram-visible service catalog and self-service connector enablement with
  allowlisted env provisioning, isolated feature state and supervisor restart.
- Added bundled GitLab `glab` with PAT env auth, Google read-only default with explicit
  write opt-in, and Atlassian Rovo personal API-token auth.
- Added organization/user scope overlay: membership checks, policy narrowing,
  separate organization secrets, read-only organization documents and action gates.
- Separated Telegram bot transport from personal-account MCP and added explicit
  user-runtime `env_update` with constrained persistent overlay and supervisor restart.
- Replaced local Jest/Python tooling and the Python container supervisor with Go and Just.

## 0.2.0 — 2026-09-08

- Standalone Hermes: removed embedded CareerGo and JobFetch adapter.
- spaces/user, separate dev/prod env files, Docker targets and persistent volumes.
- Generic MCP, native skills/hooks/memory and source mounts excluding private spaces.
- Original local CI and coverage gates, self-hosted Runner workflow and AGPL-3.0-only.

## 0.1.0 — 2026-09-07

- Private per-person Hermes deployments with Go CLI and Docker/VPS configuration.
- Pinned MCP connectors, browser desktop, Meet caption and local speech setup.
- Bounded files/archive, HH application contract and JobFetch tenant paging.
- Bundled career-go source and authenticated native tools companion.
- Memory Bank documentation, regression tests, just gates and GitHub workflows.
- Live account and container validation limits recorded separately.
