// Package shutdown — graceful-shutdown helper для cmd/<svc>/main.go.
//
// Контракт: один Manager на процесс. Регистрируем cleanup-handlers; при
// SIGTERM/SIGINT (или ctx.Cancel) хэндлеры выполняются в LIFO-порядке
// (close-в-обратном-порядке-открытия).
//
// Минимальная самодостаточная реализация (без внешних зависимостей).
package shutdown

import (
	"context"
	"errors"
	"os"
	"os/signal"
	"sync"
	"syscall"
)

// Handler — функция-cleanup. Должна быть идемпотентной и не блокироваться > 5s.
type Handler func() error

// Manager собирает cleanup-handlers и выполняет их в LIFO при сигнале или Close().
type Manager struct {
	mu       sync.Mutex
	handlers []Handler
	closed   chan struct{}
	once     sync.Once
	err      error
	cancel   context.CancelFunc
	doneCtx  context.Context
}

// New создаёт Manager и стартует goroutine-listener для SIGINT/SIGTERM.
//
// Listener неблокирующий: первый сигнал инициирует shutdown (вызов Close());
// последующие сигналы игнорируются (если процесс «завис», admin делает kill -9).
func New() *Manager {
	doneCtx, cancel := context.WithCancel(context.Background())
	m := &Manager{
		closed:  make(chan struct{}),
		cancel:  cancel,
		doneCtx: doneCtx,
	}
	go m.listenSignals()
	return m
}

// OnExit регистрирует один или несколько handlers. Они выполнятся в обратном
// порядке регистрации (LIFO) при shutdown.
//
// Можно вызывать многократно из любой goroutine.
func (m *Manager) OnExit(handlers ...Handler) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.handlers = append(m.handlers, handlers...)
}

// Wait блокируется до получения сигнала + завершения всех handlers, либо до
// отмены ctx. Возвращает первую ошибку handler-а или ctx.Err().
func (m *Manager) Wait(ctx context.Context) error {
	select {
	case <-m.closed:
		return m.err
	case <-ctx.Done():
		// Если внешний ctx отменён до сигнала — инициируем shutdown сами.
		_ = m.Close()
		<-m.closed
		if m.err != nil {
			return m.err
		}
		return ctx.Err()
	}
}

// Close — программный shutdown без сигнала. Идемпотентен (повторный вызов no-op).
func (m *Manager) Close() error {
	m.once.Do(func() {
		m.mu.Lock()
		hs := append([]Handler(nil), m.handlers...)
		m.mu.Unlock()

		// LIFO: последний зарегистрированный — первый выполняется.
		var firstErr error
		for i := len(hs) - 1; i >= 0; i-- {
			if err := hs[i](); err != nil && firstErr == nil {
				firstErr = err
			}
		}
		m.err = firstErr
		m.cancel()
		close(m.closed)
	})
	return m.err
}

// listenSignals — внутренний worker. Завершается при первом сигнале или Close().
func (m *Manager) listenSignals() {
	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM)
	defer signal.Stop(sigCh)

	select {
	case <-sigCh:
		_ = m.Close()
	case <-m.closed:
		// Close() уже вызван
	}
}

// ErrAlreadyClosed возвращается если попытка зарегистрировать handler после Close().
var ErrAlreadyClosed = errors.New("shutdown manager already closed")
