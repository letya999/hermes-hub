# CHG-0139 — M15 Model & Provider Layer

Milestone M15 on branch `feat/m15-model-provider-layer` (worktree
`hermes-hub-m15`, cut from `dev` @ e1c9bc6). Covers issues #185, #131, #182.

## Problem

- #185: the managed model relay pinned exactly one upstream (cliproxy); a
  quota/auth/provider failure surfaced to the agent as a hard error with no
  fallback, no hop observability and no owner-declared ladder.
- #131: model/provider settings were per-space only; operators could not
  share a default profile across owners or give one space its own
  CLIProxyAPI instance.
- #182: hub-media ran a single engine (`remote`/`fal`); no local engines, no
  logical-model routing, and async jobs stored only the provider request id
  — after a restart the poll could land on the wrong provider.

## Design

### Model relay ladder (#185)

- `HUB_MODEL_RELAY_UPSTREAMS=cliproxy=http://cliproxy:8317,name=url|KEY_ENV`
  parses into ordered `modelHop`s; strict validation (lowercase hop names,
  http(s) base URLs, no credentials/query/fragment/path traversal, no
  duplicates, valid env names).
- Ordered fallback on 401/403/429/5xx, transport errors and redirects; 4xx
  client errors relay through untouched. A failed hop cools down for one
  minute; `GET /v1/hops` exposes hop state (`cooldown_until`).
- Hop `KEY_ENV` resolves from the relay's env (the space's secrets env_file),
  overriding the caller's Authorization header — keys never enter compose
  literals.
- Every attempt/skip/failure logs an audit receipt; `X-Model-Hop` header
  names the hop that served the response.
- Stack plumbing: `model_fallbacks:` in space settings renders into
  `HUB_MODEL_RELAY_UPSTREAMS` after the space's cliproxy hop.

### Owner profiles + cliproxy topology (#131)

- `spaces/profiles.yaml`: `profiles: {name: {owners, model, model_url,
  cliproxy, model_fallbacks}}`. Layers apply default → named (sorted) → the
  owner's own file always wins (only unset fields are filled).
- `cliproxy: dedicated` on a non-infra space renders a per-space
  `cli-proxy-api` service aliased `cliproxy-<user>` on both networks; the
  shared `cliproxy` name keeps resolving the infra instance, and the space's
  model relay pins `cliproxy-<user>` as its first hop.
- Validation: `cliproxy` only `shared|dedicated`, dedicated forbidden on the
  infra space; fallback entries validated like relay hop specs.

### Media routing + local engine (#182)

- `HUB_MEDIA_ENGINES` lists engines in order; `HUB_MEDIA_MODEL_ROUTES` maps
  `logical=engine:provider_id[,...]` — every hop engine must be configured.
- `genRouter` walks hops for sync `Generate`, falls through on error or empty
  output, logs every attempt/success.
- New `command` media engine: `HUB_MEDIA_COMMAND` argv0, prompt on stdin,
  `HUB_MEDIA_REQUEST_{MODEL,KIND,SIZE}` env, media bytes on stdout — the
  self-hosted slot without a provider SDK.
- Async: `SubmitHandle{RemoteID, Engine, Model}` binds a submission to the
  accepting engine; sync-only hops are skipped in video routes. `Job`
  persists `engine`/`provider_model` so restart resumes polling the engine
  of record rather than re-submitting.

## Slices

- S1: relay ladder + cooldown + `/v1/hops` + audit logs — done
- S2: `model_fallbacks`/`cliproxy` settings, validation, compose render — done
- S3: `profiles.yaml` overlay default→named→owner — done
- S4: media engines list + routes parser + sync router — done
- S5: `command` media engine + engine-bound async handles + job resume — done
- S6: docs (architecture, SPEC-0040, integrations) + just check — done

## Verification

`just check` green: go test ./... race+shuffle, own Go statement coverage
86.33% (gate 85), gofmt/vet/staticcheck, docs-check, actionlint, and the
compose contract test. Docker gates intentionally not run (per owner
request); no image builds were attempted.
