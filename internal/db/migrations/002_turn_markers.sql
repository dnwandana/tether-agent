CREATE TABLE turn_markers (
    session_key TEXT PRIMARY KEY,
    session_id  TEXT NOT NULL,
    updated_at  INTEGER NOT NULL,
    resumes     INTEGER NOT NULL DEFAULT 0,
    queued      TEXT NOT NULL DEFAULT '[]'
);
