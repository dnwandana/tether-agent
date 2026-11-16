-- cron_runs holds the last runs of each cron job. cron.run_history sets the count.
CREATE TABLE cron_runs (
    id             INTEGER PRIMARY KEY,
    job_id         TEXT NOT NULL,
    started_at     INTEGER NOT NULL,          -- Unix ms
    finished_at    INTEGER NOT NULL,          -- Unix ms
    status         TEXT NOT NULL,             -- ok | error | stopped
    delivery       TEXT NOT NULL,             -- sent | silent | none | local | failed
    output         TEXT NOT NULL DEFAULT '',  -- the run text, cut to 4000 characters
    delivery_error TEXT NOT NULL DEFAULT '',
    session_id     TEXT NOT NULL DEFAULT ''   -- the cron session of an agent run
);
CREATE INDEX cron_runs_job ON cron_runs(job_id, id);

-- fail_streak counts the runs in a row with status error.
ALTER TABLE cron_jobs ADD COLUMN fail_streak INTEGER NOT NULL DEFAULT 0;
