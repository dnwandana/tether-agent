-- 001_init: the first state.db schema.
--
-- Conventions:
--   * All time columns are INTEGER Unix time in milliseconds (UTC). NULL means "not set".
--   * JSON columns are TEXT that holds a JSON value.
--   * Boolean columns are INTEGER 0 or 1.
--   * No column declares a foreign key. The stores delete related rows in one transaction.

-- sessions holds one row for each conversation. kind is chat, cron, subagent, or review.
CREATE TABLE sessions (
    id              TEXT PRIMARY KEY,
    session_key     TEXT NOT NULL DEFAULT '',
    source          TEXT NOT NULL DEFAULT '',
    chat_id         TEXT NOT NULL DEFAULT '',
    thread_id       TEXT NOT NULL DEFAULT '',
    user_id         TEXT NOT NULL DEFAULT '',
    title           TEXT NOT NULL DEFAULT '',
    model_override  TEXT NOT NULL DEFAULT '',
    prompt_override TEXT NOT NULL DEFAULT '',
    parent_id       TEXT NOT NULL DEFAULT '',
    kind            TEXT NOT NULL DEFAULT 'chat',
    started_at      INTEGER NOT NULL DEFAULT 0,
    last_active     INTEGER NOT NULL DEFAULT 0,
    archived        INTEGER NOT NULL DEFAULT 0
);
CREATE INDEX sessions_key_last_active ON sessions(session_key, last_active);

-- messages holds the transcript. AUTOINCREMENT prevents the reuse of an id after a delete,
-- because compactions and forks refer to message ids.
CREATE TABLE messages (
    id                INTEGER PRIMARY KEY AUTOINCREMENT,
    session_id        TEXT NOT NULL,
    role              TEXT NOT NULL,
    content           TEXT NOT NULL DEFAULT '',
    tool_call_id      TEXT NOT NULL DEFAULT '',
    tool_calls        TEXT,                        -- JSON array, or NULL
    tool_name         TEXT NOT NULL DEFAULT '',
    input_tokens      INTEGER NOT NULL DEFAULT 0,
    output_tokens     INTEGER NOT NULL DEFAULT 0,
    cache_read_tokens INTEGER NOT NULL DEFAULT 0,
    latency_ms        INTEGER NOT NULL DEFAULT 0,  -- total time of the model call
    latency_first_ms  INTEGER NOT NULL DEFAULT 0,  -- time to the first stream byte
    cost_usd          REAL,                        -- NULL when the price is not known
    model             TEXT NOT NULL DEFAULT '',
    provider          TEXT NOT NULL DEFAULT '',
    finish_reason     TEXT NOT NULL DEFAULT '',
    compacted         INTEGER NOT NULL DEFAULT 0,
    observed          INTEGER NOT NULL DEFAULT 0,
    timestamp         INTEGER NOT NULL DEFAULT 0
);
CREATE INDEX messages_session_id ON messages(session_id, id);

-- messages_fts is the full-text index over messages.content (external content).
CREATE VIRTUAL TABLE messages_fts USING fts5(content, content='messages', content_rowid='id');

-- The triggers keep messages_fts in sync. The session delete and the prune depend on messages_ad.
CREATE TRIGGER messages_ai AFTER INSERT ON messages BEGIN
    INSERT INTO messages_fts(rowid, content) VALUES (new.id, new.content);
END;
CREATE TRIGGER messages_ad AFTER DELETE ON messages BEGIN
    INSERT INTO messages_fts(messages_fts, rowid, content) VALUES ('delete', old.id, old.content);
END;
CREATE TRIGGER messages_au AFTER UPDATE OF content ON messages BEGIN
    INSERT INTO messages_fts(messages_fts, rowid, content) VALUES ('delete', old.id, old.content);
    INSERT INTO messages_fts(rowid, content) VALUES (new.id, new.content);
END;

-- compactions holds one summary row for each compaction of a session.
-- The summary replaces the messages from from_message_id to upto_message_id.
CREATE TABLE compactions (
    id              INTEGER PRIMARY KEY AUTOINCREMENT,
    session_id      TEXT NOT NULL,
    from_message_id INTEGER NOT NULL DEFAULT 0,
    upto_message_id INTEGER NOT NULL DEFAULT 0,
    summary         TEXT NOT NULL DEFAULT '',
    created_at      INTEGER NOT NULL DEFAULT 0
);
CREATE INDEX compactions_session_id ON compactions(session_id, id);

-- cron_jobs holds the scheduled jobs. repeat_left -1 means forever.
CREATE TABLE cron_jobs (
    id                 TEXT PRIMARY KEY,
    name               TEXT NOT NULL DEFAULT '',
    schedule           TEXT NOT NULL,
    prompt             TEXT NOT NULL DEFAULT '',
    script             TEXT NOT NULL DEFAULT '',
    no_agent           INTEGER NOT NULL DEFAULT 0,
    deliver            TEXT NOT NULL DEFAULT '',
    skills             TEXT,                     -- JSON array of skill names
    model              TEXT,                     -- JSON {"provider":"","model":""}, or NULL
    toolsets           TEXT,                     -- JSON array of toolset names
    workdir            TEXT NOT NULL DEFAULT '',
    repeat_left        INTEGER NOT NULL DEFAULT -1,
    enabled            INTEGER NOT NULL DEFAULT 1,
    origin             TEXT,                     -- JSON {"source","chat_id","thread_id","session_key"}
    created_by_session TEXT NOT NULL DEFAULT '',
    created_at         INTEGER NOT NULL DEFAULT 0,
    last_run           INTEGER,
    next_run           INTEGER,
    last_status        TEXT NOT NULL DEFAULT '',
    last_output        TEXT NOT NULL DEFAULT ''
);
CREATE INDEX cron_jobs_next_run ON cron_jobs(next_run);

-- approvals_always holds the "Always" answers of the approval gate.
CREATE TABLE approvals_always (
    pattern_id TEXT PRIMARY KEY,
    created_at INTEGER NOT NULL DEFAULT 0
);

-- pairing holds one row for each user that asked for access on a platform.
-- status is pending, approved, or revoked.
CREATE TABLE pairing (
    platform    TEXT NOT NULL,
    user_id     TEXT NOT NULL,
    user_name   TEXT NOT NULL DEFAULT '',
    code        TEXT NOT NULL DEFAULT '',
    status      TEXT NOT NULL DEFAULT 'pending',
    created_at  INTEGER NOT NULL DEFAULT 0,
    expires_at  INTEGER NOT NULL DEFAULT 0,
    approved_at INTEGER,
    attempts    INTEGER NOT NULL DEFAULT 0,
    PRIMARY KEY (platform, user_id)
);
CREATE INDEX pairing_platform_status ON pairing(platform, status);

-- active_session maps a session key to the session that the key uses now.
CREATE TABLE active_session (
    session_key TEXT PRIMARY KEY,
    session_id  TEXT NOT NULL
);

-- delegation_batches holds the delegate_task batches.
-- status is running, done, cancelled, or lost.
CREATE TABLE delegation_batches (
    id                 TEXT PRIMARY KEY,
    parent_session_key TEXT NOT NULL DEFAULT '',
    parent_session_id  TEXT NOT NULL DEFAULT '',
    background         INTEGER NOT NULL DEFAULT 0,
    status             TEXT NOT NULL DEFAULT 'running',
    tasks              TEXT,                     -- JSON array
    results            TEXT,                     -- JSON array
    created_at         INTEGER NOT NULL DEFAULT 0,
    finished_at        INTEGER
);
CREATE INDEX delegation_batches_status ON delegation_batches(status);

-- kv holds small values: the Telegram update offset, the home chat and thread, price ETags.
CREATE TABLE kv (
    key   TEXT PRIMARY KEY,
    value TEXT NOT NULL
);
