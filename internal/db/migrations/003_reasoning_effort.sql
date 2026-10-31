-- reasoning is the JSON of provider.Reasoning for an assistant message, or NULL.
ALTER TABLE messages ADD COLUMN reasoning TEXT;
-- effort is the reasoning effort that /effort set for the session. '' uses the config.
ALTER TABLE sessions ADD COLUMN effort TEXT NOT NULL DEFAULT '';
