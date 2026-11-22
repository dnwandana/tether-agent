-- dashboard_sessions holds the signed-in devices of the web dashboard. A session has a
-- random cookie value and a random header value. Only the SHA-256 hex of each value is stored.
CREATE TABLE dashboard_sessions (
  id          TEXT PRIMARY KEY,         -- 16 random bytes, hex
  cookie_hash TEXT NOT NULL UNIQUE,
  header_hash TEXT NOT NULL,
  label       TEXT NOT NULL,            -- the device label that the page sent
  user_agent  TEXT NOT NULL,
  ip          TEXT NOT NULL,            -- the peer IP at sign-in
  created_at  INTEGER NOT NULL,         -- Unix ms
  last_seen   INTEGER NOT NULL,         -- Unix ms, written at most once every 5 minutes
  expires_at  INTEGER NOT NULL,         -- Unix ms
  revoked     INTEGER NOT NULL DEFAULT 0
);
-- dashboard_codes holds the one-time codes of "tether dashboard link". A code works once.
CREATE TABLE dashboard_codes (
  code_hash  TEXT PRIMARY KEY,          -- SHA-256 hex of the 8 code characters, upper case
  expires_at INTEGER NOT NULL           -- Unix ms
);
