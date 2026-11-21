-- 0009_runtime_claims: the managed-runtime claim binding (CHG-0336 S7c).
-- A claim's row now binds the routing identity it was spawned under:
-- runtime_id and runtime_mode ride into the workload's environment and
-- the identity revision the caller's template resolved to. A warm row is
-- only reused when every bound key still matches, so an identity rotation
-- or a different runtime id can never inherit a previous generation's
-- workload. host_port records the loopback-published control endpoint the
-- docker backend chose for the supervisor's forwarding.
ALTER TABLE runtimes ADD COLUMN runtime_id TEXT NOT NULL DEFAULT '';
ALTER TABLE runtimes ADD COLUMN runtime_mode TEXT NOT NULL DEFAULT '';
ALTER TABLE runtimes ADD COLUMN identity_revision TEXT NOT NULL DEFAULT '';
ALTER TABLE runtimes ADD COLUMN host_port INTEGER NOT NULL DEFAULT 0;
