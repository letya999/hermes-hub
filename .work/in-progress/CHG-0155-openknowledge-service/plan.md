# CHG-0155: OpenKnowledge service render (M20 stage 6)

Chain: ADR-0073 D8 -> SPEC-0070 R6 -> this plan -> `state.yaml`.
Previous stage: CHG-0154. Next stage: CHG-0156.

## Goal

`knowledge_ui` feature renders a pinned, single-replica `open-knowledge`
container per space — the WYSIWYG editor/agent-MCP surface over
`spaces/<id>/vault` — built through the hubctl BuildKit path, private
network only.

## Read before coding

1. `research.md` (OK docker contract); `internal/stack/compose.go`,
   `internal/stack/services.go`, `docker/` build conventions;
   ADR-0023 (optional MCP images) for the opt-in image pattern;
   `docs/operations.md` hubctl build/up flow.

## Slices

### S6.1 Pinned image build (R6.1)

Anchors: new `docker/openknowledge/Dockerfile`; build wiring next to
existing image pins.

Change:
1. `node:24-slim` + `apt git ca-certificates` +
   `npm i -g @inkeep/open-knowledge@<PINNED>` (version in one constant;
   bump = one-line diff) + entrypoint that runs
   `ok init --no-mcp --no-skills` when `/data/.ok` is absent, then
   `exec ok start`.
2. `ENV PORT=8080 OK_BIND=0.0.0.0`; no `VOLUME` instruction.
3. Build through the existing hubctl/just BuildKit path — never raw
   `docker compose build` (AGENTS.md).

Tests (red first):
- `TestOKImagePinSingleSource` — pin appears exactly once in build defs.
- Render test: image tag `…open-knowledge:<pin>` in compose.

### S6.2 Compose service render (R6.1, R6.3, R6.4)

Anchors: `internal/stack/compose.go` service table;
`internal/stack/config.go` `Features`; `services.go` healthchecks.

Change:
1. Feature `knowledge_ui` → service `open-knowledge`: image from S6.1,
   `spaces/<id>/vault` → `/data`, `replicas` conceptually 1 (single
   service, no scale knobs), private networks only, `restart: unless-stopped`.
2. Env: `OK_ALLOW_EXTERNAL=1`, `OK_EXTERNAL_URL=http://localhost:<ui_port>`,
   `TZ` from space timezone. No publish here (that's CHG-0157).
3. Healthcheck on `/readyz`; `doctor` reports service health and missing
   prerequisites.
4. Feature off → zero rendered artifacts.

Tests:
- `TestComposeOKServiceGated` — absent without feature.
- `TestComposeOKSingleReplica` — no scale/replica keys.
- `TestComposeOKPrivateNetwork` — no host port on non-loopback.
- `TestDoctorReportsOKWhenEnabled`.

### S6.3 Vault-root boundary + ignore policy (R6.2)

Anchors: `internal/vault` (scaffold adds OK metadata ignores).

Change:
1. OK project root = vault root; scaffold `.okignore`-equivalent entries
   for `.git`, `.graph`, `runtime`-adjacent paths.
2. Validation refuses `knowledge_ui` if the vault dir would contain
   `*.env`/`*.auth`/secret material (structural check, belt & suspenders
   on D1 layout).

Tests:
- `TestOKRootContainsNoSecrets` — scaffolded tree passes the check.
- `TestRefuseWhenSecretFilePresent` — planted `x.env` blocks render.

## Red-team checklist

- `replicas: 2` attempted via override → render produces exactly one
  service; document single-writer in compose comments.
- `/data` must never be the space root — only `spaces/<id>/vault`
  (assert mount source).
- First boot on non-empty vault without `.ok` → init runs
  `--no-mcp --no-skills`, content untouched.
- Container as non-root user where upstream allows; note upstream's own
  constraints honestly in THIRD_PARTY/docs.

## Acceptance

A6.1–A6.4: enabled space `up` → `/readyz` healthy; editor loads via
tunnel; render has exactly one OK service; disabled space renders zero.

## Out of scope

- `/mcp` wiring into Hermes tools (CHG-0156).
- Human edge/port publish (CHG-0157), org variant (CHG-0159).
- Contributing to OK upstream.
