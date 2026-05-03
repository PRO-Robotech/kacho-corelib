-- +goose Up

CREATE TABLE operations (
  id            TEXT         PRIMARY KEY,  -- "<domain>_<uuid>" для api-gateway routing
  description   TEXT         NOT NULL,
  created_at    TIMESTAMPTZ  NOT NULL DEFAULT now(),
  created_by    TEXT         NOT NULL DEFAULT 'anonymous',
  modified_at   TIMESTAMPTZ  NOT NULL DEFAULT now(),
  done          BOOLEAN      NOT NULL DEFAULT false,
  metadata_type TEXT,                 -- type_url из Any
  metadata_data BYTEA,                -- value из Any
  resource_id   TEXT,                 -- denorm для filter в List
  error_code    INT,
  error_message TEXT,
  error_details BYTEA,                -- google.rpc.Status.details (Any[])
  response_type TEXT,
  response_data BYTEA
);

CREATE INDEX operations_resource_idx ON operations (resource_id);
CREATE INDEX operations_done_idx ON operations (done);
CREATE INDEX operations_created_at_idx ON operations (created_at);

-- +goose Down

DROP TABLE IF EXISTS operations;
