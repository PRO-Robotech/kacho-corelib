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

// setupPostgres поднимает контейнер Postgres с чистой схемой.
func setupPostgres(t *testing.T) *pgxpool.Pool {
	t.Helper()
	if os.Getenv("SKIP_INTEGRATION") == "1" {
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

// C1: Миграция создаёт таблицу operations с правильной схемой.
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

// C2: Миграция создаёт индексы.
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
