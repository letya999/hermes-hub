-- spawn-controller.db v1 schema, migration 4: importer + explain (S5).
-- effective_specs.attribution is the per-leaf layer provenance the
-- activating compile recorded once for this digest — the explain surface
-- reads it, it never recomputes. backups is the durable backup ledger moved
-- off v0 backups.json: space backups stay owned by this service.

ALTER TABLE effective_specs ADD COLUMN attribution TEXT NOT NULL DEFAULT '{}';

CREATE TABLE backups (
    org             TEXT NOT NULL,
    owner           TEXT NOT NULL,
    id              TEXT NOT NULL,
    last_success_at INTEGER NOT NULL DEFAULT 0,
    last_error      TEXT NOT NULL DEFAULT '',
    last_archive    TEXT NOT NULL DEFAULT '',
    retained        INTEGER NOT NULL DEFAULT 0,
    last_drill_at   INTEGER NOT NULL DEFAULT 0,
    drill_result    TEXT NOT NULL DEFAULT '',
    drill_checked   INTEGER NOT NULL DEFAULT 0,
    PRIMARY KEY (org, owner, id)
);
