-- Persist text delivery before retrying a task's spoken result.
-- +goose Up
ALTER TABLE voice_tasks ADD COLUMN result_event_id text NOT NULL DEFAULT '';

-- +goose Down
ALTER TABLE voice_tasks DROP COLUMN result_event_id;
