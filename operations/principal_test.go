// Copyright (c) PRO-Robotech
// SPDX-License-Identifier: BUSL-1.1

package operations_test

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/PRO-Robotech/kacho-corelib/operations"
)

// ---- principal_ctx unit-тесты (без БД) ----

// TestPrincipalFromContext_EmptyCtx — пустой ctx → SystemPrincipal.
func TestPrincipalFromContext_EmptyCtx(t *testing.T) {
	got := operations.PrincipalFromContext(context.Background())
	assert.Equal(t, operations.SystemPrincipal(), got,
		"пустой ctx должен дать SystemPrincipal")
}

// TestPrincipalFromContext_NilCtx — nil-ctx тоже → SystemPrincipal (defensive).
func TestPrincipalFromContext_NilCtx(t *testing.T) {
	//nolint:staticcheck // SA1012: тестируем defensive-fallback nil-ctx
	got := operations.PrincipalFromContext(nil)
	assert.Equal(t, operations.SystemPrincipal(), got,
		"nil ctx должен defensive-fallback'нуть на SystemPrincipal")
}

// TestPrincipalFromContext_RoundTrip — WithPrincipal/PrincipalFromContext.
func TestPrincipalFromContext_RoundTrip(t *testing.T) {
	p := operations.Principal{
		Type:        "user",
		ID:          "usr-abc123",
		DisplayName: "alice@example.com",
	}
	ctx := operations.WithPrincipal(context.Background(), p)
	got := operations.PrincipalFromContext(ctx)
	assert.Equal(t, p, got)
}

// TestSystemPrincipal_StableValues — stub-значения не плывут.
func TestSystemPrincipal_StableValues(t *testing.T) {
	got := operations.SystemPrincipal()
	assert.Equal(t, "system", got.Type)
	assert.Equal(t, "bootstrap", got.ID)
	assert.Equal(t, "System", got.DisplayName)
}

// ---- repo-level тесты (testcontainers) ----

// TestRepo_CreateWithPrincipal — CreateWithPrincipal пишет principal-колонки
// и Get их корректно возвращает.
func TestRepo_CreateWithPrincipal(t *testing.T) {
	pool := setupPostgres(t)
	ctx := context.Background()
	repo := newRepo(pool)

	op, err := operations.New("opx", "test create with principal", nil)
	require.NoError(t, err)

	p := operations.Principal{
		Type:        "user",
		ID:          "usr-aabbcc",
		DisplayName: "alice@example.com",
	}
	require.NoError(t, repo.CreateWithPrincipal(ctx, op, p))

	got, err := repo.Get(ctx, op.ID)
	require.NoError(t, err)
	assert.Equal(t, p, got.Principal)

	// Доп-проверка прямо в БД, что колонки заполнились.
	var pt, pid, pdn string
	err = pool.QueryRow(ctx, `
		SELECT principal_type, principal_id, principal_display_name
		FROM operations WHERE id = $1
	`, op.ID).Scan(&pt, &pid, &pdn)
	require.NoError(t, err)
	assert.Equal(t, "user", pt)
	assert.Equal(t, "usr-aabbcc", pid)
	assert.Equal(t, "alice@example.com", pdn)
}

// TestRepo_Create_LegacyDefaultsToSystem — legacy-Create (без явного
// principal'а) пишет SystemPrincipal-stub.
func TestRepo_Create_LegacyDefaultsToSystem(t *testing.T) {
	pool := setupPostgres(t)
	ctx := context.Background()
	repo := newRepo(pool)

	op, err := operations.New("opx", "legacy create", nil)
	require.NoError(t, err)

	// op.Principal не заполнен — должен использоваться SystemPrincipal.
	require.NoError(t, repo.Create(ctx, op))

	got, err := repo.Get(ctx, op.ID)
	require.NoError(t, err)
	assert.Equal(t, operations.SystemPrincipal(), got.Principal,
		"legacy Create без явного principal'а должен дать SystemPrincipal")
}

// TestRepo_Create_RespectsExplicitPrincipalOnOp — если op.Principal заполнен
// заранее (use-case вручную set'нул) — Create должен его уважать, не
// перетирать SystemPrincipal'ом.
func TestRepo_Create_RespectsExplicitPrincipalOnOp(t *testing.T) {
	pool := setupPostgres(t)
	ctx := context.Background()
	repo := newRepo(pool)

	op, err := operations.New("opx", "create with op.Principal preset", nil)
	require.NoError(t, err)
	op.Principal = operations.Principal{
		Type:        "service_account",
		ID:          "sva-deadbeef",
		DisplayName: "ci-bot",
	}

	require.NoError(t, repo.Create(ctx, op))

	got, err := repo.Get(ctx, op.ID)
	require.NoError(t, err)
	assert.Equal(t, op.Principal, got.Principal)
}

// TestRepo_CreateWithPrincipal_EmptyFallback — если в CreateWithPrincipal
// передан zero-Principal — fallback на SystemPrincipal (defensive).
func TestRepo_CreateWithPrincipal_EmptyFallback(t *testing.T) {
	pool := setupPostgres(t)
	ctx := context.Background()
	repo := newRepo(pool)

	op, err := operations.New("opx", "create with empty principal", nil)
	require.NoError(t, err)

	require.NoError(t, repo.CreateWithPrincipal(ctx, op, operations.Principal{}))

	got, err := repo.Get(ctx, op.ID)
	require.NoError(t, err)
	assert.Equal(t, operations.SystemPrincipal(), got.Principal)
}

// TestRepo_List_ReturnsPrincipal — List тоже отдает Principal в каждом item'е.
func TestRepo_List_ReturnsPrincipal(t *testing.T) {
	pool := setupPostgres(t)
	ctx := context.Background()
	repo := newRepo(pool)

	op, err := operations.New("opx", "list with principal", nil)
	require.NoError(t, err)
	p := operations.Principal{Type: "user", ID: "usr-list", DisplayName: "bob"}
	require.NoError(t, repo.CreateWithPrincipal(ctx, op, p))

	ops, _, err := repo.List(ctx, operations.ListFilter{PageSize: 10})
	require.NoError(t, err)
	require.Len(t, ops, 1)
	assert.Equal(t, p, ops[0].Principal)
}
