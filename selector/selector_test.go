// Copyright (c) PRO-Robotech
// SPDX-License-Identifier: BUSL-1.1

package selector_test

import (
	"context"
	"fmt"
	"os"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/testcontainers/testcontainers-go/modules/postgres"

	"github.com/PRO-Robotech/kacho-corelib/selector"
)

// B1: FieldSelector по name — точное совпадение.
func TestBuild_B1_ByName(t *testing.T) {
	result, err := selector.Build([]selector.Selector{
		{Field: &selector.FieldFilter{Name: "default"}},
	})
	require.NoError(t, err)
	assert.Equal(t, "WHERE (name = $1)", result.WhereClause)
	assert.Equal(t, []any{"default"}, result.Args)
}

// B2: FieldSelector по organizationId.
func TestBuild_B2_ByOrganizationID(t *testing.T) {
	result, err := selector.Build([]selector.Selector{
		{Field: &selector.FieldFilter{OrganizationID: "org-uid-abc"}},
	})
	require.NoError(t, err)
	assert.Equal(t, "WHERE (organization_id = $1)", result.WhereClause)
	assert.Equal(t, []any{"org-uid-abc"}, result.Args)
}

// B3: FieldSelector комбинация — name AND cloudId.
func TestBuild_B3_NameAndCloudID(t *testing.T) {
	result, err := selector.Build([]selector.Selector{
		{Field: &selector.FieldFilter{Name: "dev", CloudID: "cloud-uid-xyz"}},
	})
	require.NoError(t, err)
	assert.Equal(t, "WHERE (name = $1 AND cloud_id = $2)", result.WhereClause)
	require.Len(t, result.Args, 2)
	assert.Equal(t, "dev", result.Args[0])
	assert.Equal(t, "cloud-uid-xyz", result.Args[1])

	// Проверяем стабильность порядка параметров
	result2, err := selector.Build([]selector.Selector{
		{Field: &selector.FieldFilter{Name: "dev", CloudID: "cloud-uid-xyz"}},
	})
	require.NoError(t, err)
	assert.Equal(t, result.WhereClause, result2.WhereClause)
	assert.Equal(t, result.Args, result2.Args)
}

// B4: LabelSelector — фильтр по одной метке.
func TestBuild_B4_LabelSelector_SingleLabel(t *testing.T) {
	result, err := selector.Build([]selector.Selector{
		{Labels: map[string]string{"env": "production"}},
	})
	require.NoError(t, err)
	assert.Equal(t, "WHERE (labels @> $1::jsonb)", result.WhereClause)
	require.Len(t, result.Args, 1)
	assert.Contains(t, result.Args[0].(string), `"env"`)
	assert.Contains(t, result.Args[0].(string), `"production"`)
}

// B5: LabelSelector — несколько меток (AND через jsonb containment).
func TestBuild_B5_LabelSelector_MultipleLabels(t *testing.T) {
	result, err := selector.Build([]selector.Selector{
		{Labels: map[string]string{"env": "production", "team": "platform"}},
	})
	require.NoError(t, err)
	assert.Equal(t, "WHERE (labels @> $1::jsonb)", result.WhereClause)
	require.Len(t, result.Args, 1)
	// Оба ключа должны быть в JSON
	labelJSON := result.Args[0].(string)
	assert.Contains(t, labelJSON, `"env"`)
	assert.Contains(t, labelJSON, `"production"`)
	assert.Contains(t, labelJSON, `"team"`)
	assert.Contains(t, labelJSON, `"platform"`)
}

// B6: FieldSelector AND LabelSelector в одном Selector.
func TestBuild_B6_FieldAndLabel(t *testing.T) {
	result, err := selector.Build([]selector.Selector{
		{
			Field:  &selector.FieldFilter{Name: "prod-folder"},
			Labels: map[string]string{"tier": "premium"},
		},
	})
	require.NoError(t, err)
	assert.Equal(t, "WHERE (name = $1 AND labels @> $2::jsonb)", result.WhereClause)
	require.Len(t, result.Args, 2)
	assert.Equal(t, "prod-folder", result.Args[0])
	assert.Contains(t, result.Args[1].(string), `"tier"`)
}

// B7: Несколько Selector-ов — OR между ними.
func TestBuild_B7_MultipleSelectorsOR(t *testing.T) {
	result, err := selector.Build([]selector.Selector{
		{Field: &selector.FieldFilter{Name: "alpha"}},
		{Field: &selector.FieldFilter{Name: "beta"}},
	})
	require.NoError(t, err)
	assert.Equal(t, "WHERE ((name = $1) OR (name = $2))", result.WhereClause)
	require.Len(t, result.Args, 2)
	assert.Equal(t, "alpha", result.Args[0])
	assert.Equal(t, "beta", result.Args[1])
}

// B8: Пустой список Selector-ов — без WHERE.
func TestBuild_B8_EmptySelectors(t *testing.T) {
	result, err := selector.Build([]selector.Selector{})
	require.NoError(t, err)
	assert.Empty(t, result.WhereClause)
	assert.Nil(t, result.Args)
}

// B9: SQL injection через selector — только параметр-placeholder.
func TestBuild_B9_SQLInjectionProtection(t *testing.T) {
	maliciousName := "'; DROP TABLE organizations; --"
	result, err := selector.Build([]selector.Selector{
		{Field: &selector.FieldFilter{Name: maliciousName}},
	})
	require.NoError(t, err)

	// В SQL-фрагменте должен быть только $1, а не сырое значение
	assert.Equal(t, "WHERE (name = $1)", result.WhereClause)
	assert.NotContains(t, result.WhereClause, maliciousName, "вредоносный ввод не должен попасть в SQL")
	assert.NotContains(t, result.WhereClause, "DROP", "SQL injection не должна проходить")
	require.Len(t, result.Args, 1)
	assert.Equal(t, maliciousName, result.Args[0], "значение должно быть в параметре")
}

// B10: Integration — selector + SQL работает против реальной БД.
func TestBuild_B10_Integration_LabelFilter(t *testing.T) {
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

	// Создаем таблицу organizations с JSONB labels
	_, err = pool.Exec(ctx, `
		CREATE TABLE organizations (
		  uid    UUID PRIMARY KEY DEFAULT gen_random_uuid(),
		  name   TEXT NOT NULL UNIQUE,
		  labels JSONB NOT NULL DEFAULT '{}'
		);
		CREATE INDEX ON organizations USING GIN (labels jsonb_path_ops);
	`)
	require.NoError(t, err)

	// Вставляем 3 записи
	_, err = pool.Exec(ctx, `
		INSERT INTO organizations (name, labels) VALUES
		  ('alpha', '{"env":"dev"}'),
		  ('beta',  '{"env":"prod"}'),
		  ('gamma', '{"env":"prod","tier":"premium"}')
	`)
	require.NoError(t, err)

	// Запрос с label selector env=prod
	sel, err := selector.Build([]selector.Selector{
		{Labels: map[string]string{"env": "prod"}},
	})
	require.NoError(t, err)

	query := fmt.Sprintf("SELECT name FROM organizations %s ORDER BY name", sel.WhereClause)
	rows, err := pool.Query(ctx, query, sel.Args...)
	require.NoError(t, err)
	defer rows.Close()

	var names []string
	for rows.Next() {
		var name string
		require.NoError(t, rows.Scan(&name))
		names = append(names, name)
	}
	require.NoError(t, rows.Err())

	assert.Equal(t, []string{"beta", "gamma"}, names)
	assert.NotContains(t, names, "alpha")
}

// Дополнительный тест: BuildFrom с offset параметров.
func TestBuildFrom_ParameterOffset(t *testing.T) {
	result, err := selector.BuildFrom([]selector.Selector{
		{Field: &selector.FieldFilter{Name: "test"}},
	}, 5)
	require.NoError(t, err)
	assert.Equal(t, "WHERE (name = $5)", result.WhereClause)
	require.Len(t, result.Args, 1)
	assert.Equal(t, "test", result.Args[0])
}

// Тест: selector без условий → пустой WHERE.
func TestBuild_EmptySelectorInList(t *testing.T) {
	result, err := selector.Build([]selector.Selector{
		{}, // пустой selector
	})
	require.NoError(t, err)
	assert.Empty(t, result.WhereClause)
}

// Тест: keyset pagination helper.
func TestBuildPageClause(t *testing.T) {
	clause, args := selector.BuildPageClause(&selector.PageToken{
		LastResourceVersion: 42,
		LastUID:             "some-uid",
	}, 1)
	assert.Contains(t, clause, "$1")
	assert.Contains(t, clause, "$2")
	require.Len(t, args, 2)
	assert.Equal(t, int64(42), args[0])
	assert.Equal(t, "some-uid", args[1])

	// Nil token → пустая строка
	clause2, args2 := selector.BuildPageClause(nil, 1)
	assert.Empty(t, clause2)
	assert.Nil(t, args2)
}

// Тест: нет лишних пробелов/символов вне параметров в сгенерированном SQL.
func TestBuild_NoRawValuesInSQL(t *testing.T) {
	result, err := selector.Build([]selector.Selector{
		{Field: &selector.FieldFilter{Name: "my-name", OrganizationID: "my-org"}},
		{Labels: map[string]string{"key": "val"}},
	})
	require.NoError(t, err)

	// SQL не должен содержать сырых значений
	for _, raw := range []string{"my-name", "my-org", "key", "val"} {
		assert.False(t, strings.Contains(result.WhereClause, raw),
			"сырое значение %q не должно быть в SQL", raw)
	}
}
