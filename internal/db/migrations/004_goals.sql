-- goals holds the /goal state of a session. state is the JSON of goals.State, in the Hermes
-- shape, so its own times are Unix seconds as floats.
CREATE TABLE goals (
    session_id TEXT PRIMARY KEY,
    state      TEXT NOT NULL,
    revision   INTEGER NOT NULL,
    updated_at INTEGER NOT NULL
);
ALTER TABLE turn_markers ADD COLUMN in_step INTEGER NOT NULL DEFAULT 0;
