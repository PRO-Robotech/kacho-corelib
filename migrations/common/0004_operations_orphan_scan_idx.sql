-- Copyright (c) PRO-Robotech
-- SPDX-License-Identifier: BUSL-1.1

-- +goose Up

-- Partial-индекс под orphan-scan startup-reconciler'а (durable LRO recovery).
-- Reconciler сканирует НЕзавершенные операции старше grace-окна:
--   WHERE done = false AND modified_at < $1 ORDER BY modified_at FOR UPDATE SKIP LOCKED.
-- Индекс по (modified_at) WHERE NOT done покрывает фильтр+сортировку claim-запроса
-- и НЕ индексирует завершенные строки (терминальные операции — основная масса),
-- поэтому не растет с историей и не bloat'ит запись терминального перехода.

CREATE INDEX operations_orphan_scan_idx
  ON operations (modified_at)
  WHERE NOT done;

-- +goose Down

DROP INDEX IF EXISTS operations_orphan_scan_idx;
