# CHG-0001: Build a private Hermes hub

## Goal

Deliver a runnable source archive with actual Hermes/MCP wiring, Go control tooling,
career-go integration, owner-managed credentials, tests and open-source engineering.

## Steps

1. Inspect pinned upstream contracts and Memory Bank templates.
2. Implement configuration, file/job tools, native bridge and Docker supervision.
3. Add regression tests, CI, documentation and release packaging.
4. Run available gates, report unavailable live checks and deliver the archive.

## Acceptance

SPEC-0001 baseline, successful local gates, no personal data in archive, explicit setup
and deployment limits. Docker/real-account checks cannot be replaced by mock assertions.
