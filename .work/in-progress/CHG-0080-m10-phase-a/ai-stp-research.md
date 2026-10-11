# Research note — ai-stp integration (issue #181)

Repo: https://github.com/ai-engineers-guild/ai-stp (AGPL-3.0, Python).
Self-description: "Build, verify, store, select and install complete AI harness
setups. Primary consumer is your agent, through the CLI." Distribution:
`ai-stp-cli` / `ai-stp` executable with a JSON-envelope CLI. Nine component
kinds; immutable, versioned setups; provider-owned native file writes.
Hosted index at ai-stp.aiguild.space. Activity: ~2.2k commits, 5 stars, young.

## Fit against Hermes Hub boundaries

- ai-stp installs *harness setup* (skills, agents, MCP configs, hooks) by writing
  provider-native files. Hermes Hub already owns this boundary: ToolHub catalog +
  prepared bundle + `MaterializeGlobalSkills`/`MaterializeHubPlugins` render
  skills/plugins into `spaces/<user>` read-only mounts, gated by features and
  org policy. Letting ai-stp write into a user space directly would bypass
  admission, capability policy and audit — the same reason chat-side installs
  were denied in SPEC-0047.
- The supported-harness matrix does not list Hermes; a provider would have to be
  written for our mount model anyway, at which point the value left is the
  *index* (verified setups + signatures), not the installer.
- Publication/sign-in behavior unverified; treat the hosted index as an
  untrusted source like any other catalog (same stance as ghcr/mcp-registry in
  the recipe catalogs).

## Recommended integration points (if any)

1. **Read-only discovery catalog.** Map ai-stp's index onto the existing
   `HUB_RECIPE_CATALOGS` pattern (one more catalog name resolving to the ai-stp
   API). Components become *candidates* for ToolHub onboarding — they still pass
   prepare_source → review → grant → enable like a GitHub source. Zero new
   trust: ai-stp supplies metadata + source refs, never install authority.
2. **Hermes provider for ai-stp (upstream contribution).** Only if (1) proves
   demand. A provider that translates an ai-stp setup into our materialized
   mounts would need the read-only, feature-gated contract honored — non-trivial
   and not ours to start.
3. **Out of scope:** ai-stp writing files into spaces, ai-stp CLI inside the
   runtime as an install path (duplicates terminal-bypass we just closed),
   hosted-index sign-in/sync.

## Minimum viable experiment

Point `HUB_RECIPE_CATALOGS` at a thin adapter that reads the ai-stp index and
emits catalog entries (name, source repo ref, declared components). Onboard one
skill-type component end-to-end through the normal grant path. If the entry
shape does not map cleanly onto prepare_source inputs, stop — the index schema
is the real interface test.

## Verdict

Go only as a metadata catalog. The install/verify machinery duplicates ToolHub
admission + capability policy we already own; adopting it would create a second
authority over user spaces. Revisit when ai-stp lists Hermes natively or the
index grows enough verified content to matter.

## 2026-10-11 correction

The [pinned compatibility review](../../../docs/ai-stp-compatibility.md) supersedes
stale version/compatibility statements above. SPEC-0047 is file transfer, not a
catalog-installation ban. No Hermes adapter or live account action is claimed.
