-- Copyright (c) PRO-Robotech
-- SPDX-License-Identifier: BUSL-1.1

-- +goose Up

-- Добавить principal-поля в operations.
-- Без auth-контекста заполняется stub'ом 'system'/'bootstrap'/'System' через
-- DEFAULT — это же значение использует operations.SystemPrincipal() и
-- operations.PrincipalFromContext(emptyCtx). При включенном auth
-- api-gateway пробрасывает реального principal'а через
-- operations.WithPrincipal -> use-case -> repo.CreateWithPrincipal.
--
-- NOT NULL DEFAULT работает на ALTER TABLE — Postgres back-fill'ит
-- существующие строки атомарно (Postgres 11+).

ALTER TABLE operations
  ADD COLUMN principal_type         TEXT NOT NULL DEFAULT 'system',
  ADD COLUMN principal_id           TEXT NOT NULL DEFAULT 'bootstrap',
  ADD COLUMN principal_display_name TEXT NOT NULL DEFAULT 'System';

-- +goose Down

ALTER TABLE operations
  DROP COLUMN principal_type,
  DROP COLUMN principal_id,
  DROP COLUMN principal_display_name;
