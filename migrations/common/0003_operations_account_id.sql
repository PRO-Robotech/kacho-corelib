-- Copyright (c) PRO-Robotech
-- SPDX-License-Identifier: BUSL-1.1

-- +goose Up

-- Добавить additive, nullable account_id-денормализацию в общую
-- operations-таблицу, чтобы account-scoped IAM operation-listing
-- фильтровался по ней на DB-уровне (partial cursor-индекс, не
-- software-агрегация).
--
-- ADDITIVE / BACK-COMPAT: колонка nullable, БЕЗ DEFAULT и БЕЗ NOT NULL —
-- существующие строки (vpc/compute/nlb/apps и не-IAM IAM-операции) остаются
-- account_id IS NULL, поведение прочих сервисов не меняется и строки не
-- bloat'ятся. account_id проставляется только когда пишущий use-case передал
-- metadata с exact-полем account_id (corelib extractAccountID, repo.go).
--
-- partial индекс (account_id, created_at, id) WHERE account_id IS NOT NULL —
-- покрывает cursor-пагинацию account-scoped List (WHERE account_id = $x
-- ORDER BY created_at, id) и НЕ индексирует NULL-строки (не растет от
-- не-IAM операций).

ALTER TABLE operations
  ADD COLUMN account_id TEXT NULL;

CREATE INDEX operations_account_id_idx
  ON operations (account_id, created_at, id)
  WHERE account_id IS NOT NULL;

-- +goose Down

DROP INDEX IF EXISTS operations_account_id_idx;

ALTER TABLE operations
  DROP COLUMN account_id;
