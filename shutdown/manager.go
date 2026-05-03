// Package shutdown — graceful-shutdown helper поверх
// `github.com/H-BF/corlib/pkg/signals.AtExitManager`.
//
// Контракт: в `cmd/<svc>/main.go` создаётся один Manager, к нему регистрируются
// shutdown-handlers (close pgxpool, GracefulStop gRPC, close watch.Hub).
// При SIGINT/SIGTERM хэндлеры выполняются в обратном порядке регистрации
// (LIFO — close-в-обратном-порядке-открытия).
package shutdown

import (
	"context"

	"github.com/H-BF/corlib/pkg/signals"
)

// Handler — функция-cleanup. Должна быть идемпотентной и не блокироваться > 5s.
type Handler func() error

// Manager — обёртка над signals.AtExitManager.
type Manager struct {
	mgr *signals.AtExitManager
}

// New создаёт Manager. Listen-worker сразу подписывается на SIGINT/SIGTERM —
// при первом сигнале вызывает зарегистрированные handlers в LIFO-порядке.
func New() *Manager {
	return &Manager{mgr: signals.NewAtExitManager()}
}

// OnExit регистрирует handler, который выполнится при SIGTERM/SIGINT.
// Можно вызывать многократно — handlers выполняются в обратном порядке регистрации.
func (m *Manager) OnExit(handlers ...Handler) {
	asAtExit := make([]func() error, 0, len(handlers))
	for _, h := range handlers {
		asAtExit = append(asAtExit, h)
	}
	m.mgr.WhenSignalExit(asAtExit...)
}

// Wait блокируется до получения сигнала + завершения всех handlers,
// либо до отмены ctx.
//
// Возвращает первую ошибку handler-а (если была) или ctx.Err().
func (m *Manager) Wait(ctx context.Context) error {
	return m.mgr.Wait4Closed(ctx)
}

// Close — программный shutdown без сигнала. Вызывает handlers и завершает Wait.
func (m *Manager) Close() error {
	return m.mgr.Close()
}
