# CHG-0258: config.yaml drives runtime, supervision, ports and channels (M30 stage 4)

Chain: ADR-0093 D2, D5 -> SPEC-0092 R4 -> this plan -> `state.yaml`.
Evidence: `../../done/CHG-0254-passport-gap-audit/research.md` Finding 2.
Previous stage: CHG-0257. Next stage: CHG-0259.

## Goal

`config.yaml` holds the runtime settings, supervision policy, host ports,
channels and memory policy, and these are the values the runtime, the
supervisor, communication-hub and compose actually use. Nothing about
ports or channels is lost on migration.

## Read before coding

1. SPEC-0092 normative `config.yaml`, effect table rows for config.
2. `internal/stack/config.go` `Settings` (`Timezone`, `OAuthPort`, `BrowserPort`, `SlackEventsPort`, `Ingress`, `TelegramAuth`, `Memory`, `Compression`, `Context`).
3. `internal/stack/hermes_capability.go` `managedHermesConfig`; `materialize.go` `materializeHermesConfig`.
4. Supervisor policy inputs (`internal/supervisor/ensure.go`, idle/restart settings); communication channel config in `internal/communication`.
5. `internal/migration/passport.go` mapping.

## Preconditions and rules

- Channel credentials are refs only (`{ref: name}` → Broker/secret store); no tokens in config.
- Red test first.

## Slices

### S4.1 Compile config v2 (R4.1)

NOTE (updated by CHG-0255 S1.0): v2 leaves (runtime/supervision/ports/channels/memory) and normative emission already landed in CHG-0255 with basic checks; this slice keeps the deeper rules (port owner derivation via catalog, channel-feature binding) and the migrator edge cases.

Anchors: `passport.go` `classes["config"]` + checks; `migration/passport.go`.

Change:
1. Leaves: `runtime.{timezone,locale,context.*}` (choice/user_data), `supervision.{idle_stop,restart,watchers.*}` (narrow; `protected` kept), `ports.<name>` (user_data, owner derived by catalog port name → service), `channels.<name>.{bot,allowed_users,voice}` (refs + choice), `memory.{backend,nightly_rollup}` (choice).
2. Checks: port unique per host across spaces of the deployment; port owner service present in stack; voice requires `hub-stt`/`hub-tts` instances; channel requires its catalog feature.
3. Migrator maps `timezone`, `oauth_port`, `browser_port`, `slack_events_port`, `ingress`, `telegram_auth`, `memory`/`honcho`, `compression`/`context` into these leaves.

Tests (red first):
- `TestConfigPassportNormativeShape`
- `TestConfigPortsUniqueAndOwned`
- `TestConfigChannelRefsOnly` (literal token → error)
- `TestMigrationPreservesPortsAndChannels` (artem-shaped fixture)

### S4.2 Render from config (R4.2)

Anchors: `managedHermesConfig`, `materializeHermesConfig`; compose ports in `compose.go`; supervisor policy file; communication channel config writer.

Change:
1. Effective Hermes config runtime section (timezone, locale, compaction/context) from `config.runtime`.
2. Compose `ports` from `config.ports` mapped to owning service; OAuth redirect URI from `ports.oauth-callback`.
3. Supervisor idle/restart/watchers from `config.supervision` (live reload of the policy file).
4. communication-hub channel config from `config.channels` (refs resolved by the existing secret path at service start).

Tests (red first):
- `TestEffectiveHermesConfigFromConfigPassport`
- `TestComposePortsFromConfigPassport`
- `TestSupervisorPolicyReloadLive`

## Red-team checklist

- User layer exposes a private service port or reuses another space's port → compile error.
- Protected watcher removal by user → compile error (existing rule kept).
- Diagnostics print port names/numbers and channel names only.

## Acceptance

A4 live: change `ports.oauth-callback` → diff `recreate` for the owning
service only, endpoint answers on the new port, other containers keep their
start time. Change `supervision.idle_stop` → applied without restart.

## Out of scope

Channel onboarding UX; new channel types.

## Done checklist

- [ ] Tests red then green; `just check`; docker-check; docker-clean.
- [ ] Live A4 in state.yaml.
- [ ] docs/spaces.md, docs/operations.md, docs/services/{supervisor,communication}.md updated.
- [ ] Issue closed; state done; folder moved.
