# Changelog

## Unreleased

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
