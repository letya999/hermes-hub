-- S7d deletes the transitional v0 file mirror: activation never writes
-- generated/passports again, so the durable retry marker for a lost
-- publish has no writer and no sweeper left.
DROP TABLE mirror_pending;
