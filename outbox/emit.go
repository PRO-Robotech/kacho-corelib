// Copyright (c) PRO-Robotech
// SPDX-License-Identifier: BUSL-1.1

package outbox

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/jackc/pgx/v5"
)

// Emit вставляет одну outbox-строку в произвольную таблицу с фиксированной
// схемой: (sequence_no BIGSERIAL PK, resource_kind TEXT, resource_id TEXT,
// event_type TEXT, payload JSONB, created_at TIMESTAMPTZ DEFAULT now()).
//
// table — имя таблицы (например "vpc_outbox").
// kind — тип ресурса ("Network", "Subnet", "Address", "RouteTable", "SecurityGroup").
// id — ID ресурса (TEXT, поддерживает любой формат — UUID, короткий непрозрачный id и т.д.).
// eventType — "CREATED" | "UPDATED" | "DELETED".
// payload — произвольная map (сериализуется в JSONB).
//
// Должна вызываться внутри pgx.Tx, в которой выполняется INSERT/UPDATE/DELETE
// целевой ресурсной таблицы — это обеспечивает атомарность outbox-write.
//
// На каждый INSERT срабатывает trigger pg_notify('<channel>', sequence_no),
// который будит подписанных stream subscribers.
func Emit(ctx context.Context, tx pgx.Tx, table, kind, id, eventType string, payload map[string]any) error {
	if table == "" {
		return fmt.Errorf("outbox.Emit: table name required")
	}
	bp, err := json.Marshal(payload)
	if err != nil {
		// Не должно случаться для разумных payload-ов, но не молчим.
		return fmt.Errorf("outbox.Emit: marshal payload: %w", err)
	}
	// table инжектится в SQL, поэтому caller обязан использовать literal —
	// проверку whitelist здесь не делаем (helper в общей библиотеке должен
	// быть гибок). Атаки SQLi через имя таблицы — caller responsibility.
	q := fmt.Sprintf(
		`INSERT INTO %s (resource_kind, resource_id, event_type, payload) VALUES ($1, $2, $3, $4)`,
		table,
	)
	_, err = tx.Exec(ctx, q, kind, id, eventType, bp)
	if err != nil {
		return fmt.Errorf("outbox.Emit: insert into %s: %w", table, err)
	}
	return nil
}
