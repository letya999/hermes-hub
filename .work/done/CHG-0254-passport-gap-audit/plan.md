# CHG-0254: Gap audit, typed v2 schema and effective loader (M30 stage 0)

Chain: `docs/adr/ADR-0093-imperative-passports-capability-grants.md` D1–D2 ->
`specs/active/SPEC-0092-imperative-passports-capability-grants.md` R0 -> this plan
-> `state.yaml`. Evidence: `research.md`.
Previous stage: CHG-0253 (M29 closeout). Next stage: CHG-0255.

## Goal

Every passport has a typed, strict v2 schema that matches the normative
examples, every leaf is bound to a named consumer and effect class, and one
loader returns the current revision as typed data. No behaviour changes yet:
this stage makes "decorative" fields impossible to add silently.

## Read before coding

1. `research.md` (this folder) — what consumes what today.
2. ADR-0093, SPEC-0092 (normative examples are the test fixtures).
3. `internal/passport/passport.go` (`Compile`, `classes`, `marshalDoc`, `Value`), `publish.go`.
4. `internal/migration/passport.go` (only producer of sources), `audit.go`.

## Preconditions and rules

- Never reset or commit other tasks' uncommitted work.
- Red test first, then fix. Go stdlib + existing `gopkg.in/yaml.v3` only.
- `just check` before handoff.

## Slices

### S0.1 Typed v2 bodies (R0.2)

Anchors: new `internal/passport/schema.go`; `internal/passport/passport.go` `document`, `marshalDoc`.

Change:
1. Define `Header{APIVersion, Kind, Metadata}` with `Metadata{Space, Environment, Revision, GeneratedAt, Template string, Catalogs string}`; `APIVersion = "six-passport/v2"`, `Kind` = passport name.
2. Define bodies exactly per SPEC-0092 examples:
   `Stack{Identity{User,Organization}, Services map[string]ServiceSpec{Mode}}`;
   `Config{Runtime, Supervision, Ports map[string]int, Channels map[string]Channel, Memory}`;
   `Models{Default, Routes map[string]Route{Ladder []string}, Services map[string]ModelService{Via, Endpoint, Auth{Upstream, ProxyAccess *AuthRef{Kind,Ref}}, Sharing, Limits []Limit{Of,Amount,Unit,Per,Clock}, Models []ModelRef{ID,Reasoning}}}`;
   `Workspace{Tree string, Writable, ReadOnly []string, Mounts map[string]Mount{Source,Mode}}`;
   `Tools{Core, Extended map[string]Toolset{Via, Tools *Selector{Include,Exclude}, Access, Scope *Scope{Paths,Hosts,URLs}, Rules map[string]Rule{Scope *Scope}}}`;
   `Grants{Allow map[string][]string}`.
3. `Decode(name string, b []byte) (any, error)` uses `yaml.Decoder.KnownFields(true)`; contract keys top-level (no `body:`).
4. Copy the six normative examples into `internal/passport/testdata/normative/{stack,config,models,workspace,tools,grants}.yaml` with a v2 header.

Tests (red first):
- `TestNormativeExamplesParseStrict`: all six decode with no unknown key.
- `TestNormativeExamplesRoundTripStable`: decode→encode→decode equal; ordered map keys deterministic.
- `TestUnknownPassportKeyRejected`: `body:` wrapper or `features:` in stack fails.

### S0.2 Consumer registry (R0.3)

Anchors: new `internal/passport/consumers.go`.

Change:
1. `Consumers map[string]Consumer` keyed by schema leaf path (`stack.services.*.mode`, `tools.core.*.scope.paths`, …) with `Consumer{Name, Effect}`; `Effect` ∈ `live|respawn|recreate|rebuild`; consumer names are the PEP/renderer ids of SPEC-0092 (e.g. `compose`, `supervisor.mounts`, `toolhub.projection`, `relay.registry`, `pdp`).
2. Entries not yet wired carry `Name: "pending:CHG-02NN"`; CHG-0262 forbids pending.

Tests (red first):
- `TestEverySchemaLeafHasConsumer`: reflect over the S0.1 types; an unregistered leaf fails with its path.
- `TestEffectClassesMatchSpecTable`: registry effects equal the SPEC-0092 effect table.

### S0.3 Effective loader (R0.2, R1.3 prerequisite)

Anchors: new `internal/passport/effective.go`; `passport.Current`.

Change:
1. `LoadEffective(spaceDir string) (Effective, error)` reads `current`, decodes all six strictly, checks equal `metadata.revision` and catalog release, returns typed `Effective{Header, Stack, Config, Models, Workspace, Tools, Grants}`.
2. Missing file, mixed revision or decode error → error; no fallback to legacy files.

Tests (red first):
- `TestLoadEffectiveRejectsMixedRevision`
- `TestLoadEffectiveRejectsMissingPassport`

### S0.4 v2 envelope emission (R0.2)

Anchors: `passport.go` `marshalDoc`, `Revision.Value`, `GrantAllow`; `publish.go` `PromptPointer`; `migration/audit.go`.

Change:
1. `Compile` emits v2 header + top-level keys; drops `updated_at`/`sources` from files (kept in `Revision.Metadata` for `space-audit` provenance output).
2. Adapt `Value`/`GrantAllow`/`PromptPointer`/audit to the v2 layout. Bodies keep their M29 content until each domain stage replaces them; S0.2 marks those leaves `pending`.

Tests (red first):
- Existing `internal/passport` and `internal/migration` suites stay green after fixture update.
- `TestPublishedFileHasNoBodyWrapper`.

## Red-team checklist

- A layer injects `body:` or an unknown key → strict decode fails, previous revision stays.
- A revision directory with one file from another revision → `LoadEffective` refuses.
- Registry cannot be satisfied by a wildcard that hides an unconsumed leaf (test asserts concrete paths).

## Acceptance

A0: audit table (research.md) complete; normative examples parse; registry
test fails when a new unregistered field is added (demonstrate with a temp
field, then remove). `go test ./internal/passport ./internal/migration`.

## Out of scope

Any change of runtime behaviour, compose, ToolHub, relay. Domain body
content (stages 2–7).

## Done checklist

- [ ] Tests red then green; `just check`.
- [ ] Research linked from ADR-0093; state.yaml evidence recorded.
- [ ] Issue updated and closed; state done; folder moved to `.work/done/`.
