// Package operations — Long-Running Operations primitive: Worker для
// async-исполнения мутаций + Repo для перехода `done=false → true`.
//
// Operations.Run() — fire-and-trigger pattern: handler возвращает Operation
// клиенту сразу, фоновый worker делает реальную работу и обновляет row.
//
// Worker-lifecycle (Worker type):
//   - Run() / RunWithWorker() запускают goroutine, регистрируют её в
//     pkg-level registry (sync.WaitGroup + active counter).
//   - Wait(ctx) блокируется пока все активные workers не вернут или ctx
//     истечёт. Используется в graceful-shutdown handler'а сервиса.
//   - panic в fn перехватывается через recover() → MarkError, не валит
//     процесс целиком.
//
// Context-propagation (baggage):
//   - Worker НЕ наследует deadline / cancel callerCtx — request-ctx
//     cancel-ится сразу как handler возвращает Operation клиенту, а worker
//     должен жить независимо.
//   - Worker НАСЛЕДУЕТ observability-values callerCtx (OTel SpanContext,
//     request-id, slog logger, tenant claims, любые WithValue-ключи) через
//     baggage.Extract — иначе worker-логи и trace-span'ы оторваны от
//     исходного запроса (AP-3 в evgeniy skill).
package operations

import (
	"context"
	"errors"
	"fmt"
	"runtime/debug"
	"sync"
	"sync/atomic"
	"time"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/types/known/anypb"

	"github.com/PRO-Robotech/kacho-corelib/baggage"
)

// defaultRegistry — pkg-level worker tracker. Используется Run() для
// backward-compatibility (без явного Worker type). Production-сервисы
// должны вызывать defaultRegistry.Wait(ctx) перед exit.
var defaultRegistry = NewWorker()

// Worker — координатор активных async worker-горутин.
//
// Назначение: graceful-shutdown сервиса не должен терять in-flight операции.
// Без этого SIGTERM → grpcSrv.GracefulStop() → возврат handler'у → exit
// процесса посреди INSERT/Allocate. Operation остаётся `done=false` навсегда,
// клиент висит в polling. Race-window для data-loss (особенно для inline
// allocator: IP записан, но MarkDone не отправлен — клиент думает fail,
// retry → второй IP).
type Worker struct {
	wg     sync.WaitGroup
	active atomic.Int64
}

// NewWorker — новый изолированный registry. Тесты должны его использовать
// вместо defaultRegistry чтобы не мешать друг другу.
func NewWorker() *Worker { return &Worker{} }

// Active — текущее число запущенных но не завершённых worker'ов
// (для observability / metrics).
func (w *Worker) Active() int64 { return w.active.Load() }

// Wait блокируется пока все active workers не завершатся, либо ctx истечёт.
// Возвращает nil при штатном drain'е, ctx.Err() при таймауте.
//
// Использование в cmd/<svc>/main.go::run:
//
//	<-ctx.Done()
//	grpcSrv.GracefulStop()
//	shutCtx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
//	defer cancel()
//	if err := operations.Wait(shutCtx); err != nil {
//	    logger.Warn("workers did not finish in time", "err", err, "active", operations.Active())
//	}
func (w *Worker) Wait(ctx context.Context) error {
	done := make(chan struct{})
	go func() {
		w.wg.Wait()
		close(done)
	}()
	select {
	case <-done:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

// runOn — internal launcher: increments WG + active, defer'ed decrement,
// recover() перехватывает panic в fn → MarkError, не валит процесс.
//
// callerCtx — request-ctx handler-а. Worker НЕ наследует его deadline /
// cancel, но через baggage.Extract сохраняет все Values (OTel SpanContext,
// request-id, slog logger, tenant claims) — иначе worker-логи и трейсы
// оторваны от исходного запроса.
func (w *Worker) runOn(callerCtx context.Context, repo Repo, opID string, fn func(context.Context) (*anypb.Any, error)) {
	w.wg.Add(1)
	w.active.Add(1)
	go func() {
		defer w.wg.Done()
		defer w.active.Add(-1)

		// baggage.Extract сохраняет observability-values из callerCtx
		// (OTel span, request-id, slog logger, tenant claims), но
		// отрезает deadline/cancel — worker автономен.
		workerCtx := baggage.Extract(callerCtx)
		var resp *anypb.Any
		var err error

		// recover panic → MarkError. Без этого panic в fn (например, nil-deref
		// в новом repo-методе) роняет весь процесс.
		func() {
			defer func() {
				if r := recover(); r != nil {
					stack := debug.Stack()
					err = fmt.Errorf("panic in operation worker: %v\n%s", r, stack)
				}
			}()
			resp, err = fn(workerCtx)
		}()

		if err != nil {
			// Если err уже gRPC-status — используем его. Иначе — Internal
			// (panic / unwrapped → не leak'аем raw text в Operation.error).
			st, ok := status.FromError(err)
			if !ok || st.Code() == codes.Unknown {
				st = status.New(codes.Internal, "internal worker error")
			}
			// MarkError use собственный context на случай shutdown — fresh
			// ctx с коротким timeout.
			markCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			_ = repo.MarkError(markCtx, opID, st.Proto())
			cancel()
			return
		}
		markCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		_ = repo.MarkDone(markCtx, opID, resp)
		cancel()
	}()
}

// Run — backward-compatible API: запускает worker в default-registry.
//
// callerCtx — request-context handler-а. Из него ИЗВЛЕКАЮТСЯ значимые values
// (OTel SpanContext, request-id, slog logger, tenant claims) через
// baggage.Extract — они propagate'ятся в worker-ctx и в fn. Это критично
// для distributed-tracing и structured-logging: без этого worker-логи и
// trace-span'ы оторваны от исходного запроса (см. AP-3 в evgeniy skill).
//
// При этом worker НЕ наследует deadline/cancel callerCtx: handler возвращает
// Operation клиенту сразу, request-ctx cancel-ится через миллисекунды, а
// worker должен жить независимо до завершения. Если внутри fn нужен timeout —
// fn оборачивает свой workerCtx в context.WithTimeout сам.
func Run(callerCtx context.Context, repo Repo, opID string, fn func(context.Context) (*anypb.Any, error)) {
	defaultRegistry.runOn(callerCtx, repo, opID, fn)
}

// RunWithWorker — вариант для тестов / multi-tenant сервисов: использует
// явный Worker registry вместо default'ного. Семантика callerCtx — как у Run.
func RunWithWorker(w *Worker, callerCtx context.Context, repo Repo, opID string, fn func(context.Context) (*anypb.Any, error)) {
	w.runOn(callerCtx, repo, opID, fn)
}

// Wait — pkg-level: ждёт окончания всех active workers в default-registry.
func Wait(ctx context.Context) error { return defaultRegistry.Wait(ctx) }

// Active — pkg-level: число active workers в default-registry.
func Active() int64 { return defaultRegistry.Active() }

// ErrShutdownTimeout sentinel для caller'ов которые хотят отличить
// "drain ok" vs "timeout".
var ErrShutdownTimeout = errors.New("operations: workers did not finish before shutdown timeout")
