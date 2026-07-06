// Copyright (c) PRO-Robotech
// SPDX-License-Identifier: BUSL-1.1

package operations

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"google.golang.org/grpc/codes"
	grpcstatus "google.golang.org/grpc/status"
	"google.golang.org/protobuf/types/known/anypb"
)

// interruptedMsg — frozen INTERNAL-текст для orphan'а, не доведшего работу до
// commit. Намеренно отличается от worker-сбоя ("internal worker error"): оператор
// по тексту отличает live-worker-сбой от reconciler-разрешенного orphan'а.
const interruptedMsg = "operation interrupted before completion"

// TerminalOutcome — терминальный исход, который доменный Resolver вычислил по
// committed-реальности ресурса.
type TerminalOutcome int

const (
	// OutcomeSkip — committed-реальность не позволяет уверенно разрешить операцию
	// в этом прогоне (resolver не смог прочитать ресурс / неоднозначно); строка
	// остается done=false, sweep повторится позже.
	OutcomeSkip TerminalOutcome = iota
	// OutcomeDone — работа фактически закоммичена → MarkDone(Response). Для Create/
	// Update Response — текущий ресурс; для Delete Response может быть nil (Empty).
	OutcomeDone
	// OutcomeInterrupted — работа не дошла до commit (ресурс отсутствует для
	// Create/Update либо еще жив для Delete) → MarkError(interrupted).
	OutcomeInterrupted
)

// ResolverResult — решение Resolver'а по одной осиротевшей операции.
type ResolverResult struct {
	Outcome  TerminalOutcome
	Response *anypb.Any // используется при OutcomeDone (nil → google.protobuf.Empty-семантика)
}

// Resolver — доменный порт (реализуется сервисом): по метаданным осиротевшей
// операции определяет ее терминальный исход, сверяясь с committed-реальностью
// ресурса (repo.Get по resource_id из metadata). Движок reconciler'а — в corelib,
// resolver — в сервисе (знает типы метаданных и таблицы ресурсов).
//
// Контракт диспетчеризации (vpc-style):
//   - Create/Update-метаданные: ресурс присутствует → {OutcomeDone, current},
//     отсутствует → {OutcomeInterrupted}.
//   - Delete-метаданные: ресурс отсутствует → {OutcomeDone, nil(Empty)},
//     присутствует → {OutcomeInterrupted}.
//   - transient-ошибка чтения ресурса → возврат (ResolverResult{}, err): движок
//     инкрементит reconcile_errors и пропускает orphan до следующего sweep'а.
type Resolver interface {
	Resolve(ctx context.Context, op Operation) (ResolverResult, error)
}

// ReconcilerConfig параметризует Reconciler.
type ReconcilerConfig struct {
	// Schema — schema-квалификатор таблицы operations ("public" / "kacho_vpc").
	Schema string
	// OrphanGrace — минимальный возраст (по modified_at) кандидата-orphan'а.
	// Должен превышать максимальную ожидаемую длительность операции, чтобы не
	// разрешать преждевременно еще-живого worker'а. Дефолт 5m.
	OrphanGrace time.Duration
	// BatchSize — размер пачки claim'а за один sweep. Дефолт 100.
	BatchSize int
	// Interval — период периодического sweep'а (Run). Дефолт 30s.
	Interval time.Duration
	// ResolveTimeout — жёсткий потолок времени одного Resolver.Resolve внутри
	// claim-транзакции. Sweep держит claim-tx (FOR UPDATE SKIP LOCKED + пул-коннект)
	// открытой на всё время резолва пачки; без потолка зависший доменный Resolve
	// (напр. peer-вызов без deadline) оставил бы tx idle-in-transaction на
	// неограниченное время — блокируя VACUUM и удерживая коннект. Потолок гарантирует
	// прогресс sweep'а. Дефолт 10s; ≤0 → default.
	ResolveTimeout time.Duration
	// SweepBudget — жёсткий потолок СУММАРНОЙ длительности claim-транзакции одного
	// Sweep'а. ResolveTimeout ограничивает лишь ОДИН резолв; без агрегатного потолка
	// claim-tx могла бы жить до BatchSize×ResolveTimeout (~1000s при outage), удерживая
	// пул-коннект, FOR UPDATE row-locks и xmin-горизонт operations-таблицы против
	// VACUUM на всё окно. При исчерпании бюджета Sweep коммитит
	// уже разрешённое и выходит; неразрешённые orphan'ы остаются durable (done=false)
	// и добираются следующим Sweep'ом. Дефолт 30s; ≤0 → default.
	SweepBudget time.Duration
}

func (c ReconcilerConfig) withDefaults() ReconcilerConfig {
	if c.OrphanGrace <= 0 {
		c.OrphanGrace = 5 * time.Minute
	}
	if c.BatchSize <= 0 {
		c.BatchSize = 100
	}
	if c.Interval <= 0 {
		c.Interval = 30 * time.Second
	}
	if c.ResolveTimeout <= 0 {
		c.ResolveTimeout = 10 * time.Second
	}
	if c.SweepBudget <= 0 {
		c.SweepBudget = 30 * time.Second
	}
	return c
}

// ReconcilerOption — функциональная опция Reconciler.
type ReconcilerOption func(*Reconciler)

// WithReconcilerRecorder подключает sink метрик reconcile (runs/errors/orphans).
func WithReconcilerRecorder(r Recorder) ReconcilerOption {
	return func(rc *Reconciler) {
		if r != nil {
			rc.rec = r
		}
	}
}

// WithReconcilerLogger подключает структурированный логгер.
func WithReconcilerLogger(l *slog.Logger) ReconcilerOption {
	return func(rc *Reconciler) {
		if l != nil {
			rc.log = l
		}
	}
}

// Reconciler — startup + периодический backstop: разрешает осиротевшие in-flight
// операции (умершего процесса) в терминал по committed-реальности ресурса.
// Claim через FOR UPDATE SKIP LOCKED — конкурентные reconciler'ы реплик
// партиционируют множество (exactly-once), а не дерутся. Терминальная запись —
// через тот же CAS-on-`done` (идемпотентна с live-worker'ом).
type Reconciler struct {
	pool           *pgxpool.Pool
	resolver       Resolver
	rec            Recorder
	log            *slog.Logger
	table          string
	grace          time.Duration
	batch          int
	interval       time.Duration
	resolveTimeout time.Duration
	sweepBudget    time.Duration
}

// NewReconciler конструирует Reconciler. pool/resolver обязательны.
func NewReconciler(pool *pgxpool.Pool, resolver Resolver, cfg ReconcilerConfig, opts ...ReconcilerOption) *Reconciler {
	cfg = cfg.withDefaults()
	rc := &Reconciler{
		pool:           pool,
		resolver:       resolver,
		rec:            NopRecorder{},
		log:            slog.Default(),
		table:          pgx.Identifier{cfg.Schema, "operations"}.Sanitize(),
		grace:          cfg.OrphanGrace,
		batch:          cfg.BatchSize,
		interval:       cfg.Interval,
		resolveTimeout: cfg.ResolveTimeout,
		sweepBudget:    cfg.SweepBudget,
	}
	for _, o := range opts {
		o(rc)
	}
	return rc
}

// Sweep — один прогон: claim пачки осиротевших операций (FOR UPDATE SKIP LOCKED)
// и разрешение каждой через Resolver в терминал. Терминальная запись — внутри той
// же транзакции, что держит claim-lock (exactly-once между репликами). Возвращает
// число разрешенных.
func (rc *Reconciler) Sweep(ctx context.Context) (int, error) {
	rc.rec.IncReconcileRuns()

	tx, err := rc.pool.Begin(ctx)
	if err != nil {
		rc.rec.IncReconcileErrors()
		return 0, fmt.Errorf("operations.Reconciler.Sweep: begin: %w", err)
	}
	defer func() { _ = tx.Rollback(context.Background()) }()

	threshold := time.Now().UTC().Add(-rc.grace)
	claimSQL := fmt.Sprintf(`
		SELECT %s
		FROM %s
		WHERE done = false AND modified_at < $1
		ORDER BY modified_at ASC
		FOR UPDATE SKIP LOCKED
		LIMIT $2`,
		opColumns, rc.table)

	rows, err := tx.Query(ctx, claimSQL, threshold, rc.batch)
	if err != nil {
		rc.rec.IncReconcileErrors()
		return 0, fmt.Errorf("operations.Reconciler.Sweep: claim: %w", err)
	}
	var orphans []Operation
	for rows.Next() {
		op, scanErr := scanOperation(rows)
		if scanErr != nil {
			rows.Close()
			rc.rec.IncReconcileErrors()
			return 0, fmt.Errorf("operations.Reconciler.Sweep: scan: %w", scanErr)
		}
		orphans = append(orphans, *op)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		rc.rec.IncReconcileErrors()
		return 0, fmt.Errorf("operations.Reconciler.Sweep: rows: %w", err)
	}

	resolved := 0
	// Агрегатный потолок длительности claim-tx: как только суммарное время резолва
	// пачки превышает SweepBudget, обрываем батч и коммитим уже разрешённое. Проверка
	// в начале итерации — не начинаем новый резолв (до ResolveTimeout каждый), если
	// бюджет уже исчерпан. Неразрешённые orphan'ы durable (done=false) → следующий
	// Sweep (не держим claim-tx до BatchSize×ResolveTimeout).
	sweepDeadline := time.Now().Add(rc.sweepBudget)
	for i := range orphans {
		if time.Now().After(sweepDeadline) {
			rc.log.Warn("reconciler: sweep budget exhausted, committing partial batch",
				"resolved", resolved, "claimed", len(orphans))
			break
		}
		op := orphans[i]
		res, rerr := rc.resolveOne(ctx, op)
		if rerr != nil {
			rc.rec.IncReconcileErrors()
			rc.log.Warn("reconciler: resolver error, skipping orphan", "op", op.ID, "err", rerr)
			rc.keepClaimAlive(ctx, tx)
			continue
		}
		switch res.Outcome {
		case OutcomeSkip:
			rc.keepClaimAlive(ctx, tx)
			continue
		case OutcomeDone:
			if err := markDoneCAS(ctx, tx, rc.table, op.ID, res.Response); err != nil && !errors.Is(err, ErrAlreadyDone) {
				rc.rec.IncReconcileErrors()
				rc.log.Warn("reconciler: MarkDone failed", "op", op.ID, "err", err)
				continue
			}
			rc.rec.IncOrphansRecovered("done")
			resolved++
		case OutcomeInterrupted:
			st := grpcstatus.New(codes.Internal, interruptedMsg).Proto()
			if err := markErrorCAS(ctx, tx, rc.table, op.ID, st); err != nil && !errors.Is(err, ErrAlreadyDone) {
				rc.rec.IncReconcileErrors()
				rc.log.Warn("reconciler: MarkError failed", "op", op.ID, "err", err)
				continue
			}
			rc.rec.IncOrphansRecovered("error")
			resolved++
		}
	}

	if err := tx.Commit(ctx); err != nil {
		rc.rec.IncReconcileErrors()
		return 0, fmt.Errorf("operations.Reconciler.Sweep: commit: %w", err)
	}
	return resolved, nil
}

// resolveOne вызывает доменный Resolver под жёстким per-item потолком времени
// (resolveTimeout). Потолок ограничивает, сколько claim-транзакция может простоять
// idle-in-transaction на одном резолве (см. ReconcilerConfig.ResolveTimeout).
// resolveTimeout ≤ 0 → без обёртки (для явного отключения в тестах).
func (rc *Reconciler) resolveOne(ctx context.Context, op Operation) (ResolverResult, error) {
	if rc.resolveTimeout <= 0 {
		return rc.resolver.Resolve(ctx, op)
	}
	rctx, cancel := context.WithTimeout(ctx, rc.resolveTimeout)
	defer cancel()
	return rc.resolver.Resolve(rctx, op)
}

// keepClaimAlive исполняет дешёвый no-op statement на claim-транзакции, сбрасывая
// серверный idle_in_transaction_session_timeout. Ветки OutcomeDone/OutcomeInterrupted
// сбрасывают idle-таймер неявно через markDoneCAS/markErrorCAS; ветки OutcomeSkip и
// resolver-error НЕ пишут в claim-коннект, поэтому без этого «касания» серия
// последовательных skip'ов (каждый сжигает до ResolveTimeout idle на ДРУГОМ
// коннекте, пока claim-tx простаивает) накопила бы idle сверх серверного потолка →
// Postgres прибил бы claim-tx (SQLSTATE 25P03) → tx.Commit / следующий markDoneCAS
// упали бы, и ВЕСЬ батч (включая быстро-разрешимые орфаны дальше по списку)
// откатился бы — LRO-recovery не прогрессировал бы именно во время peer-outage,
// ради восстановления которого reconciler и существует (CWE-400). Так непрерывный
// idle claim-tx ограничен одним ResolveTimeout, а не суммой по батчу.
//
// Ошибка keep-alive лишь логируется: если claim-tx уже мертва, реальную причину
// поднимет последующий markDoneCAS / tx.Commit.
func (rc *Reconciler) keepClaimAlive(ctx context.Context, tx pgx.Tx) {
	if _, err := tx.Exec(ctx, "SELECT 1"); err != nil {
		rc.log.Warn("reconciler: claim-tx keep-alive failed", "err", err)
	}
}

// RecoverAll прогоняет Sweep до тех пор, пока очередной прогон не разрешит 0
// операций (backlog осиротевших исчерпан / остались только нерезолвимые в этом
// проходе). Зовется на старте ДО приема трафика.
func (rc *Reconciler) RecoverAll(ctx context.Context) error {
	for {
		n, err := rc.Sweep(ctx)
		if err != nil {
			return err
		}
		if n == 0 {
			return nil
		}
		if err := ctx.Err(); err != nil {
			return err
		}
	}
}

// Run — периодический backstop: Sweep на каждом тике до отмены ctx. Ошибки
// отдельного прогона логируются (loop не умирает на transient-сбое).
func (rc *Reconciler) Run(ctx context.Context) {
	t := time.NewTicker(rc.interval)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			if _, err := rc.Sweep(ctx); err != nil {
				rc.log.Warn("reconciler: sweep error", "err", err)
			}
		}
	}
}
