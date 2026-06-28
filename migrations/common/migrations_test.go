// Copyright (c) PRO-Robotech
// SPDX-License-Identifier: BUSL-1.1

package common_test

import (
	"context"
	"os"
	"testing"

	_ "embed"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/testcontainers/testcontainers-go/modules/postgres"
)

//go:embed 0001_operations.sql
var migrationSQL string

//go:embed 0002_operations_principal.sql
var migration0002SQL string

//go:embed 0003_operations_account_id.sql
var migration0003SQL string

//go:embed 0004_operations_orphan_scan_idx.sql
var migration0004SQL string

// setupPostgres поднимает контейнер Postgres с чистой схемой.
func setupPostgres(t *testing.T) *pgxpool.Pool {
	t.Helper()
	if testing.Short() || os.Getenv("SKIP_INTEGRATION") == "1" {
		t.Skip("integration tests skipped (SKIP_INTEGRATION=1)")
	}

	ctx := context.Background()

	ctr, err := postgres.Run(ctx, "postgres:16-alpine",
		postgres.WithDatabase("testdb"),
		postgres.WithUsername("test"),
		postgres.WithPassword("test"),
		postgres.BasicWaitStrategies(),
	)
	require.NoError(t, err)
	t.Cleanup(func() { _ = ctr.Terminate(ctx) })

	dsn, err := ctr.ConnectionString(ctx, "sslmode=disable")
	require.NoError(t, err)

	pool, err := pgxpool.New(ctx, dsn)
	require.NoError(t, err)
	t.Cleanup(pool.Close)

	return pool
}

// applyMigrationUp применяет Up-часть миграции (между -- +goose Up и -- +goose Down).
func applyMigrationUp(t *testing.T, pool *pgxpool.Pool) {
	t.Helper()
	ctx := context.Background()

	upSQL := extractGooseSection(migrationSQL, "Up")
	require.NotEmpty(t, upSQL, "Up-секция миграции не должна быть пустой")

	_, err := pool.Exec(ctx, upSQL)
	require.NoError(t, err, "ошибка при применении миграции Up")
}

// applyMigrationDown применяет Down-часть миграции.
func applyMigrationDown(t *testing.T, pool *pgxpool.Pool) {
	t.Helper()
	ctx := context.Background()

	downSQL := extractGooseSection(migrationSQL, "Down")
	require.NotEmpty(t, downSQL, "Down-секция миграции не должна быть пустой")

	_, err := pool.Exec(ctx, downSQL)
	require.NoError(t, err, "ошибка при применении миграции Down")
}

// extractGooseSection извлекает SQL между -- +goose Up/Down и следующим маркером.
func extractGooseSection(sql, section string) string {
	marker := "-- +goose " + section
	lines := splitLines(sql)
	result := make([]string, 0, len(lines))

	inSection := false
	for _, line := range lines {
		if line == marker {
			inSection = true
			continue
		}
		if inSection && len(line) >= 10 && line[:10] == "-- +goose " {
			break
		}
		if inSection {
			result = append(result, line)
		}
	}
	return joinLines(result)
}

func splitLines(s string) []string {
	var lines []string
	cur := ""
	for _, r := range s {
		if r == '\n' {
			lines = append(lines, cur)
			cur = ""
		} else {
			cur += string(r)
		}
	}
	if cur != "" {
		lines = append(lines, cur)
	}
	return lines
}

func joinLines(lines []string) string {
	result := ""
	for _, l := range lines {
		result += l + "\n"
	}
	return result
}

// C1: Миграция создает таблицу operations с правильной схемой.
func TestMigration_C1_OperationsSchema(t *testing.T) {
	pool := setupPostgres(t)
	ctx := context.Background()

	applyMigrationUp(t, pool)

	// Проверяем наличие всех колонок
	expectedCols := []string{
		"id", "description", "created_at", "created_by", "modified_at", "done",
		"metadata_type", "metadata_data", "resource_id",
		"error_code", "error_message", "error_details",
		"response_type", "response_data",
	}
	var colCount int
	err := pool.QueryRow(ctx, `
		SELECT count(*) FROM information_schema.columns
		WHERE table_name = 'operations'
		  AND column_name = ANY($1)
	`, expectedCols).Scan(&colCount)
	require.NoError(t, err)
	assert.Equal(t, len(expectedCols), colCount, "все колонки должны существовать")

	// Проверяем PRIMARY KEY на id
	var pkCount int
	err = pool.QueryRow(ctx, `
		SELECT count(*) FROM information_schema.table_constraints tc
		JOIN information_schema.key_column_usage kcu
		  ON tc.constraint_name = kcu.constraint_name
		WHERE tc.table_name = 'operations'
		  AND tc.constraint_type = 'PRIMARY KEY'
		  AND kcu.column_name = 'id'
	`).Scan(&pkCount)
	require.NoError(t, err)
	assert.Equal(t, 1, pkCount, "id должен быть PRIMARY KEY")
}

// C2: Миграция создает индексы.
func TestMigration_C2_Indexes(t *testing.T) {
	pool := setupPostgres(t)
	ctx := context.Background()

	applyMigrationUp(t, pool)

	var idxCount int
	err := pool.QueryRow(ctx, `
		SELECT count(*) FROM pg_indexes
		WHERE tablename = 'operations'
		  AND indexname IN (
		    'operations_resource_idx',
		    'operations_done_idx',
		    'operations_created_at_idx'
		  )
	`).Scan(&idxCount)
	require.NoError(t, err)
	assert.Equal(t, 3, idxCount, "все три индекса должны существовать")
}

// C-orphan: миграция 0004 добавляет partial-индекс под orphan-scan reconciler'а
// (durable LRO recovery): индекс (modified_at) WHERE NOT done покрывает
// claim-запрос reconciler'а и не растет от завершенных строк.
func TestMigration_C_OrphanScanIndex(t *testing.T) {
	pool := setupPostgres(t)
	ctx := context.Background()

	applyMigrationUp(t, pool) // 0001 — таблица operations

	up0004 := extractGooseSection(migration0004SQL, "Up")
	require.NotEmpty(t, up0004, "Up-секция 0004 не должна быть пустой")
	_, err := pool.Exec(ctx, up0004)
	require.NoError(t, err)

	var idxCount int
	err = pool.QueryRow(ctx, `
		SELECT count(*) FROM pg_indexes
		WHERE tablename = 'operations' AND indexname = 'operations_orphan_scan_idx'
	`).Scan(&idxCount)
	require.NoError(t, err)
	assert.Equal(t, 1, idxCount, "partial-индекс orphan-scan должен существовать")

	// Индекс должен быть partial (predicate WHERE NOT done).
	var hasPredicate bool
	err = pool.QueryRow(ctx, `
		SELECT i.indpred IS NOT NULL
		FROM pg_index i JOIN pg_class c ON c.oid = i.indexrelid
		WHERE c.relname = 'operations_orphan_scan_idx'
	`).Scan(&hasPredicate)
	require.NoError(t, err)
	assert.True(t, hasPredicate, "индекс должен быть partial (WHERE NOT done)")

	// Down убирает индекс.
	down0004 := extractGooseSection(migration0004SQL, "Down")
	require.NotEmpty(t, down0004, "Down-секция 0004 не должна быть пустой")
	_, err = pool.Exec(ctx, down0004)
	require.NoError(t, err)
}

// C3: Миграция идемпотентна при up/down/up.
func TestMigration_C3_Idempotent(t *testing.T) {
	pool := setupPostgres(t)
	ctx := context.Background()

	applyMigrationUp(t, pool)
	applyMigrationDown(t, pool)
	applyMigrationUp(t, pool)

	var tableExists bool
	err := pool.QueryRow(ctx, `
		SELECT EXISTS(
		  SELECT 1 FROM information_schema.tables
		  WHERE table_name = 'operations'
		)
	`).Scan(&tableExists)
	require.NoError(t, err)
	assert.True(t, tableExists, "таблица operations должна существовать после повторного up")
}

// apply0002Up — Up-секция миграции 0002.
func apply0002Up(t *testing.T, pool *pgxpool.Pool) {
	t.Helper()
	upSQL := extractGooseSection(migration0002SQL, "Up")
	require.NotEmpty(t, upSQL)
	_, err := pool.Exec(context.Background(), upSQL)
	require.NoError(t, err)
}

// apply0002Down — Down-секция миграции 0002.
func apply0002Down(t *testing.T, pool *pgxpool.Pool) {
	t.Helper()
	downSQL := extractGooseSection(migration0002SQL, "Down")
	require.NotEmpty(t, downSQL)
	_, err := pool.Exec(context.Background(), downSQL)
	require.NoError(t, err)
}

// C4: Миграция 0002 добавляет principal_type / principal_id /
// principal_display_name с правильными DEFAULT'ами.
func TestMigration_C4_PrincipalColumns(t *testing.T) {
	pool := setupPostgres(t)
	ctx := context.Background()

	applyMigrationUp(t, pool)
	apply0002Up(t, pool)

	expectedCols := []string{
		"principal_type",
		"principal_id",
		"principal_display_name",
	}
	var colCount int
	err := pool.QueryRow(ctx, `
		SELECT count(*) FROM information_schema.columns
		WHERE table_name = 'operations'
		  AND column_name = ANY($1)
		  AND is_nullable = 'NO'
	`, expectedCols).Scan(&colCount)
	require.NoError(t, err)
	assert.Equal(t, len(expectedCols), colCount,
		"все три principal-колонки должны быть NOT NULL")

	// Проверка DEFAULT-значений: вставляем строку без principal-полей и
	// смотрим, что в БД появились stub'ы 'system'/'bootstrap'/'System'.
	_, err = pool.Exec(ctx, `
		INSERT INTO operations (id, description) VALUES ('op-defaults', 'no-auth-ctx')
	`)
	require.NoError(t, err)

	var pt, pid, pdn string
	err = pool.QueryRow(ctx, `
		SELECT principal_type, principal_id, principal_display_name
		FROM operations WHERE id = 'op-defaults'
	`).Scan(&pt, &pid, &pdn)
	require.NoError(t, err)
	assert.Equal(t, "system", pt)
	assert.Equal(t, "bootstrap", pid)
	assert.Equal(t, "System", pdn)
}

// C5: Миграция 0002 идемпотентна (up → down → up).
func TestMigration_C5_0002Idempotent(t *testing.T) {
	pool := setupPostgres(t)
	ctx := context.Background()

	applyMigrationUp(t, pool)
	apply0002Up(t, pool)
	apply0002Down(t, pool)
	apply0002Up(t, pool)

	// После повторного up принципал-колонки снова на месте.
	var colCount int
	err := pool.QueryRow(ctx, `
		SELECT count(*) FROM information_schema.columns
		WHERE table_name = 'operations'
		  AND column_name IN ('principal_type','principal_id','principal_display_name')
	`).Scan(&colCount)
	require.NoError(t, err)
	assert.Equal(t, 3, colCount)
}

// C6: Миграция 0002 back-fill'ит существующие строки stub-значениями.
func TestMigration_C6_0002BackfillExisting(t *testing.T) {
	pool := setupPostgres(t)
	ctx := context.Background()

	// Применяем 0001 и вставляем «доисторическую» строку без principal-полей.
	applyMigrationUp(t, pool)
	_, err := pool.Exec(ctx, `
		INSERT INTO operations (id, description) VALUES ('legacy-op', 'pre-iam')
	`)
	require.NoError(t, err)

	// Поверх — миграция 0002. Существующая строка должна получить DEFAULT'ы.
	apply0002Up(t, pool)

	var pt, pid, pdn string
	err = pool.QueryRow(ctx, `
		SELECT principal_type, principal_id, principal_display_name
		FROM operations WHERE id = 'legacy-op'
	`).Scan(&pt, &pid, &pdn)
	require.NoError(t, err)
	assert.Equal(t, "system", pt)
	assert.Equal(t, "bootstrap", pid)
	assert.Equal(t, "System", pdn)
}

// apply0003Up — Up-секция миграции 0003.
func apply0003Up(t *testing.T, pool *pgxpool.Pool) {
	t.Helper()
	upSQL := extractGooseSection(migration0003SQL, "Up")
	require.NotEmpty(t, upSQL)
	_, err := pool.Exec(context.Background(), upSQL)
	require.NoError(t, err)
}

// apply0003Down — Down-секция миграции 0003.
func apply0003Down(t *testing.T, pool *pgxpool.Pool) {
	t.Helper()
	downSQL := extractGooseSection(migration0003SQL, "Down")
	require.NotEmpty(t, downSQL)
	_, err := pool.Exec(context.Background(), downSQL)
	require.NoError(t, err)
}

// C7: Миграция 0003 добавляет nullable account_id-колонку (additive,
// без NOT NULL — не bloat'ит существующие строки) и partial cursor-индекс.
func TestMigration_C7_AccountIDColumn(t *testing.T) {
	pool := setupPostgres(t)
	ctx := context.Background()

	applyMigrationUp(t, pool)
	apply0002Up(t, pool)
	apply0003Up(t, pool)

	// account_id присутствует и nullable (NOT NULL не навязан — additive).
	var isNullable string
	err := pool.QueryRow(ctx, `
		SELECT is_nullable FROM information_schema.columns
		WHERE table_name = 'operations' AND column_name = 'account_id'
	`).Scan(&isNullable)
	require.NoError(t, err)
	assert.Equal(t, "YES", isNullable, "account_id должен быть nullable (additive)")

	// partial cursor-индекс существует.
	var idxCount int
	err = pool.QueryRow(ctx, `
		SELECT count(*) FROM pg_indexes
		WHERE tablename = 'operations' AND indexname = 'operations_account_id_idx'
	`).Scan(&idxCount)
	require.NoError(t, err)
	assert.Equal(t, 1, idxCount, "partial account_id cursor-индекс должен существовать")

	// Индекс — partial (WHERE account_id IS NOT NULL).
	var idxDef string
	err = pool.QueryRow(ctx, `
		SELECT indexdef FROM pg_indexes
		WHERE tablename = 'operations' AND indexname = 'operations_account_id_idx'
	`).Scan(&idxDef)
	require.NoError(t, err)
	assert.Contains(t, idxDef, "account_id IS NOT NULL",
		"индекс должен быть partial (WHERE account_id IS NOT NULL)")
}

// C8: Миграция 0003 additive — существующие строки (0001/0002) переживают ALTER
// без потери данных, account_id у них NULL (не bloat).
func TestMigration_C8_0003AdditiveExisting(t *testing.T) {
	pool := setupPostgres(t)
	ctx := context.Background()

	applyMigrationUp(t, pool)
	apply0002Up(t, pool)
	// «доисторическая» строка до 0003.
	_, err := pool.Exec(ctx, `
		INSERT INTO operations (id, description, resource_id)
		VALUES ('pre-0003-op', 'pre-account-id', 'snt-Q')
	`)
	require.NoError(t, err)

	apply0003Up(t, pool)

	var resourceID string
	var accountID *string
	err = pool.QueryRow(ctx, `
		SELECT resource_id, account_id FROM operations WHERE id = 'pre-0003-op'
	`).Scan(&resourceID, &accountID)
	require.NoError(t, err)
	assert.Equal(t, "snt-Q", resourceID, "существующие данные не потеряны")
	assert.Nil(t, accountID, "account_id у доисторической строки NULL (additive)")
}

// C9: Миграция 0003 идемпотентна (up → down → up).
func TestMigration_C9_0003Idempotent(t *testing.T) {
	pool := setupPostgres(t)
	ctx := context.Background()

	applyMigrationUp(t, pool)
	apply0002Up(t, pool)
	apply0003Up(t, pool)
	apply0003Down(t, pool)
	apply0003Up(t, pool)

	var colCount int
	err := pool.QueryRow(ctx, `
		SELECT count(*) FROM information_schema.columns
		WHERE table_name = 'operations' AND column_name = 'account_id'
	`).Scan(&colCount)
	require.NoError(t, err)
	assert.Equal(t, 1, colCount, "account_id снова на месте после повторного up")
}
