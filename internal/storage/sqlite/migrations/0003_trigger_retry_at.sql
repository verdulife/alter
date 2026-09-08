-- 0003_trigger_retry_at.sql
-- Adds the RetryAt column for the Scheduler's transient action-retry backoff.
--
-- Contract: RetryAt is OPERATIONAL state, entirely separate from NextFireAt
-- (the derived scheduling deadline). It is non-nil only while a trigger that was
-- due could not be fired because its TriggerAction failed; it holds the earliest
-- time the retry becomes eligible. A completed fire clears it. It is nullable
-- (nil = no retry pending).

ALTER TABLE triggers ADD COLUMN retry_at TEXT;