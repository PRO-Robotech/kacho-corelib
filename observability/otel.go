package observability

import (
	"context"
	"os"
)

// ShutdownFn — функция завершения работы провайдера телеметрии.
type ShutdownFn func(context.Context) error

// InitOtel инициализирует OTLP-провайдер телеметрии.
// Если переменная KACHO_OTEL_EXPORTER_OTLP_ENDPOINT не задана — возвращает no-op функцию.
// Полная OTLP-инициализация — TODO в sub-phase 0.2+.
func InitOtel(ctx context.Context, serviceName string) (ShutdownFn, error) {
	endpoint := os.Getenv("KACHO_OTEL_EXPORTER_OTLP_ENDPOINT")
	if endpoint == "" {
		return func(context.Context) error { return nil }, nil
	}
	// Полная OTLP-инициализация — TODO в 0.2+
	return func(context.Context) error { return nil }, nil
}
