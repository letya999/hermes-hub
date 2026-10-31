# CHG-0240 evidence — 2026-10-31

Only fixed category counts and byte totals are recorded; no auth data,
credential dictionary names, file names from private storage or audit records.

- Before this pass: 30 spaces, 504 `.log/.old/.bak/.tmp` files (44,305,029
  bytes) and six diagnostic files (about 84.7 MB). Three local ciphertext
  stores, two local keys, 17 secrets env files, two Telegram auth env files
  and 39 proxy auth files were present.
- Registry inventory: 102 current Broker credential references and 23 current
  local references. All 23 local references are bound to active connections;
  none has a reviewed Broker credential contract or a Telegram-named
  definition. The built-in two-phase `credentials migrate` cannot move them.
  Active local ciphertext/key files and Broker data remain intact.
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
