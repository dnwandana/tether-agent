-- Keep the count of the old rule-wide rows that /approvals showed, for the notice.
INSERT OR REPLACE INTO kv(key, value)
  SELECT 'approvals_always_removed', CAST(COUNT(*) AS TEXT) FROM approvals_always
  WHERE pattern_id NOT LIKE 'protected_path_%' AND pattern_id <> 'sqlite_client_state_db'
  HAVING COUNT(*) > 0;
DROP TABLE approvals_always;

-- approval_grants holds the "Always" answers of the approval gate. A row covers one rule for
-- one exact command text in one workdir, run by the same programs.
CREATE TABLE approval_grants (
    id             TEXT NOT NULL PRIMARY KEY, -- sha256 hex of pattern_id, content_sha256, workdir, via
    pattern_id     TEXT NOT NULL,
    content_sha256 TEXT NOT NULL,             -- sha256 hex of the command bytes, or the MCP config hash
    workdir        TEXT NOT NULL DEFAULT '',
    via            TEXT NOT NULL DEFAULT '',
    programs       TEXT NOT NULL DEFAULT '[]', -- JSON list of the real paths, in Cmds order
    preview        TEXT NOT NULL DEFAULT '',  -- redacted, one line, at most 60 characters
    server         TEXT NOT NULL DEFAULT '',  -- the MCP server, for an MCP row
    tool_name      TEXT NOT NULL DEFAULT '',  -- the raw MCP tool name, for an MCP row
    created_at     INTEGER NOT NULL DEFAULT 0
);
