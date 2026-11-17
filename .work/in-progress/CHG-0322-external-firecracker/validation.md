# CHG-0322 validation

- [ ] unit: uno client taxonomy (ambiguous/retryable/terminal, idempotent replay)
- [ ] unit: backend fail-closed (Uno down, box failed, verify mismatch)
- [ ] live WSL2: spawn -> readyz auth/noauth -> /v1/execute completed
- [ ] live WSL2: guest kernel != host kernel (VM boundary fact)
- [ ] live WSL2: state persisted through virtio-fs share, survives respawn
- [ ] live WSL2: no egress from guest except allowlisted relays (fact)
- [ ] live WSL2: missing/invalid config -> bounded failure, no runc fallback
