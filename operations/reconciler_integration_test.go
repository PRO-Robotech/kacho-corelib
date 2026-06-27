// Copyright (c) PRO-Robotech
// SPDX-License-Identifier: BUSL-1.1

package operations_test

// Integration-тесты (testcontainers Postgres) startup-reconciler'а:
// осиротевшие in-flight операции разрешаются в терминал по
// committed-реальности ресурса (доменный Resolver), grace-окно защищает
// live-worker, конкурентные reconciler'ы реплик разбирают множество exactly-once
// через FOR UPDATE SKIP LOCKED, reconciler добивает невосстановимый MarkDone-fail.

import (
	"context"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/PRO-Robotech/kacho-corelib/operations"
)

// fakeResolver — тестовый доменный resolver: по op.ID решает терминальный исход.
// present[id]=true → ресурс закоммичен (Create/Update → Done с response,
// либо Delete → Interrupted); по умолчанию absent.
type fakeResolver struct {
	outcome map[string]operations.ResolverResult
	errFor  map[string]error
}

func newFakeResolver() *fakeResolver {
	return &fakeResolver{outcome: map[string]operations.ResolverResult{}, errFor: map[string]error{}}
}

func (f *fakeResolver) Resolve(_ context.Context, op operations.Operation) (operations.ResolverResult, error) {
	if e, ok := f.errFor[op.ID]; ok {
		return operations.ResolverResult{}, e
	}
	if r, ok := f.outcome[op.ID]; ok {
		return r, nil
	}
	return operations.ResolverResult{Outcome: operations.OutcomeSkip}, nil
}

// insertOrphan вставляет «осиротевшую» (done=false) строку с modified_at в
// прошлом (старше grace), имитируя in-flight операцию упавшего процесса.
func insertOrphan(t *testing.T, ctx context.Context, pool *pgxpool.Pool, id string, ageOlderThan time.Duration) {
	t.Helper()
	secs := ageOlderThan.Seconds()
	_, err := pool.Exec(ctx, `
		INSERT INTO operations (id, description, created_at, modified_at, done)
		VALUES ($1, 'orphan', now() - make_interval(secs => $2), now() - make_interval(secs => $2), false)`,
		id, secs)
	require.NoError(t, err)
}

func newReconciler(t *testing.T, pool *pgxpool.Pool, res operations.Resolver, rec operations.Recorder) *operations.Reconciler {
	t.Helper()
	return operations.NewReconciler(pool, res, operations.ReconcilerConfig{
		Schema:      "public",
		OrphanGrace: 1 * time.Minute,
		BatchSize:   100,
		Interval:    10 * time.Millisecond,
	}, operations.WithReconcilerRecorder(rec))
}

// Restart mid-Create, ресурс закоммичен → MarkDone(current resource).
func TestReconciler_Orphan_Create_Present(t *testing.T) {
	pool := setupPostgres(t)
	ctx := context.Background()
	repo := newRepo(pool)
	rec := operations.NewMemRecorder()

	insertOrphan(t, ctx, pool, "enp-create-present-001", 10*time.Minute)
	res := newFakeResolver()
	res.outcome["enp-create-present-001"] = operations.ResolverResult{
		Outcome:  operations.OutcomeDone,
		Response: mustAnyVal(t, "network-committed"),
	}

	rc := newReconciler(t, pool, res, rec)
	n, err := rc.Sweep(ctx)
	require.NoError(t, err)
	assert.Equal(t, 1, n)

	got, err := repo.Get(ctx, "enp-create-present-001")
	require.NoError(t, err)
	assert.True(t, got.Done)
	require.NotNil(t, got.Response)
	assert.Nil(t, got.Error)
	assert.GreaterOrEqual(t, rec.OrphansRecovered("done"), float64(1))
}

// Restart mid-Create, writer-TX откатилась (ресурс отсутствует) →
// MarkError(INTERNAL "operation interrupted before completion").
func TestReconciler_Orphan_Create_Absent(t *testing.T) {
	pool := setupPostgres(t)
	ctx := context.Background()
	repo := newRepo(pool)
	rec := operations.NewMemRecorder()

	insertOrphan(t, ctx, pool, "enp-create-absent-001", 10*time.Minute)
	res := newFakeResolver()
	res.outcome["enp-create-absent-001"] = operations.ResolverResult{Outcome: operations.OutcomeInterrupted}

	rc := newReconciler(t, pool, res, rec)
	_, err := rc.Sweep(ctx)
	require.NoError(t, err)

	got, err := repo.Get(ctx, "enp-create-absent-001")
	require.NoError(t, err)
	assert.True(t, got.Done)
	require.NotNil(t, got.Error)
	assert.Equal(t, int32(13), got.Error.GetCode())
	assert.Equal(t, "operation interrupted before completion", got.Error.GetMessage())
	assert.Nil(t, got.Response)
	assert.GreaterOrEqual(t, rec.OrphansRecovered("error"), float64(1))
}

// Restart mid-Delete, ресурс удален → MarkDone(Empty).
func TestReconciler_Orphan_Delete_Absent(t *testing.T) {
	pool := setupPostgres(t)
	ctx := context.Background()
	repo := newRepo(pool)
	rec := operations.NewMemRecorder()

	insertOrphan(t, ctx, pool, "enp-delete-absent-001", 10*time.Minute)
	res := newFakeResolver()
	res.outcome["enp-delete-absent-001"] = operations.ResolverResult{Outcome: operations.OutcomeDone} // Response nil = Empty

	rc := newReconciler(t, pool, res, rec)
	_, err := rc.Sweep(ctx)
	require.NoError(t, err)

	got, err := repo.Get(ctx, "enp-delete-absent-001")
	require.NoError(t, err)
	assert.True(t, got.Done)
	assert.Nil(t, got.Error)
}

// Restart mid-Delete, ресурс еще жив → MarkError(interrupted).
func TestReconciler_Orphan_Delete_Present(t *testing.T) {
	pool := setupPostgres(t)
	ctx := context.Background()
	repo := newRepo(pool)
	rec := operations.NewMemRecorder()

	insertOrphan(t, ctx, pool, "enp-delete-present-001", 10*time.Minute)
	res := newFakeResolver()
	res.outcome["enp-delete-present-001"] = operations.ResolverResult{Outcome: operations.OutcomeInterrupted}

	rc := newReconciler(t, pool, res, rec)
	_, err := rc.Sweep(ctx)
	require.NoError(t, err)

	got, err := repo.Get(ctx, "enp-delete-present-001")
	require.NoError(t, err)
	assert.True(t, got.Done)
	require.NotNil(t, got.Error)
	assert.Equal(t, "operation interrupted before completion", got.Error.GetMessage())
}

// Grace-окно защищает live-worker (свежая done=false строка не eligible).
func TestReconciler_GraceWindow_SkipsLiveWorker(t *testing.T) {
	pool := setupPostgres(t)
	ctx := context.Background()
	repo := newRepo(pool)
	rec := operations.NewMemRecorder()

	// Свежая (modified_at ~ now) → внутри grace → не трогается.
	op, err := operations.New("enp", "fresh inflight", nil)
	require.NoError(t, err)
	require.NoError(t, repo.Create(ctx, op))

	res := newFakeResolver()
	res.outcome[op.ID] = operations.ResolverResult{Outcome: operations.OutcomeDone, Response: mustAnyVal(t, "x")}

	rc := newReconciler(t, pool, res, rec)
	n, err := rc.Sweep(ctx)
	require.NoError(t, err)
	assert.Equal(t, 0, n, "свежая строка не eligible (внутри grace)")

	got, err := repo.Get(ctx, op.ID)
	require.NoError(t, err)
	assert.False(t, got.Done, "live-worker не разрешен преждевременно")
}

// Два reconciler-движка поверх одной DB — exactly-once через SKIP LOCKED.
func TestReconciler_TwoReplicas_SkipLocked_ExactlyOnce(t *testing.T) {
	pool := setupPostgres(t)
	ctx := context.Background()
	repo := newRepo(pool)
	rec := operations.NewMemRecorder()

	const orphans = 20
	res := newFakeResolver()
	for i := 0; i < orphans; i++ {
		id := "enp-many-" + itoa(i)
		insertOrphan(t, ctx, pool, id, 10*time.Minute)
		res.outcome[id] = operations.ResolverResult{Outcome: operations.OutcomeDone, Response: mustAnyVal(t, "r")}
	}

	rcA := newReconciler(t, pool, res, rec)
	rcB := newReconciler(t, pool, res, rec)

	doneCh := make(chan int, 2)
	go func() { _ = recoverAll(t, ctx, rcA); doneCh <- 0 }()
	go func() { _ = recoverAll(t, ctx, rcB); doneCh <- 0 }()
	<-doneCh
	<-doneCh

	// Каждый orphan разрешен ровно один раз: сумма recovered == числу orphan'ов.
	assert.Equal(t, float64(orphans), rec.OrphansRecovered("done"),
		"сумма orphans_recovered{done} == числу orphan'ов (exactly-once)")
	for i := 0; i < orphans; i++ {
		got, err := repo.Get(ctx, "enp-many-"+itoa(i))
		require.NoError(t, err)
		assert.True(t, got.Done)
	}
}

// Reconciler добивает невосстановимый MarkDone-fail (backstop).
func TestReconciler_RecoversPermanentTerminalWriteFail(t *testing.T) {
	pool := setupPostgres(t)
	ctx := context.Background()
	repo := newRepo(pool)
	rec := operations.NewMemRecorder()

	// Имитация строки с исчерпанными ретраями терминальной записи: done=false,
	// ресурс фактически закоммичен (resolver вернет Done), modified_at старше grace.
	insertOrphan(t, ctx, pool, "enp-perm-fail-001", 10*time.Minute)
	res := newFakeResolver()
	res.outcome["enp-perm-fail-001"] = operations.ResolverResult{Outcome: operations.OutcomeDone, Response: mustAnyVal(t, "recovered")}

	rc := newReconciler(t, pool, res, rec)
	_, err := rc.Sweep(ctx)
	require.NoError(t, err)

	got, err := repo.Get(ctx, "enp-perm-fail-001")
	require.NoError(t, err)
	assert.True(t, got.Done, "потерянная терминальная запись восстановлена reconciler'ом")
}

// Restart mid-Update, ресурс закоммичен → MarkDone(current committed).
func TestReconciler_Orphan_Update_Present(t *testing.T) {
	pool := setupPostgres(t)
	ctx := context.Background()
	repo := newRepo(pool)
	rec := operations.NewMemRecorder()

	insertOrphan(t, ctx, pool, "enp-update-present-001", 10*time.Minute)
	res := newFakeResolver()
	res.outcome["enp-update-present-001"] = operations.ResolverResult{
		Outcome:  operations.OutcomeDone,
		Response: mustAnyVal(t, "network-current-committed"),
	}

	rc := newReconciler(t, pool, res, rec)
	_, err := rc.Sweep(ctx)
	require.NoError(t, err)

	got, err := repo.Get(ctx, "enp-update-present-001")
	require.NoError(t, err)
	assert.True(t, got.Done)
	require.NotNil(t, got.Response)
	assert.Nil(t, got.Error)
}

// Повторное создание Reconciler'а поверх ТОЙ ЖЕ DB (имитация
// рестарта процесса) разрешает осиротевшую операцию — данные не теряются.
func TestReconciler_RestartRecovery_NewEngineSameDB(t *testing.T) {
	pool := setupPostgres(t)
	ctx := context.Background()
	repo := newRepo(pool)

	insertOrphan(t, ctx, pool, "enp-restart-001", 10*time.Minute)

	// «Рестарт»: совершенно новый Reconciler + Recorder поверх той же DB.
	rec := operations.NewMemRecorder()
	res := newFakeResolver()
	res.outcome["enp-restart-001"] = operations.ResolverResult{Outcome: operations.OutcomeDone, Response: mustAnyVal(t, "after-restart")}
	rc := newReconciler(t, pool, res, rec)

	require.NoError(t, recoverAll(t, ctx, rc))

	got, err := repo.Get(ctx, "enp-restart-001")
	require.NoError(t, err)
	assert.True(t, got.Done, "после рестарта движка осиротевшая операция терминальна")
}

func recoverAll(t *testing.T, ctx context.Context, rc *operations.Reconciler) error {
	t.Helper()
	return rc.RecoverAll(ctx)
}
