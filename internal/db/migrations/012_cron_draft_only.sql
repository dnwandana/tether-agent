-- draft_only is 1 for a job that can read and write drafts but cannot send.
ALTER TABLE cron_jobs ADD COLUMN draft_only INTEGER NOT NULL DEFAULT 0;
