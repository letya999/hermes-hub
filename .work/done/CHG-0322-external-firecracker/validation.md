# CHG-0322 validation

- [ ] unit: uno client taxonomy (ambiguous/retryable/terminal, idempotent replay)
- [ ] unit: backend fail-closed (Uno down, box failed, verify mismatch)
- [x] live WSL2: spawn -> readyz auth/noauth -> /v1/execute completed —
      2026-11-17, supervisor -> mini-Uno -> FC box -> guest runtime ->
      `run.completed` returning the stub completion through the mux
- [x] live WSL2: guest kernel != host kernel (VM boundary fact) —
      guest `6.18.56` (kata kernel) vs host `6.6.87-WSL2`, verified via
      mini-Uno exec `uname -r`
- [x] live WSL2: state persisted through share disk, survives respawn —
      virtio-fs proved unavailable in stock/kata FC; replaced by per-box
      ext4 virtio-blk image built from the space dir, synced back on
      delete (guest write observed in host space dir after box removal)
- [x] live WSL2: no egress from guest except allowlisted relays — guest
      has zero virtio-net devices (fact); all outbound goes through the
      fc-agent vsock mux which only dials configured proxy targets
- [x] live WSL2: authorization boundary — unauthenticated and wrong-token
      `/readyz` through the host forward both return 401
- [ ] live WSL2: missing/invalid config -> bounded failure, no runc fallback

## Live acceptance evidence (2026-11-17, WSL2)

- `POST /v1/execute` job `fc-job-24`: `status=completed`,
  `run_id=run_94ffa1bbf151406e90b93d934f0c0db4`,
  `text="firecracker runtime received message"` (SSE via model stub).
- Full mount contract observed in guest: `/dev/vdb` at `/persist` plus
  bind-mounts for `/state*`, `/workspace`, `/config`, `/skills`,
  `/run/model-config`, `/run/model-state`, `/run/hosting.key`.
- Request path: supervisor -> mini-Uno forward `127.0.0.1:<port>` -> FC
  vsock -> fc-agent ingress -> guest `127.0.0.1:8080`.
- Control relay runs unpublished on the agent network, dials the box
  vsock UDS (`CONNECT 8080`).
- Guest loopback brought up by fc-agent; runtime launched as
  `/usr/local/bin/hub-runtime serve` (absolute path — detached exec
  drops PATH).
- Relay aliases resolve to guest loopback; mux proxies reach the stub
  (model SSE, MCP toolhub) and the live shared toolhub.
- Known fixture gap: run initially failed `No inference provider
  configured` — the copied space's cliproxy provider is unavailable
  offline; model stub proves the transport, provider wiring stays a
  space-config concern.
