-- spawn-controller.db v1 schema, migration 3: durable runtime lifecycle
-- and the warm pool (S4). spaces.effective_digest is the pointer
-- activation writes and every reader and claim follows — readers never
-- recompile (Decision 11). Runtime rows carry the Decision 10 claim
-- binding (policy and image digests), the backend's workload id and the
-- forwarding address. The partial unique index is the backstop that one
-- (space, agent) pair starts at most one runtime at a time; rows with
-- unset space/agent (a bare fsm row) never collide on NULL.

ALTER TABLE spaces ADD COLUMN effective_digest TEXT NOT NULL DEFAULT '';

ALTER TABLE runtimes ADD COLUMN policy_digest TEXT NOT NULL DEFAULT '';
ALTER TABLE runtimes ADD COLUMN image_digest TEXT NOT NULL DEFAULT '';
ALTER TABLE runtimes ADD COLUMN backend_id TEXT NOT NULL DEFAULT '';
ALTER TABLE runtimes ADD COLUMN address TEXT NOT NULL DEFAULT '';

CREATE UNIQUE INDEX runtimes_one_starting
    ON runtimes (org, owner, space_id, agent)
    WHERE state = 'starting';

CREATE INDEX runtimes_space ON runtimes (org, owner, space_id, agent, state);
