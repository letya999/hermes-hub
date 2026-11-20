-- spawn-controller.db v1 schema (portable SQLite/Postgres).
-- Every table is tenant-owned (org, owner); fsm entity tables carry
-- id/state/deadline (unix seconds, NULL in final states) and their history
-- tables carry seq/at/from_state/event/to_state, per shared/fsm. Domain
-- columns (space_id, agent, generation, layer, digest) are nullable until
-- the S2+ writers fill them; fsm.Create inserts only the fsm columns.

CREATE TABLE spaces (
    org        TEXT NOT NULL,
    owner      TEXT NOT NULL,
    id         TEXT NOT NULL,
    created_at INTEGER NOT NULL,
    PRIMARY KEY (org, owner, id)
);

CREATE TABLE passport_revisions (
    org      TEXT NOT NULL,
    owner    TEXT NOT NULL,
    id       TEXT NOT NULL,
    space_id TEXT,
    layer    TEXT,
    digest   TEXT,
    state    TEXT NOT NULL,
    deadline INTEGER,
    PRIMARY KEY (org, owner, id)
);
CREATE INDEX passport_revisions_due ON passport_revisions (deadline);

CREATE TABLE passport_revisions_history (
    org        TEXT NOT NULL,
    owner      TEXT NOT NULL,
    id         TEXT NOT NULL,
    seq        INTEGER NOT NULL,
    at         INTEGER NOT NULL,
    from_state TEXT NOT NULL,
    event      TEXT NOT NULL,
    to_state   TEXT NOT NULL,
    PRIMARY KEY (org, owner, id, seq)
);

CREATE TABLE runtimes (
    org        TEXT NOT NULL,
    owner      TEXT NOT NULL,
    id         TEXT NOT NULL,
    space_id   TEXT,
    agent      TEXT,
    generation INTEGER,
    state      TEXT NOT NULL,
    deadline   INTEGER,
    PRIMARY KEY (org, owner, id)
);
CREATE INDEX runtimes_due ON runtimes (deadline);

CREATE TABLE runtimes_history (
    org        TEXT NOT NULL,
    owner      TEXT NOT NULL,
    id         TEXT NOT NULL,
    seq        INTEGER NOT NULL,
    at         INTEGER NOT NULL,
    from_state TEXT NOT NULL,
    event      TEXT NOT NULL,
    to_state   TEXT NOT NULL,
    PRIMARY KEY (org, owner, id, seq)
);

CREATE TABLE leases (
    org        TEXT NOT NULL,
    owner      TEXT NOT NULL,
    id         TEXT NOT NULL,
    runtime_id TEXT NOT NULL,
    created_at INTEGER NOT NULL,
    PRIMARY KEY (org, owner, id)
);
CREATE INDEX leases_runtime ON leases (org, owner, runtime_id);

CREATE TABLE workers (
    org        TEXT NOT NULL,
    owner      TEXT NOT NULL,
    id         TEXT NOT NULL,
    space_id   TEXT,
    agent      TEXT,
    generation INTEGER,
    state      TEXT NOT NULL,
    deadline   INTEGER,
    PRIMARY KEY (org, owner, id)
);
CREATE INDEX workers_due ON workers (deadline);

CREATE TABLE workers_history (
    org        TEXT NOT NULL,
    owner      TEXT NOT NULL,
    id         TEXT NOT NULL,
    seq        INTEGER NOT NULL,
    at         INTEGER NOT NULL,
    from_state TEXT NOT NULL,
    event      TEXT NOT NULL,
    to_state   TEXT NOT NULL,
    PRIMARY KEY (org, owner, id, seq)
);

CREATE TABLE image_pins (
    org        TEXT NOT NULL,
    owner      TEXT NOT NULL,
    id         TEXT NOT NULL,
    digest     TEXT NOT NULL,
    created_at INTEGER NOT NULL,
    PRIMARY KEY (org, owner, id)
);
