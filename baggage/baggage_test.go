package baggage_test

import (
	"context"
	"errors"
	"log/slog"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.opentelemetry.io/otel/trace"

	"github.com/PRO-Robotech/kacho-corelib/baggage"
)

type ctxKey string

// TestExtract_CustomValuePropagated — произвольный WithValue-ключ
// (например, request-id, tenant-id) сохраняется в worker-ctx.
func TestExtract_CustomValuePropagated(t *testing.T) {
	const key ctxKey = "request-id"
	callerCtx := context.WithValue(context.Background(), key, "req-abc-123")

	workerCtx := baggage.Extract(callerCtx)

	assert.Equal(t, "req-abc-123", workerCtx.Value(key),
		"WithValue-ключ должен propagate'иться в worker через baggage.Extract")
}

// TestExtract_MultipleValuesPropagated — несколько разных values
// (имитация: request-id + tenant-id + locale).
func TestExtract_MultipleValuesPropagated(t *testing.T) {
	const (
		keyRequestID ctxKey = "request-id"
		keyTenantID  ctxKey = "tenant-id"
		keyLocale    ctxKey = "locale"
	)

	ctx := context.Background()
	ctx = context.WithValue(ctx, keyRequestID, "req-1")
	ctx = context.WithValue(ctx, keyTenantID, "tenant-42")
	ctx = context.WithValue(ctx, keyLocale, "ru-RU")

	workerCtx := baggage.Extract(ctx)

	assert.Equal(t, "req-1", workerCtx.Value(keyRequestID))
	assert.Equal(t, "tenant-42", workerCtx.Value(keyTenantID))
	assert.Equal(t, "ru-RU", workerCtx.Value(keyLocale))
}

// TestExtract_OTelSpanContextPropagated — OpenTelemetry SpanContext
// сохраняется (trace.SpanContextFromContext возвращает тот же id).
// Это самый критичный сценарий: distributed-trace должен соединить
// request-span с worker-span'ами.
func TestExtract_OTelSpanContextPropagated(t *testing.T) {
	traceID, err := trace.TraceIDFromHex("0123456789abcdef0123456789abcdef")
	require.NoError(t, err)
	spanID, err := trace.SpanIDFromHex("0123456789abcdef")
	require.NoError(t, err)

	sc := trace.NewSpanContext(trace.SpanContextConfig{
		TraceID:    traceID,
		SpanID:     spanID,
		TraceFlags: trace.FlagsSampled,
		Remote:     true,
	})
	callerCtx := trace.ContextWithSpanContext(context.Background(), sc)

	workerCtx := baggage.Extract(callerCtx)

	gotSC := trace.SpanContextFromContext(workerCtx)
	assert.True(t, gotSC.IsValid(), "SpanContext должен быть valid после Extract")
	assert.Equal(t, traceID, gotSC.TraceID(), "TraceID должен propagate'иться")
	assert.Equal(t, spanID, gotSC.SpanID(), "SpanID должен propagate'иться")
	assert.True(t, gotSC.IsSampled(), "TraceFlags должны сохраниться")
}

// TestExtract_SlogLoggerPropagated — slog.Logger сохранённый в ctx
// через WithValue (типичный паттерн для structured logging в gRPC-interceptor'ах)
// доступен в worker-ctx.
func TestExtract_SlogLoggerPropagated(t *testing.T) {
	const loggerKey ctxKey = "slog-logger"
	logger := slog.Default().With("svc", "kacho-vpc", "rpc", "Network.Create")
	callerCtx := context.WithValue(context.Background(), loggerKey, logger)

	workerCtx := baggage.Extract(callerCtx)

	got, ok := workerCtx.Value(loggerKey).(*slog.Logger)
	require.True(t, ok, "slog.Logger должен быть доступен под тем же ключом")
	assert.NotNil(t, got)
}

// TestExtract_DeadlineNotInherited — caller-ctx с deadline 50ms,
// worker-ctx после Extract не имеет этого deadline.
func TestExtract_DeadlineNotInherited(t *testing.T) {
	callerCtx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()

	workerCtx := baggage.Extract(callerCtx)

	_, hasDeadline := workerCtx.Deadline()
	assert.False(t, hasDeadline,
		"worker-ctx не должен наследовать deadline caller-ctx (worker автономен)")
}

// TestExtract_CallerCancelDoesNotPropagate — cancel() на caller-ctx
// не cancel-ит worker-ctx; worker продолжает жить, пока его собственный
// cancel/timeout не сработает.
func TestExtract_CallerCancelDoesNotPropagate(t *testing.T) {
	callerCtx, cancel := context.WithCancel(context.Background())
	workerCtx := baggage.Extract(callerCtx)

	cancel() // Cancel caller — имитация handler returns.

	// Маленькая пауза чтобы убедиться, что если бы cancel propagate'ил,
	// он бы уже сработал.
	time.Sleep(20 * time.Millisecond)

	select {
	case <-workerCtx.Done():
		t.Fatal("worker-ctx не должен cancel'иться когда caller-ctx cancel'ится")
	default:
		// OK — worker autonomous.
	}
	assert.NoError(t, workerCtx.Err(),
		"worker-ctx.Err() должен быть nil несмотря на caller cancel")
}

// TestExtract_CallerDeadlineExpiresWorkerStillAlive — caller-ctx
// с очень коротким deadline истекает, worker-ctx продолжает жить
// (это композиция предыдущих двух тестов в более production-like сценарии).
func TestExtract_CallerDeadlineExpiresWorkerStillAlive(t *testing.T) {
	callerCtx, cancel := context.WithTimeout(context.Background(), 10*time.Millisecond)
	defer cancel()

	workerCtx := baggage.Extract(callerCtx)

	// Ждём истечения caller-deadline.
	<-callerCtx.Done()
	assert.True(t, errors.Is(callerCtx.Err(), context.DeadlineExceeded),
		"caller-ctx должен истечь")

	// worker-ctx по-прежнему не cancel'ится.
	select {
	case <-workerCtx.Done():
		t.Fatal("worker-ctx не должен cancel'иться по caller-deadline")
	default:
	}
}

// TestExtract_NilCallerCtxSafe — defensive: Extract(nil) не паникует,
// возвращает Background.
func TestExtract_NilCallerCtxSafe(t *testing.T) {
	//nolint:staticcheck // SA1012: intentional nil for defensive check
	workerCtx := baggage.Extract(nil)
	require.NotNil(t, workerCtx)
	assert.NoError(t, workerCtx.Err())
}

// TestExtract_BackgroundCallerCtxOK — defensive: Extract(Background) работает.
func TestExtract_BackgroundCallerCtxOK(t *testing.T) {
	workerCtx := baggage.Extract(context.Background())
	require.NotNil(t, workerCtx)
	assert.NoError(t, workerCtx.Err())
	_, hasDeadline := workerCtx.Deadline()
	assert.False(t, hasDeadline)
}

// TestExtract_WorkerOwnTimeoutWorks — после Extract caller оборачивает
// в WithTimeout — этот timeout уже работает (worker управляет своим
// lifecycle).
func TestExtract_WorkerOwnTimeoutWorks(t *testing.T) {
	callerCtx := context.Background()
	workerCtx := baggage.Extract(callerCtx)

	workerCtx, cancel := context.WithTimeout(workerCtx, 20*time.Millisecond)
	defer cancel()

	<-workerCtx.Done()
	assert.True(t, errors.Is(workerCtx.Err(), context.DeadlineExceeded),
		"worker-ctx должен cancel'иться по своему собственному timeout")
}
