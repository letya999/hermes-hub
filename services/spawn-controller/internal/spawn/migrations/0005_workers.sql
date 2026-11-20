-- spawn-controller.db v1 schema, migration 5: durable worker lifecycle
-- (S6). Worker rows carry the claim binding — cell key and image digest —
-- plus the backend's workload id, forwarding address and the canary nonce
-- the reuse guard re-verifies. The partial unique index is the backstop
-- that one (space, cell key, image) pair starts at most one worker at a
-- time, mirroring runtimes_one_starting.

ALTER TABLE workers ADD COLUMN cell_key TEXT NOT NULL DEFAULT '';
ALTER TABLE workers ADD COLUMN image_digest TEXT NOT NULL DEFAULT '';
ALTER TABLE workers ADD COLUMN backend_id TEXT NOT NULL DEFAULT '';
ALTER TABLE workers ADD COLUMN address TEXT NOT NULL DEFAULT '';
ALTER TABLE workers ADD COLUMN canary TEXT NOT NULL DEFAULT '';
-- The claim that built the row, credential values stripped: the reuse
-- guard rebuilds the spec from it to re-verify the live container
-- profile, and the drain selector filters on binding_id/job_id.
ALTER TABLE workers ADD COLUMN claim TEXT NOT NULL DEFAULT '';
ALTER TABLE workers ADD COLUMN binding_id TEXT NOT NULL DEFAULT '';
ALTER TABLE workers ADD COLUMN job_id TEXT NOT NULL DEFAULT '';

CREATE UNIQUE INDEX workers_one_starting
    ON workers (org, owner, space_id, cell_key, image_digest)
    WHERE state = 'starting';

CREATE INDEX workers_space ON workers (org, owner, space_id, cell_key, state);
