-- acp_sessions holds the ACP session of each Tether session and coding agent, so that a
-- later call can resume it after an idle stop or a restart.
CREATE TABLE acp_sessions (
    session_id     TEXT NOT NULL,
    agent          TEXT NOT NULL,
    acp_session_id TEXT NOT NULL,
    cwd            TEXT NOT NULL,
    created_at     INTEGER NOT NULL DEFAULT 0,
    updated_at     INTEGER NOT NULL DEFAULT 0,
    PRIMARY KEY (session_id, agent)
);
