-- +goose Up

CREATE SEQUENCE IF NOT EXISTS resource_version_seq;

CREATE TABLE resource_events (
  resource_version BIGINT      PRIMARY KEY DEFAULT nextval('resource_version_seq'),
  event_type       TEXT        NOT NULL CHECK (event_type IN ('ADDED', 'MODIFIED', 'DELETED')),
  resource_kind    TEXT        NOT NULL,
  resource_uid     UUID        NOT NULL,
  data             BYTEA,
  created_at       TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE INDEX resource_events_kind_rv_idx ON resource_events (resource_kind, resource_version);
CREATE INDEX resource_events_cleanup_idx ON resource_events (created_at);

-- Вспомогательная функция для ресурсных таблиц: поднимает resource_version при UPDATE.
-- +goose StatementBegin
CREATE OR REPLACE FUNCTION bump_resource_version() RETURNS TRIGGER AS $$
BEGIN
  NEW.resource_version := nextval('resource_version_seq');
  RETURN NEW;
END;
$$ LANGUAGE plpgsql;
-- +goose StatementEnd

-- +goose Down

-- +goose StatementBegin
DROP FUNCTION IF EXISTS bump_resource_version();
-- +goose StatementEnd
DROP TABLE IF EXISTS resource_events;
DROP SEQUENCE IF EXISTS resource_version_seq;
