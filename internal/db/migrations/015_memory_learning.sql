-- memory_edits holds one row for each change of a memory file or a memory note.
CREATE TABLE memory_edits (
  id         INTEGER PRIMARY KEY AUTOINCREMENT,
  stamp      TEXT NOT NULL DEFAULT '',    -- 4 random hex characters
  at         INTEGER NOT NULL DEFAULT 0,
  target     TEXT NOT NULL DEFAULT '',    -- memory, user, rule, or note:<topic>
  idx        INTEGER NOT NULL DEFAULT -1, -- the index of the entry before the change, or -1
  dest       TEXT NOT NULL DEFAULT '',    -- for move and expire: the destination
  action     TEXT NOT NULL DEFAULT '',    -- add, replace, remove, move, expire, undo, redo
  old_text   TEXT NOT NULL DEFAULT '',
  new_text   TEXT NOT NULL DEFAULT '',
  source     TEXT NOT NULL DEFAULT '',    -- chat, review, tidy, owner, cron, expiry
  session_id TEXT NOT NULL DEFAULT '',
  quote      TEXT NOT NULL DEFAULT '',
  outside    TEXT NOT NULL DEFAULT '',    -- the outside sources, comma-separated
  group_id   INTEGER,                     -- the id of the first row of a two-row change
  undone_by  INTEGER                      -- the id of the undo row
);
CREATE INDEX memory_edits_group ON memory_edits(group_id);
-- memory_blocks holds the entries that the owner deleted.
CREATE TABLE memory_blocks (
  id        INTEGER PRIMARY KEY AUTOINCREMENT,
  at        INTEGER NOT NULL DEFAULT 0,
  text_norm TEXT NOT NULL DEFAULT '',
  text      TEXT NOT NULL DEFAULT '',
  edit_id   INTEGER NOT NULL DEFAULT 0
);
CREATE INDEX memory_blocks_norm ON memory_blocks(text_norm);
CREATE INDEX memory_blocks_edit ON memory_blocks(edit_id);
-- memory_held holds the memory writes that wait for the owner.
CREATE TABLE memory_held (
  id          INTEGER PRIMARY KEY AUTOINCREMENT,
  stamp       TEXT NOT NULL DEFAULT '',
  at          INTEGER NOT NULL DEFAULT 0,
  expires_at  INTEGER NOT NULL DEFAULT 0,
  op          TEXT NOT NULL DEFAULT '',   -- JSON of the memory op
  source      TEXT NOT NULL DEFAULT '',
  session_id  TEXT NOT NULL DEFAULT '',
  session_key TEXT NOT NULL DEFAULT '',
  sources     TEXT NOT NULL DEFAULT '',
  state       TEXT NOT NULL DEFAULT 'held' -- held, saved, dropped, swept
);
CREATE INDEX memory_held_state ON memory_held(state, expires_at);
-- notices holds the change cards and their state.
CREATE TABLE notices (
  id          INTEGER PRIMARY KEY AUTOINCREMENT,
  stamp       TEXT NOT NULL DEFAULT '',
  at          INTEGER NOT NULL DEFAULT 0,
  session_key TEXT NOT NULL DEFAULT '',
  text        TEXT NOT NULL DEFAULT '',
  items       TEXT NOT NULL DEFAULT '[]', -- JSON list of {"kind","id"}
  state       TEXT NOT NULL DEFAULT 'open' -- open, undone, kept
);
CREATE INDEX notices_state ON notices(state, at);
-- skill_reads holds one row for each read of a skill.
CREATE TABLE skill_reads (
  id         INTEGER PRIMARY KEY AUTOINCREMENT,
  name       TEXT NOT NULL DEFAULT '',
  at         INTEGER NOT NULL DEFAULT 0,
  source     TEXT NOT NULL DEFAULT '',    -- view, command, cron
  session_id TEXT NOT NULL DEFAULT ''
);
CREATE INDEX skill_reads_name ON skill_reads(name, at);
-- skill_flags holds the curator state of each skill.
CREATE TABLE skill_flags (
  id            INTEGER PRIMARY KEY AUTOINCREMENT,
  name          TEXT NOT NULL UNIQUE,
  rel_dir       TEXT NOT NULL DEFAULT '',
  kept          INTEGER NOT NULL DEFAULT 0,
  first_seen    INTEGER NOT NULL DEFAULT 0,
  archived_at   INTEGER,
  archived_path TEXT NOT NULL DEFAULT ''
);
-- skill_copies holds one row for each copy of a skill folder before a change.
CREATE TABLE skill_copies (
  id         INTEGER PRIMARY KEY AUTOINCREMENT,
  stamp      TEXT NOT NULL DEFAULT '',
  name       TEXT NOT NULL DEFAULT '',
  rel_dir    TEXT NOT NULL DEFAULT '',
  at         INTEGER NOT NULL DEFAULT 0,
  action     TEXT NOT NULL DEFAULT '',    -- create, patch, write_file, delete, undo, redo
  source     TEXT NOT NULL DEFAULT '',    -- chat, review, cron, child, owner
  session_id TEXT NOT NULL DEFAULT '',
  path       TEXT NOT NULL DEFAULT '',
  hash_after TEXT NOT NULL DEFAULT '',
  undone_by  INTEGER
);
CREATE INDEX skill_copies_name ON skill_copies(name, id);
-- messages.outside holds the outside sources of the row, comma-separated.
ALTER TABLE messages ADD COLUMN outside TEXT NOT NULL DEFAULT '';
-- messages.member is 1 when a member, not an owner, sent the user row.
ALTER TABLE messages ADD COLUMN member INTEGER NOT NULL DEFAULT 0;
