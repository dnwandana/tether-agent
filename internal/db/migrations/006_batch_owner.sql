-- owner is 1 when an owner turn started the batch, and 0 for a member turn. The result
-- turn and the lost note run with this flag. A row from an older version is an owner row.
ALTER TABLE delegation_batches ADD COLUMN owner INTEGER NOT NULL DEFAULT 1;
