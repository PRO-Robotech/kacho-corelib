// Copyright (c) PRO-Robotech
// SPDX-License-Identifier: BUSL-1.1

package operations_test

// Integration-тесты (testcontainers Postgres) ownership-scoped листинга
// операций: ListOwned — ownership-предикат внутри SQL WHERE (симметрично
// GetOwned/CancelOwned). Чужие операции НЕ видны в выдаче; фильтр по ResourceID
// комбинируется с ownership-предикатом; пагинация keyset сохраняется.

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/PRO-Robotech/kacho-corelib/operations"
)

// ListOwned видит ТОЛЬКО операции владельца; чужие отфильтрованы предикатом.
func TestOwnership_ListOwned_OnlyOwnerRows(t *testing.T) {
	pool := setupPostgres(t)
	ctx := context.Background()
	repo := newRepo(pool)
	owned := ownedRepo(t, pool)

	a1 := createOwnedOp(t, ctx, repo, "A op 1", usrA)
	a2 := createOwnedOp(t, ctx, repo, "A op 2", usrA)
	_ = createOwnedOp(t, ctx, repo, "B op 1", usrB)

	got, next, err := owned.ListOwned(ctx, operations.ListFilter{PageSize: 50}, operations.OwnerFromPrincipal(usrA))
	require.NoError(t, err)
	assert.Empty(t, next)

	ids := map[string]bool{}
	for _, op := range got {
		ids[op.ID] = true
		assert.Equal(t, "usr-A", op.Principal.ID, "ListOwned не должен возвращать чужие строки")
	}
	assert.True(t, ids[a1.ID], "своя операция a1 присутствует")
	assert.True(t, ids[a2.ID], "своя операция a2 присутствует")
	assert.Len(t, got, 2, "ровно 2 операции usr-A, операция usr-B невидима")
}

// Чужой owner c непустой выдачей у другого principal → пустой список (no-leak).
func TestOwnership_ListOwned_StrangerSeesNothing(t *testing.T) {
	pool := setupPostgres(t)
	ctx := context.Background()
	repo := newRepo(pool)
	owned := ownedRepo(t, pool)

	createOwnedOp(t, ctx, repo, "A only", usrA)

	got, _, err := owned.ListOwned(ctx, operations.ListFilter{PageSize: 50}, operations.OwnerFromPrincipal(usrB))
	require.NoError(t, err)
	assert.Empty(t, got, "у usr-B нет своих операций → пусто, чужие не протекают")
}

// ResourceID-фильтр комбинируется с ownership-предикатом (AND), не подменяет его.
func TestOwnership_ListOwned_ResourceFilterPlusOwner(t *testing.T) {
	pool := setupPostgres(t)
	ctx := context.Background()
	repo := newRepo(pool)
	owned := ownedRepo(t, pool)

	// Две операции разных владельцев c ОДНИМ resource_id (проставляем напрямую в
	// денормализованную колонку — эквивалент extractResourceID из metadata).
	opA := createOwnedOp(t, ctx, repo, "A with resource", usrA)
	opB := createOwnedOp(t, ctx, repo, "B with same resource", usrB)
	_, err := pool.Exec(ctx, "UPDATE operations SET resource_id = 'net-shared' WHERE id = ANY($1)",
		[]string{opA.ID, opB.ID})
	require.NoError(t, err)

	// usr-A фильтрует по общему resource_id → видит только свою (ownership AND resource).
	got, _, err := owned.ListOwned(ctx,
		operations.ListFilter{ResourceID: "net-shared", PageSize: 50},
		operations.OwnerFromPrincipal(usrA))
	require.NoError(t, err)
	require.Len(t, got, 1, "resource-фильтр + ownership → только своя операция")
	assert.Equal(t, opA.ID, got[0].ID)
	assert.Equal(t, "usr-A", got[0].Principal.ID)
}

// Keyset-пагинация сохраняется под ownership-предикатом.
func TestOwnership_ListOwned_Pagination(t *testing.T) {
	pool := setupPostgres(t)
	ctx := context.Background()
	repo := newRepo(pool)
	owned := ownedRepo(t, pool)

	const total = 5
	for i := 0; i < total; i++ {
		createOwnedOp(t, ctx, repo, "page op", usrA)
	}
	// Шум от другого владельца — не должен просочиться и не должен влиять на курсор.
	createOwnedOp(t, ctx, repo, "B noise", usrB)

	seen := map[string]bool{}
	var token string
	pages := 0
	for {
		got, next, err := owned.ListOwned(ctx,
			operations.ListFilter{PageSize: 2, PageToken: token},
			operations.OwnerFromPrincipal(usrA))
		require.NoError(t, err)
		for _, op := range got {
			require.Equal(t, "usr-A", op.Principal.ID, "чужая строка не должна появляться в пагинации")
			seen[op.ID] = true
		}
		pages++
		if next == "" {
			break
		}
		token = next
		require.Less(t, pages, 10, "пагинация не должна зациклиться")
	}
	assert.Len(t, seen, total, "все %d операций владельца собраны через пагинацию", total)
}
