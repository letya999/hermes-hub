---
chg: CHG-0195
title: M24 #363 S8 — secrets and settings via the credential broker
issue: 363
status: in-progress
---

# CHG-0195 — skill secrets and settings (SPEC-0083 R8)

Skill secrets never live in skill files and never enter the Hermes process
environment. Declared credential names are stored per `(principal, skill)`
inside the owner's skill store under `.state/` (private to the store — the
effective view never sees dot-entries), and lease into the disposable exec
container for exactly one call via the stdin exec-scratch protocol — never
argv, never container config, never tool results or logs.

Design note on R8.2's "broker skill-secrets": the broker module is forbidden
to become a canonical secret store (module rules), so the (principal, skill)
binding is realized as `spaces/<p>/skills/.state/credentials/<skill>.json`
(0600) — the per-user store the runtime already owns. The *declarative
delivery contract* is `ScratchExecRequest.env` riding the existing stdin
channel into `docker run -i … hubctl exec-scratch` — the same property
class as broker materializers, with negative tests.

## Slices

- S8.1 `requires.credentials[]` + compat normalization:
  - `requires.credentials` (strings or `{name,prompt,help,required_for,hard}`
    objects), `requires.env` alias, `metadata.hermes.requires_env`,
    `metadata.openclaw.requires.env` — all fold into `Requires.Credentials`
    (rename of `Requires.Env`, serialized `credentials`).
  - Codex `agents/openai.yaml`: `dependencies.tools` → `Requires.Tools`,
    `policy.allow_implicit_invocation` → `AllowImplicitInvocation` (tree-level
    at Build, like pep723Deps).
  - `metadata.hermes.config` → `Manifest.Settings` (`SettingSpec` list).
  - Reserved secret names rejected at parse/validate: `HUB_*`, `BROKER_*`,
    `HERMES_*` prefixes (hub/broker-owned names can never be declared).
- S8.2 `.state` store helpers (`internal/skillstore/state.go`):
  `ReadCredentials`/`WriteCredentials` (`name ^[A-Z][A-Z0-9_]*$`, ≤16,
  value ≤16 KiB, no \x00\r\n; 0600 file under 0700 `.state/credentials/`),
  `ReadSettings`/`WriteSettings` (same dir hygiene, values ≤4 KiB).
- S8.3 readiness: `Entry.CredsMissing` + hard `Missing` veto;
  `Capabilities.SkillCreds` (skill→stored names) resolved at render from the
  user store `.state`; `skill()` surfaces `credentials`, `setup_needed`,
  `form_url` via the existing `/v1/credential-forms` relay.
- S8.4 form → `.state`: gateway `formKeys` accepts `service: "skill:<name>"`
  (allowed keys = declared credentials from the mounted effective index);
  `credentialForm.Skill`; persist writes `.state/credentials/<skill>.json`
  via managed `/v1/skill-creds` (new runtime endpoint: auth + envelope +
  declared-name filter, no restart) or the unmanaged direct write.
- S8.5 exec lease: `code_exec` gains optional `skill` arg (schema only —
  never argv); `DockerScratchExec` resolves `(principal, skill)` → env from
  `.state/credentials` ∩ declared names; `ScratchExecRequest.Env` → stdin;
  `exec-scratch` appends validated entries (name pattern, blocklist, ≤16) to
  the sanitized `cmd.Env`. Compose mounts the user skills store ro at
  `/skills-store` on ToolHub (`HUB_SKILLS_STORE`) for the resolver.
- S8.6 settings: `.state/settings/<skill>.json` per user; `skill()` reports
  effective settings (stored-or-default); `skill_manage` `configure` action
  writes `.state` only (validated keys ⊆ declared specs — no draft, no card;
  it is private user state like settings_set, not tree mutation).
- S8.7 contract/docs/index: `code_exec` +`skill` arg and `skill_manage`
  +`configure` are agent-tool contract changes → bump `hub-agent-tools`
  catalog version, prompt contract, docs/tools.md + docs/routines.md;
  state.yaml, index.

## Negative-test duties (R8.3)

- Secret value never in: `docker run` argv, `exec-scratch` stdout/stderr,
  tool results, `skill()`/`skill_view` output, logs/audit.
- Undeclared env names in `.state` are not leased; undeclared skill names
  rejected; ineligible skill → no lease.
- `skill` arg on code_exec when no creds stored → clean exec, no env.
- Reserved names (HUB_/BROKER_*) rejected at draft/publish lint.
- Missing hard credential → skill hidden (`missing: [credential:…]`);
  missing soft → `setup_needed` + form link.

## Out of scope

- Broker-module changes (no new delivery method/provider this change).
- Bounded-cli cell `CliExecRequest.Skill` parity — the scratch channel is the
  lease surface; `native:terminal` executes inside Hermes and must never
  receive secrets by design (documented).
- Live docker acceptance — #368.
