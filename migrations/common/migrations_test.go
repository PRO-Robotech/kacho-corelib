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

//go:embed 0001_resource_events.sql
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

	// Извлекаем Up-секцию из migration файла
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
	start := -1
	lines := splitLines(sql)
	result := make([]string, 0, len(lines))

	inSection := false
	for _, line := range lines {
		if line == marker {
			inSection = true
			start = 0
			_ = start
			continue
		}
		if inSection && len(line) > 9 && line[:10] == "-- +goose " {
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

// C1: Миграция создаёт resource_version_seq.
func TestMigration_C1_ResourceVersionSequence(t *testing.T) {
	pool := setupPostgres(t)
	ctx := context.Background()

	applyMigrationUp(t, pool)

	// Sequence существует
	var v1, v2 int64
	err := pool.QueryRow(ctx, `SELECT nextval('resource_version_seq')`).Scan(&v1)
	require.NoError(t, err)
	assert.Equal(t, int64(1), v1, "первый nextval должен быть 1")

	err = pool.QueryRow(ctx, `SELECT nextval('resource_version_seq')`).Scan(&v2)
	require.NoError(t, err)
	assert.Equal(t, int64(2), v2, "второй nextval должен быть 2 (монотонно возрастает)")
}

// C2: Миграция создаёт resource_events с правильной схемой.
func TestMigration_C2_ResourceEventsSchema(t *testing.T) {
	pool := setupPostgres(t)
	ctx := context.Background()

	applyMigrationUp(t, pool)

	// Проверяем наличие колонок
	var colCount int
	err := pool.QueryRow(ctx, `
		SELECT count(*) FROM information_schema.columns
		WHERE table_name = 'resource_events'
		  AND column_name IN ('resource_version','event_type','resource_kind','resource_uid','data','created_at')
	`).Scan(&colCount)
	require.NoError(t, err)
	assert.Equal(t, 6, colCount, "все 6 колонок должны существовать")

	// Проверяем PRIMARY KEY на resource_version
	var pkCount int
	err = pool.QueryRow(ctx, `
		SELECT count(*) FROM information_schema.table_constraints tc
		JOIN information_schema.key_column_usage kcu
		  ON tc.constraint_name = kcu.constraint_name
		WHERE tc.table_name = 'resource_events'
		  AND tc.constraint_type = 'PRIMARY KEY'
		  AND kcu.column_name = 'resource_version'
	`).Scan(&pkCount)
	require.NoError(t, err)
	assert.Equal(t, 1, pkCount, "resource_version должен быть PRIMARY KEY")

	// Проверяем индексы
	var idxCount int
	err = pool.QueryRow(ctx, `
		SELECT count(*) FROM pg_indexes
		WHERE tablename = 'resource_events'
		  AND indexname IN ('resource_events_kind_rv_idx', 'resource_events_cleanup_idx')
	`).Scan(&idxCount)
	require.NoError(t, err)
	assert.Equal(t, 2, idxCount, "оба индекса должны существовать")
}

// C3: CHECK constraint отклоняет невалидный event_type.
func TestMigration_C3_CheckConstraint(t *testing.T) {
	pool := setupPostgres(t)
	ctx := context.Background()

	applyMigrationUp(t, pool)

	// Пытаемся вставить невалидный event_type
	_, err := pool.Exec(ctx,
		`INSERT INTO resource_events (event_type, resource_kind, resource_uid)
		 VALUES ('UNKNOWN', 'Organization', gen_random_uuid())`,
	)
	assert.Error(t, err, "должна быть ошибка CHECK constraint")
	assert.Contains(t, err.Error(), "resource_events_event_type_check",
		"ошибка должна содержать имя constraint")

	// Таблица пуста
	var count int
	err2 := pool.QueryRow(ctx, `SELECT count(*) FROM resource_events`).Scan(&count)
	require.NoError(t, err2)
	assert.Equal(t, 0, count)
}

// C6: Миграция идемпотентна при повторном применении (up/down/up).
func TestMigration_C6_Idempotent(t *testing.T) {
	pool := setupPostgres(t)
	ctx := context.Background()

	// Первое применение
	applyMigrationUp(t, pool)

	// Down
	applyMigrationDown(t, pool)

	// Снова Up
	applyMigrationUp(t, pool)

	// Схема корректна после повторного применения
	var v1 int64
	err := pool.QueryRow(ctx, `SELECT nextval('resource_version_seq')`).Scan(&v1)
	require.NoError(t, err)
	assert.Greater(t, v1, int64(0))

	// bump_resource_version функция существует
	var fnExists bool
	err = pool.QueryRow(ctx, `
		SELECT EXISTS(
		  SELECT 1 FROM pg_proc
		  WHERE proname = 'bump_resource_version'
		)
	`).Scan(&fnExists)
	require.NoError(t, err)
	assert.True(t, fnExists, "функция bump_resource_version должна существовать")
}
