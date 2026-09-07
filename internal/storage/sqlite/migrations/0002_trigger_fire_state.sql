-- 0002_trigger_fire_state.sql
-- Adds execution bookkeeping columns to triggers for a future Scheduler.
-- Both are nullable: NextFireAt is nil until computed, LastFiredAt is nil
-- until the trigger fires for the first time.

ALTER TABLE triggers ADD COLUMN next_fire_at  TEXT;
ALTER TABLE triggers ADD COLUMN last_fired_at TEXT;