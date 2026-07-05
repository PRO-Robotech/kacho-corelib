// Copyright (c) PRO-Robotech
// SPDX-License-Identifier: BUSL-1.1

package db

import (
	"context"
	"fmt"

	"github.com/jackc/pgx/v5/pgxpool"
)

// NewPool создает pgxpool с server-side таймаутами и проверяет связь с БД (Ping)
// — fail-fast на старте: при недоступной БД возвращается ошибка, а не «ленивый»
// пул, который упадет лишь на первом запросе. Таймаут связи контролируется
// переданным ctx.
//
// Устанавливаемые RuntimeParams (defense-in-depth, независимо от корректности
// app-side ctx):
//   - statement_timeout=30s — потолок исполнения ОДНОГО запроса.
//   - idle_in_transaction_session_timeout=60s — потолок «idle-in-transaction»
//     (транзакция открыта, но не исполняет запрос). Дренер держит claim-tx
//     открытой на время applier-вызова (~5s), reconciler.Sweep — на время
//     одного Resolve (~10s, ResolveTimeout). На каждой итерации Sweep сбрасывает
//     idle-таймер statement'ом: ветки Done/Interrupted — через markDoneCAS/
//     markErrorCAS, а ветки Skip/resolver-error — через явный keep-alive
//     (reconciler.keepClaimAlive: SELECT 1). Поэтому непрерывный idle ограничен
//     одним ResolveTimeout на любой ветке, а не суммой по батчу. 60s
//     даёт запас ~6x над этим потолком и при этом жёстко реапит по-настоящему
//     зависшую tx (минуты), которую app-side ctx проглядел (CGO/DNS-stall,
//     игнорирующий cancel) — иначе она держала бы FOR UPDATE SKIP LOCKED
//     row-locks и блокировала VACUUM (CWE-400).
//
// lock_timeout НЕ выставляется намеренно: ожидание блокировки и так ограничено
// statement_timeout (30s), а отдельный lock_timeout ввёл бы новый класс ошибки
// (SQLSTATE 55P03) на путь contended-CAS во всех сервисах, чьи mapRepoErr его не
// обрабатывают — регрессия без выигрыша сверх statement_timeout.
func NewPool(ctx context.Context, dsn string) (*pgxpool.Pool, error) {
	cfg, err := pgxpool.ParseConfig(dsn)
	if err != nil {
		return nil, err
	}
	if cfg.ConnConfig.RuntimeParams == nil {
		cfg.ConnConfig.RuntimeParams = map[string]string{}
	}
	cfg.ConnConfig.RuntimeParams["statement_timeout"] = "30000"
	cfg.ConnConfig.RuntimeParams["idle_in_transaction_session_timeout"] = "60000"
	pool, err := pgxpool.NewWithConfig(ctx, cfg)
	if err != nil {
		return nil, err
	}
	if err := pool.Ping(ctx); err != nil {
		pool.Close()
		return nil, fmt.Errorf("db: ping after pool creation: %w", err)
	}
	return pool, nil
}
