# CHG-0240 evidence — 2026-10-31

Only fixed category counts and byte totals are recorded; no auth data,
credential dictionary names, file names from private storage or audit records.

- Before this pass: 30 spaces, 504 `.log/.old/.bak/.tmp` files (44,305,029
  bytes) and six diagnostic files (about 84.7 MB). Three local ciphertext
  stores, two local keys, 17 secrets env files, two Telegram auth env files
  and 39 proxy auth files were present.
- Corrected registry inventory: 102 historical Broker credential references
  and 23 historical local references. Only 18 Broker and three local
  references belong to active connections. An earlier inventory parser omitted
  the `credential_ref` JSON tag and incorrectly classified all 23 local
  references as active; that conclusion is withdrawn. The built-in two-phase
  `credentials migrate` was not used. Local ciphertext/key files and Broker
  data remained intact at this stage.
- No files were present under the legacy Hermes session or memory directories.
  Telegram Broker/proxy auth material was retained without inspecting values.
- A digest-bound one-off cleanup removed 330 aged ignored files outside
  `spaces/local` (25,425,530 bytes). It refused symlinks, tracked files and
  `.old/.bak` entries without a present unsuffixed counterpart. Old proxy auth
  backup copies disappeared; active primary files remained. Re-inventory
  found zero old-suffix files in dormant spaces; 200 remain in active spaces.
- Of 23 legacy `settings.yaml` spaces, 22 parsed successfully and had no split
  pair. For each, the split pair was written, re-read and compared for exact
  `Settings` equality before deleting the legacy file. All 22 migrated; one
  dormant config remains unreadable because of an unknown field and its space
  contains two auth files. Its source was retained. The active local space
  passed `hubctl doctor` and `hubctl render` after migration; ToolHub,
  Communication Hub and Broker containers remained running.
- Automatic approval review had twice rejected direct deletion of 101 older
  ignored host files under `spaces/local` with `blocked by policy` despite
  explicit owner authorization. This pass excluded that path rather than
  rerouting the same deletion.
- Full `go run ./cmd/e2e mock --scope core`: 151 scenarios, 122 PASS,
  29 BLOCKED for fixtures requiring additional evidence, 0 FAIL. The runner
  exited 1 because BLOCKED scenarios are incomplete. This is local mock
  evidence, not live Telegram or provider acceptance.
- First `just check` attempt failed when Windows could not `fork/exec` the
  locale and supervisor test processes under concurrent gate load. Both
  packages passed separately with Go parallelism limited to one. A second
  parallel `just check` encountered the same Windows launch failure in
  `cmd/supervisor` and `internal/e2e` and was stopped after those failures.
  All four `just check` legs then passed sequentially: `go-check` with own Go
  statement coverage 86.23% (floor 85%), `lint`, `docs-check`, and
  `config-check`. The full parallel `just check` did not pass on this host.
- Post-cleanup local ToolHub, Communication Hub and Broker containers and
  both dedicated owner proxies remained running. ToolHub and Communication
  Hub HTTP endpoints responded after the active config render.
- Direct Docker inventory found 137 total volumes, 127 Hermes-named. Mounts
  were checked across all 22 containers, including stopped containers; live
  Compose volume declarations were also compared. A digest-bound exact-name
  removal deleted 106 unused, unreferenced Hermes volumes, then three more
  unused legacy Broker/ToolHub volumes. No broad volume/system prune was used.
  Re-inventory found zero unused Hermes volumes; 28 Docker volumes remained
  overall. Attached current Broker state/recovery and support volumes stayed.
- Before the active gateway reset, pending/running jobs and pending/sending
  deliveries were all zero. The exact `/data` volume attached to only the
  local dev Communication Hub was checked by name, then its container was
  stopped/removed and the volume deleted directly via Docker. `hubctl
  dev-rebuild` only restored the required dev service image and Compose
  configuration. The new volume contained three bootstrap files / 42 bytes
  instead of 4,225 files / 4,598,160 bytes. The running image matched the
  rebuilt dev tag, the authenticated tasks route returned 401 without a
  bearer, and the local ToolHub and Broker remained up.
- The 101 auto-review-blocked `spaces/local` files are host bind data, not
  Docker volumes. Volume deletion cannot remove them. No Docker bind-mount
  workaround was used for that rejected action.
- After the owner's all-space external connector retirement instruction, the
  corrected inventory found 21 active MCP connections, 73 active external
  CLI/MCP bindings and six active system bindings in two registries. All
  connector-labelled containers and volumes were already absent.
- Broker control API received 49 unique credential IDs from registry and
  onboarding state: 24 revoked and deleted, 25 already absent, zero failed.
  Two additional historical references lacked enough owner identity to make
  an authorized Broker call; their backend existence could not be established.
  No credential values or dictionary keys were emitted.
- ToolHub was stopped for registry replacement. Both replacement snapshots
  passed `toolhub.Load` validation before apply. The two registries initially
  shrank from 15,378,029 to 2,002,932 bytes. All external connections,
  references, bindings, workloads, onboarding and operations were removed;
  six active system bindings remained. Eight CLI definitions were still
  pinned by old capability profile revisions at that point.
- MCP/CLI wishes were removed from 28 spaces (29 config files, 29 entries).
  Post-change inventory found zero MCP wishes, CLI registries, external
  connections, external bindings and external credential references in the
  readable spaces. The one previously unreadable dormant config remains.
- Exact project-owned cleanup removed 23 ignored file/directory targets,
  comprising 164 files and 16,464,998,273 bytes: connector artifacts,
  encrypted local stores and historical registry/audit copies. No symlink or
  tracked path was selected. ToolHub recreated an empty ciphertext store on
  restart (zero records). All 23 `hermes-artifact/*` image tags were removed
  without force; zero connector-labelled containers/volumes remain.
- ToolHub restarted and remained running. Unauthenticated `/mcp` returned
  401 as expected; `hubctl doctor --dir spaces/local --env dev` reported a
  valid configuration. The Telegram bot and core model relay credentials were
  not part of this connector retirement.
- Direct external integration fields were removed from 15 source env files
  (385 entries, including empty placeholders), then 31 root-level rendered
  env files (547 entries). All 17 source env files now have 19 populated core
  fields and zero populated external fields. Running containers were checked
  by allowlisted aggregate counts; two model relays initially retained three
  external fields. Both were recreated with `hubctl dev-rebuild`, became
  healthy on their dev images, and the running-container count fell to zero.
- Twenty-eight browser-profile directories containing 4,569 files and
  1,170,331,649 bytes were retired after workspace, symlink and tracked-file
  checks. Two stopped orphan browser containers were removed by exact name.
  No browser volume remained.
- `hubctl render` and `doctor` passed for all three running dev spaces after
  env cleanup. Render migrated three system bindings to current tool policy
  versions and reported two stale managed profiles. Both profiles were
  previewed and republished through the confirmed host capability command.
  A third current profile with two external CLI selections was similarly
  previewed and republished without those selections.
- A second offline registry pass retained only current/referenced capability
  policy revisions and current profiles/grants. Dry-run loaded both pruned
  registries and compared every active managed catalog before/after; they
  matched. Apply reduced the stores from 3,294,581 to 813,660 bytes, removed
  all eight remaining CLI definitions and reduced 102 capability changes to
  29. ToolHub restarted. Final inventory: zero external definitions,
  connections, references, bindings, MCP wishes, CLI registries, artifact
  images, labelled connector containers or volumes. All 19 system
  definitions and six active system bindings remained. Five active managed
  profiles resolved successfully; four had nonempty system catalogs, with
  zero external catalog entries.
- `go run ./cmd/e2e mock --scope core` was repeated after connector cleanup:
  151 scenarios, 122 PASS, 29 BLOCKED for fixtures needing further evidence,
  zero FAIL. Its exit 1 means the suite is incomplete; mock results are not
  provider or Telegram acceptance.
- `just check` passed as a whole on this pass; own Go statement coverage was
  86.23% against the 85% floor. The three running dev spaces passed `doctor`.
- The first live authenticated MCP `tools/list` passed for local but found a
  shared-registry policy mismatch for two sibling tokens: HTTP 503. The
  local ToolHub registry held their profiles and active system bindings at
  older policy versions even though the sibling tokens had current hashes.
  `toolhub.MigrateProjection` moved two active system bindings; both shared
  profiles were previewed and republished through the confirmed host command.
  Exact token/profile scope and policy checks then passed. Live MCP
  `tools/list` passed for local, artem_main and artem_two with 15, 95 and 95
  tools respectively. This tests the live projection, not external provider
  execution or Telegram delivery.
