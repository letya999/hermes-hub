-- spawn-controller.db v1 schema, migration 2: layered passport revisions.
-- A revision row stores one submitted layer's six documents verbatim plus
-- its content digest; base_revision is the activation CAS anchor (the
-- active revision the row was validated against, or the revision that
-- superseded it — the live pointer chain). error carries the locale key
-- of a rejection, detail the compiler diagnostic. The partial unique
-- index is the at-most-one-active-per-layer backstop (SQLite/Postgres).

ALTER TABLE spaces ADD COLUMN environment TEXT NOT NULL DEFAULT 'dev';

ALTER TABLE passport_revisions ADD COLUMN documents TEXT;
ALTER TABLE passport_revisions ADD COLUMN base_revision TEXT NOT NULL DEFAULT '';
ALTER TABLE passport_revisions ADD COLUMN error TEXT NOT NULL DEFAULT '';
ALTER TABLE passport_revisions ADD COLUMN detail TEXT NOT NULL DEFAULT '';
ALTER TABLE passport_revisions ADD COLUMN created_at INTEGER NOT NULL DEFAULT 0;

CREATE UNIQUE INDEX passport_revisions_one_active
    ON passport_revisions (org, owner, space_id, layer)
    WHERE state = 'active';

CREATE INDEX passport_revisions_space ON passport_revisions (org, owner, space_id, layer, state, created_at);

-- effective_specs holds the compiled effective bundle per space, keyed by
-- its own content digest so identical activations share one immutable row
-- (insert-if-absent); policy slices look rows up by digest.
CREATE TABLE effective_specs (
    org        TEXT NOT NULL,
    owner      TEXT NOT NULL,
    id         TEXT NOT NULL,
    space_id   TEXT NOT NULL,
    documents  TEXT NOT NULL,
    created_at INTEGER NOT NULL,
    PRIMARY KEY (org, owner, id)
);
CREATE INDEX effective_specs_space ON effective_specs (org, owner, space_id);
