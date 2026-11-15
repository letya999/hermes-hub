# S7.0 spike: pinned cliproxy contract

Pinned: `router-for-me/CLIProxyAPI` @ `ba7e55836dee959e93ec6d41395865d9ec535086`
(`docker/Dockerfile` cliproxy_build stage), binary `/usr/local/bin/cli-proxy-api`.
Verified against both the pinned source and the live `cliproxy-artem_two` container.

## Auth file layout

- `config.yaml` field `auth-dir` names the session directory (live:
  `/cliproxy/auths`). `sdk/auth/filestore.go` `FileTokenStore` walks that dir
  recursively (`WalkDir`) and loads **every** `*.json` it finds — one file per
  account.
- File name per provider (`internal/auth/<p>/filename.go`):
  `antigravity-<email>.json`, `codex[-<hashAccountID>]-<email>[-<plan>].json`,
  `claude-<email>.json`, `kimi-<email>.json`, `xai-<email>.json`.
- Session JSON keys (observed, values withheld): `access_token`,
  `refresh_token`, `expires_in`, `timestamp`, `type`, `email`, `project_id`,
  `disabled`, `expired`.
- One process can be pinned to one auth file: give the instance its own bind
  (`/cliproxy` per instance) so its `auth-dir` contains exactly that file.
  There is no single-file flag — isolation is directory-level, which our
  per-instance bind already provides.

## Client auth (`api-keys`)

`config.yaml` `api-keys: […]` lists the keys a client must present to the
proxy port (`port: 8317`). Per-instance config => per-instance key set: the
service's `proxy_access` key lives only in its instance config, and the relay
hop carries the matching key.

## Login flow and callback port

- CLI flags (`cmd/server/main.go`): `-antigravity-login`, `-codex-login`,
  `-codex-device-login`, `-claude-login`, `-kimi-login`, `-xai-login`,
  `-vertex-import <file>`; shared `-config`, `-no-browser`,
  `-oauth-callback-port` (0 = provider-specific default port).
- Flow: `Do<Provider>Login(cfg, &LoginOptions{NoBrowser, CallbackPort})` →
  `sdkAuth.Manager.Login` → OAuth callback listener on the callback port →
  `FileTokenStore.Save` writes `<provider>-<email>.json` into `auth-dir`.
- So `hubctl models login` = run `cli-proxy-api -<provider>-login
  -oauth-callback-port <config.ports.oauth-callback>` inside the service's
  instance, then enroll the produced file into Broker and delete the plaintext.

## Decision

Instance-per-service (ADR-0093 D8) confirmed: session pinning is directory
isolation, so each `via: cliproxy` service gets its own container + bind +
`auth-dir` with exactly one session file + its own `api-keys`.
