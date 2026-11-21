-- spawn-controller.db v1 schema, migration 7: durable v0 mirror queue (S7b).
-- Every activation marks its space's v0 file publish pending in the same
-- transaction that commits it: the mirror effect can never be lost between
-- the commit and the file write. mirrorActive clears the row once the
-- generated tree carries the effective digest's revision; the sweeper
-- retries rows a crash left behind (the DB is the truth — a mirror failure
-- never un-activates). Columns match the fixed DueRows projection so the
-- sweeper enumerates pending mirrors across tenants without a scoped read.

CREATE TABLE mirror_pending (
    org      TEXT NOT NULL,
    owner    TEXT NOT NULL,
    id       TEXT NOT NULL,
    state    TEXT NOT NULL DEFAULT 'pending',
    deadline INTEGER NOT NULL,
    PRIMARY KEY (org, owner, id)
);
CREATE INDEX mirror_pending_due ON mirror_pending (deadline);
