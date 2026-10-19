# CHG-0025 closeout — remaining work to leave M5.1

This stage is **M5.1: trusted Git artifact + local isolated runtime**.
It is not M5.2 (grants, elicitation, revoke UX) and not M5.3 (Hermes chat
journey / reconnect). Issues in scope: #97, #98, local half of #73.
VPS (#73 remainder) and Hermes reconnect (#74) stay deferred.

Product meaning of “100 users, arbitrary MCP”:
one shared immutable image per exact Git SHA; many per-user bindings
(credentials + state); only a **bounded** number of live processes
(`max_active` + FIFO + idle stop). Configured ≠ running. Image layers
may reuse; processes, volumes and secrets must not.

Do **not** treat 100 concurrent MCP+ToolHive stacks on Docker Desktop
as a closeout gate. Milestone M5 already says not to promise 100 heavy
workloads at once on an arbitrary PC.

## Already proven (do not re-run)

| Gate | Fact |
|---|---|
| Four public MCP SHA → recipe → isolated BuildKit → digest/provenance/SBOM | Serena, Context7, Go filesystem, Rust filesystem at frozen SHAs |
| Trusted only after real `tools/list` contract | `hubctl artifact preflight` / `--contract`; claimed import tools rejected |
| Live `docker_fallback` + stock ToolHive + Hermes list + one safe call | All four, 2026-09-16; unique result bodies |
| Linux ELF bridge; Windows `hubctl.exe` rejected | live Windows build |
| Builder policy `rootless-buildkit-bootstrap-v1` distinct from MCP runtime | implemented |
| 100 unique bindings 95+5 env-files, sequential unique nets/volumes | live Docker; IPAM cannot hold 100 concurrent /16 networks |
| Restart onto same volume; delete one without the other | live Docker |
| FIFO budget + idle cleanup | shipped controller (unit of real code) |
| `just check` | 85.03% statement coverage, race/format/vet/staticcheck/docs/actionlint |
| Caller catalog (files/skills) | **not** mounted; issue #103 opened; not this stage |

## Close this stage (short remaining)

All five gates green 2026-09-16. CHG-0025 is done; next change is M5.2 bind/project/revoke.

### 1. Owner decision — scale model (no code)

Accept in writing (comment on #73/#97 or this file):

- 100 users = 100 **registered** bindings, not 100 live stacks.
- Live cap = `max_active` (start with 1–4 on Desktop).
- Same SHA → one image, many bindings.
- Sequential 100 unique names + FIFO is the capacity proof on this PC.
- Optional later: `--subnet /24` for a small concurrent demo (8–16), never 100 full stacks here.

Owner accepted this scale model on 2026-09-16 (close the stage and go to M5.2).

### 2. Secret probe from the MCP container (live Docker, one test)

Production topology already puts `HERMES_BRIDGE_TOKEN` on the relay, not
the MCP, with a private PID namespace. The red `/proc` result was a
**rejected one-container fixture**.

Add/extend `-tags integration` so a process **inside the MCP container**
cannot read the relay/ToolHive token from env, `/proc`, logs or metadata.
Record that as the secret-isolation pass. Do not require same-UID sibling
inside the ToolHive process namespace to go green (kernel: same UID +
same PID ns can read `environ`; we will not patch ToolHive).

### 3. Revoke / rotation denies `tools/call` (one live path)

On one already-trusted public MCP (or the generic fallback endpoint):
enable → safe call works → disable or rotate credential → next
`tools/call` denied. This is the missing row of criterion 3/4, not a
new product.

### 4. Image reuse with distinct processes (cheap live inspect)

Two bindings of the **same** image digest: same image id, different
container ids, different volumes, different credentials.env. Already
implied by sequential 100; make it an explicit inspect assertion so
the “share the image, not the desk” story is one log line.

### 5. Docs stamp

After 1–4: rewrite the last verification.md block to the accepted scale;
set CHG-0025 `state.yaml` remaining gates to passed/failed with facts;
one paragraph in `docs/architecture.md` / SPEC-0019 that configured
bindings ≫ live slots. `just check` if code changed. No new
`just docker-check` unless the image contract moved.

## Out of this stage (next change)

Do not pull these into CHG-0025 closeout:

| Item | Why later |
|---|---|
| #103 mount of `spaces/<user>` files/skills | Optional grant; isolation today is correct |
| #81 catalog-default vs runtime access | M5.2 control plane |
| Hermes ordinary-message onboarding (#74, #82, #99) | M5.3 user journey |
| Calendar/Slack/Telegram live login (CHG-0024) | Separate change; dirty tree stays |
| 100 concurrent ToolHive stacks / VPS | Deferred; not Desktop DoD |
| Patch Hermes or ToolHive | Forbidden |

## Next stage after closeout (M5.2)

**Change name (proposed):** CHG-0026 user binding journey  
**Outcome:** a person can attach an already-trusted GitHub MCP to
*their* account, use it from Hermes, and lose it on disable — without
the operator CLI ritual used in M5.1 proofs.

Minimum slices:

1. **Bind** trusted definition → per-user connection + credential ref
   (owner workspace `credentials.env` or none if credential-free).
2. **Project** only that principal’s tools on the ToolHub MCP endpoint
   (reload on enable/disable; stale session still denied — already in
   SPEC-0019, prove it live).
3. **Start on demand** from the generic controller when Hermes first
   calls; idle stop; image reused.
4. **Revoke/rotate** cuts the next call (gate 3 above can be the seed).
5. Hermes path: `mcp add` / reconnect is #74 — if not ready, M5.2 still
   ships bind/project/revoke through ToolHub; M5.3 owns the chat UX.

Arbitrary MCP stays SHA-pinned import from M5.1; M5.2 does not rebuild
the builder.

## Order of work

```
owner accepts scale model (1)
    → MCP secret probe (2) + image-reuse inspect (4)   [same Docker session]
    → revoke denies call (3)
    → docs/state stamp (5)
    → CHG-0025 → done
    → open CHG-0026 (M5.2 bind/project/revoke)
```

Estimated: one focused implementation session after the scale decision,
not another four-MCP rebuild.
