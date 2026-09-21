-- Task metadata mirrored from disk. The files under ./tasks are the source of
-- truth; this table exists so that statistics can GROUP BY topic in SQL and so
-- that attempts have something to reference. Tasks that disappear from disk are
-- soft deleted, never removed, otherwise deleting a directory would silently
-- destroy the history that the whole trainer is built around.
CREATE TABLE tasks (
    id            TEXT PRIMARY KEY,
    title         TEXT    NOT NULL,
    topic         TEXT    NOT NULL,
    difficulty    INTEGER NOT NULL CHECK (difficulty BETWEEN 1 AND 3),
    estimate_min  INTEGER NOT NULL CHECK (estimate_min > 0),
    content_hash  TEXT    NOT NULL,
    first_seen_at TEXT    NOT NULL,
    updated_at    TEXT    NOT NULL,
    deleted_at    TEXT
) STRICT;

CREATE INDEX idx_tasks_topic ON tasks (topic) WHERE deleted_at IS NULL;

-- One row per sitting. duration_ms is wall clock from opening the task to the
-- first green run; active_ms only counts time the editor was focused and being
-- typed in, which is the number actually worth plotting.
CREATE TABLE attempts (
    id          INTEGER PRIMARY KEY AUTOINCREMENT,
    task_id     TEXT    NOT NULL REFERENCES tasks (id),
    status      TEXT    NOT NULL CHECK (status IN ('in_progress', 'passed', 'abandoned')),
    started_at  TEXT    NOT NULL,
    finished_at TEXT,
    duration_ms INTEGER,
    active_ms   INTEGER NOT NULL DEFAULT 0,
    day         TEXT    NOT NULL,
    run_count   INTEGER NOT NULL DEFAULT 0,
    fail_count  INTEGER NOT NULL DEFAULT 0
) STRICT;

CREATE INDEX idx_attempts_task ON attempts (task_id, started_at);
CREATE INDEX idx_attempts_day ON attempts (day);

-- At most one open attempt per task, enforced by the schema rather than by a
-- comment in the Go code.
CREATE UNIQUE INDEX idx_attempts_one_open ON attempts (task_id) WHERE status = 'in_progress';

-- Snapshot of the code as it looked when the attempt went green. Needed for the
-- divergence journal, which shows it next to the reference solution.
CREATE TABLE attempt_files (
    attempt_id INTEGER NOT NULL REFERENCES attempts (id) ON DELETE CASCADE,
    path       TEXT    NOT NULL,
    content    TEXT    NOT NULL,
    PRIMARY KEY (attempt_id, path)
) STRICT;

-- Debounced autosave of the editor buffers so a page reload never loses work.
CREATE TABLE drafts (
    task_id    TEXT NOT NULL,
    path       TEXT NOT NULL,
    content    TEXT NOT NULL,
    updated_at TEXT NOT NULL,
    PRIMARY KEY (task_id, path)
) STRICT;

-- Spaced repetition state. due_on is a local calendar date, not UTC: with UTC
-- "today" would roll over at 3am local and the morning session would show
-- yesterday's list.
CREATE TABLE reviews (
    task_id        TEXT    PRIMARY KEY REFERENCES tasks (id),
    stage          INTEGER NOT NULL DEFAULT 0,
    due_on         TEXT    NOT NULL,
    last_passed_at TEXT    NOT NULL,
    pass_count     INTEGER NOT NULL DEFAULT 0
) STRICT;

CREATE INDEX idx_reviews_due ON reviews (due_on);

-- "What did I get wrong" notes, written right after a green run.
CREATE TABLE notes (
    id         INTEGER PRIMARY KEY AUTOINCREMENT,
    attempt_id INTEGER NOT NULL REFERENCES attempts (id) ON DELETE CASCADE,
    task_id    TEXT    NOT NULL,
    body       TEXT    NOT NULL,
    created_at TEXT    NOT NULL,
    updated_at TEXT    NOT NULL
) STRICT;

CREATE UNIQUE INDEX idx_notes_attempt ON notes (attempt_id);
CREATE INDEX idx_notes_created ON notes (created_at DESC);
