-- last_error is the redacted text of the last failure alert that went out. last_error_key is
-- its key. A failure with the same key sends no new alert.
ALTER TABLE cron_jobs ADD COLUMN last_error TEXT NOT NULL DEFAULT '';
ALTER TABLE cron_jobs ADD COLUMN last_error_key TEXT NOT NULL DEFAULT '';
-- pause_reason tells why the scheduler disabled the job: '', 'failures', or 'schedule'.
ALTER TABLE cron_jobs ADD COLUMN pause_reason TEXT NOT NULL DEFAULT '';
-- watch is 1 for a job that wakes the model only when the script output changes.
ALTER TABLE cron_jobs ADD COLUMN watch INTEGER NOT NULL DEFAULT 0;
-- watch_hash is the SHA-256 of the baseline stdout. watch_output is the baseline stdout, cut at
-- 8000 runes. NULL means no baseline.
ALTER TABLE cron_jobs ADD COLUMN watch_hash TEXT;
ALTER TABLE cron_jobs ADD COLUMN watch_output TEXT;
-- continuity is 1 for a job whose prompt gets the output of its last delivered run.
ALTER TABLE cron_jobs ADD COLUMN continuity INTEGER NOT NULL DEFAULT 0;
-- effort is the reasoning effort of the runs of the job. '' uses the config.
ALTER TABLE cron_jobs ADD COLUMN effort TEXT NOT NULL DEFAULT '';
-- cost_usd is the cost of the run. cost_median is the median of the job when cost_flag is 1.
ALTER TABLE cron_runs ADD COLUMN cost_usd REAL;
ALTER TABLE cron_runs ADD COLUMN cost_median REAL;
-- cost_flag is 1 when the run cost more than 3 times the median of the job.
ALTER TABLE cron_runs ADD COLUMN cost_flag INTEGER NOT NULL DEFAULT 0;
-- The jobs that the fail streak paused before this migration show in /brief.
UPDATE cron_jobs SET pause_reason = 'failures'
  WHERE enabled = 0 AND fail_streak > 0 AND last_output LIKE '%is paused: it failed%';
-- The jobs that a bad stored schedule paused before this migration show in /brief.
UPDATE cron_jobs SET pause_reason = 'schedule'
  WHERE enabled = 0 AND last_output LIKE '%is paused: its stored schedule % is not valid.%';
