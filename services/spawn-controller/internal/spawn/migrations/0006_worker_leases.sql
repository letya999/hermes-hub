-- migration 6: worker lease expiry and absent-row retention (S6 fix-up).
--
-- expires_at = 0 means "never expires": runtime leases keep that default.
-- Worker leases carry a real expiry; the lease sweep deletes expired rows
-- and unleases the worker once none remain.
ALTER TABLE leases ADD COLUMN expires_at INTEGER NOT NULL DEFAULT 0;
