-- spawn-controller.db v1 schema, migration 8: v0 mirror backfill (S7b).
-- Spaces whose revision set was activated before the mirror queue existed
-- (importer, S7a) carry effective_digest but no mirror_pending row and no
-- published .policy link — the sweeper had nothing to retry and v0 readers
-- could never catch up. Enqueue one immediately-due pending row per active
-- space so the first sweep republishes every missing mirror from the DB
-- truth. NOT EXISTS keeps the backfill idempotent and dialect-neutral.

INSERT INTO mirror_pending (org, owner, id, state, deadline)
SELECT s.org, s.owner, s.id, 'pending', 0
FROM spaces s
WHERE s.effective_digest != ''
  AND NOT EXISTS (
      SELECT 1 FROM mirror_pending mp
      WHERE mp.org = s.org AND mp.owner = s.owner AND mp.id = s.id
  );
