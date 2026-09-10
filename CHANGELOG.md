# Changelog

## Unreleased

- Added peer `spaces/<id>` scope homes, `scope.yaml`, persistent Hermes/connection/workspace
  storage and explicit dry-run/apply `migrate-spaces` migration with recoverable sources.
- Split `communication-hub` from `hermes-runtime` behind an authenticated private job contract;
  the gateway owns Telegram transport and queue data while the runtime owns scopes and Hermes.
- Added the channel-neutral `communication-hub` gateway with Telegram sender mapping,
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
