-- 0001_init.sql
-- Initial schema for tasks, triggers and events.

CREATE TABLE IF NOT EXISTS tasks (
    id          TEXT NOT NULL PRIMARY KEY,
    title       TEXT NOT NULL,
    description TEXT NOT NULL DEFAULT '',
    status      TEXT NOT NULL,
    priority    TEXT NOT NULL,
    due_at      TEXT,
    source      TEXT NOT NULL DEFAULT '',
    created_at  TEXT NOT NULL,
    updated_at  TEXT NOT NULL
);

CREATE TABLE IF NOT EXISTS triggers (
    id         TEXT NOT NULL PRIMARY KEY,
    task_id    TEXT NOT NULL REFERENCES tasks(id),
    type       TEXT NOT NULL,
    value      TEXT NOT NULL DEFAULT '',
    enabled    INTEGER NOT NULL DEFAULT 1,
    created_at TEXT NOT NULL
);

CREATE TABLE IF NOT EXISTS events (
    id         TEXT NOT NULL PRIMARY KEY,
    type       TEXT NOT NULL,
    payload    TEXT NOT NULL DEFAULT '{}',
    created_at TEXT NOT NULL
);

CREATE INDEX IF NOT EXISTS idx_tasks_status     ON tasks(status);
CREATE INDEX IF NOT EXISTS idx_tasks_priority   ON tasks(priority);
CREATE INDEX IF NOT EXISTS idx_triggers_task_id ON triggers(task_id);
CREATE INDEX IF NOT EXISTS idx_triggers_enabled ON triggers(enabled);
CREATE INDEX IF NOT EXISTS idx_events_type      ON events(type);
