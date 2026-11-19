-- held_sends holds the cron deliveries that model_guard held, and the original text of a masked
-- cron delivery. A row lives 24 h. A tap or the sweep of the scheduler deletes it.
CREATE TABLE held_sends (
  id           TEXT PRIMARY KEY,
  kind         TEXT NOT NULL,      -- 'hold' (model_guard) or 'original' (a masked delivery)
  job_id       TEXT NOT NULL,
  run_id       INTEGER,            -- the cron_runs row
  target       TEXT NOT NULL,      -- source:chat[:thread]
  target_title TEXT NOT NULL,
  text         TEXT NOT NULL,
  reason       TEXT NOT NULL,
  sources      TEXT NOT NULL,      -- JSON list
  card_chat    TEXT NOT NULL,
  card_msg_id  INTEGER NOT NULL,
  created_at   INTEGER NOT NULL    -- Unix ms
);
CREATE INDEX held_sends_job ON held_sends(job_id);
