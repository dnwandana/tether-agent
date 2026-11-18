-- chat_modes holds the /mode value of a chat or a topic. No row means guarded.
CREATE TABLE chat_modes (
    scope  TEXT PRIMARY KEY,   -- telegram:<chat_id>:<thread_id>, or cli
    mode   TEXT NOT NULL,      -- read-only | full
    set_at INTEGER NOT NULL DEFAULT 0
);

-- approval_log holds one row for each decision of the approval gate on a request that matched
-- a rule, an MCP class other than read, or the floor.
CREATE TABLE approval_log (
    id             INTEGER PRIMARY KEY,
    at             INTEGER NOT NULL,            -- Unix ms
    session_id     TEXT NOT NULL DEFAULT '',
    scope          TEXT NOT NULL DEFAULT '',    -- the /mode scope
    kind           TEXT NOT NULL DEFAULT '',    -- chat, cron, subagent
    tool           TEXT NOT NULL DEFAULT '',    -- terminal, execute_code, mcp:<s>.<t>, acp:<agent>
    server         TEXT NOT NULL DEFAULT '',    -- the MCP server, for an MCP row
    tool_name      TEXT NOT NULL DEFAULT '',    -- the raw MCP tool name, for an MCP row
    rule           TEXT NOT NULL DEFAULT '',    -- the pattern ID or the synthetic rule
    class          TEXT NOT NULL DEFAULT '',    -- the MCP class
    decision       TEXT NOT NULL,               -- allowed, denied, blocked
    source         TEXT NOT NULL,               -- owner, grant, mode-off, cron-mode, and the other sources
    answer         TEXT NOT NULL DEFAULT '',    -- once, session, always (owner answers only)
    content_sha256 TEXT NOT NULL DEFAULT '',
    preview        TEXT NOT NULL DEFAULT ''     -- redacted, at most 300 characters
);
CREATE INDEX approval_log_scope_at ON approval_log(scope, at);
CREATE INDEX approval_log_at ON approval_log(at);
