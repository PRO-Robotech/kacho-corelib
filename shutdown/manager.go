// Copyright (c) PRO-Robotech
// SPDX-License-Identifier: BUSL-1.1

// Package shutdown — graceful-shutdown helper для cmd/<svc>/main.go.
//
// Контракт: один Manager на процесс. Регистрируем cleanup-handlers; при
// SIGTERM/SIGINT (или ctx.Cancel) хэндлеры выполняются в LIFO-порядке
// (close-в-обратном-порядке-открытия). Каждый handler ограничен timeout'ом
// (WithHandlerTimeout, дефолт 10s): зависший cleanup не задерживает завершение
// процесса бесконечно — он бросается, фиксируется ErrHandlerTimeout, остальные
// продолжают выполняться.
//
// Минимальная самодостаточная реализация (без внешних зависимостей).
package shutdown

import (
	"context"
	"errors"
	"log/slog"
	"os"
	"os/signal"
	"sync"
	"syscall"
	"time"
)

// defaultHandlerTimeout — верхняя граница выполнения одного cleanup-handler'а.
const defaultHandlerTimeout = 10 * time.Second

// Handler — функция-cleanup. Должна быть идемпотентной и завершаться в пределах
// handler-timeout'а; иначе она бросается (ErrHandlerTimeout), shutdown продолжается.
type Handler func() error

// ErrHandlerTimeout фиксируется, когда cleanup-handler не уложился в
// handler-timeout. Возвращается из Wait/Close как первая ошибка, если других нет.
var ErrHandlerTimeout = errors.New("shutdown: handler timed out")

// Option настраивает Manager.
type Option func(*Manager)

// WithHandlerTimeout задает верхнюю границу выполнения одного handler'а.
// d<=0 отключает ограничение (handler выполняется синхронно без timeout'а).
func WithHandlerTimeout(d time.Duration) Option {
	return func(m *Manager) { m.handlerTimeout = d }
}

// WithLogger задает logger для наблюдаемости shutdown'а. Используется для
// логирования ошибок best-effort cleanup'а на пути поздней регистрации
// (OnExit после начала shutdown'а), где ошибка не может быть возвращена вызвавшему
// и иначе была бы потеряна молча. nil → slog.Default().
func WithLogger(l *slog.Logger) Option {
	return func(m *Manager) {
		if l != nil {
			m.logger = l
		}
	}
}

// Manager собирает cleanup-handlers и выполняет их в LIFO при сигнале или Close().
type Manager struct {
	mu             sync.Mutex
	handlers       []Handler
	closing        bool // shutdown инициирован; OnExit после этого выполняет handler сразу
	handlerTimeout time.Duration
	closed         chan struct{}
	once           sync.Once
	err            error
	cancel         context.CancelFunc
	doneCtx        context.Context
	logger         *slog.Logger
}

// New создает Manager и стартует goroutine-listener для SIGINT/SIGTERM.
//
// Listener неблокирующий: первый сигнал инициирует shutdown (вызов Close());
// последующие сигналы игнорируются (если процесс «завис», admin делает kill -9).
func New(opts ...Option) *Manager {
	doneCtx, cancel := context.WithCancel(context.Background())
	m := &Manager{
		handlerTimeout: defaultHandlerTimeout,
		closed:         make(chan struct{}),
		cancel:         cancel,
		doneCtx:        doneCtx,
		logger:         slog.Default(),
	}
	for _, o := range opts {
		o(m)
	}
	go m.listenSignals()
	return m
}

// OnExit регистрирует один или несколько handlers. Они выполнятся в обратном
// порядке регистрации (LIFO) при shutdown.
//
// Если shutdown уже инициирован, поздно зарегистрированный handler выполняется
// немедленно (best-effort, под тем же handler-timeout'ом) — чтобы ресурс,
// открытый уже на фазе завершения, не остался без cleanup'а (без silent loss).
// Можно вызывать многократно из любой goroutine.
func (m *Manager) OnExit(handlers ...Handler) {
	m.mu.Lock()
	if m.closing {
		timeout := m.handlerTimeout
		logger := m.logger
		m.mu.Unlock()
		for _, h := range handlers {
			if err := runBounded(h, timeout); err != nil {
				// Ошибку вернуть некому (Close уже завершился), но терять её молча
				// нельзя — иначе неубранный ресурс на фазе shutdown'а невидим для
				// пост-мортема. Логируем best-effort.
				logger.Error("shutdown: late-registered handler failed",
					slog.String("err", err.Error()))
			}
		}
		return
	}
	m.handlers = append(m.handlers, handlers...)
	m.mu.Unlock()
}

// Wait блокируется до получения сигнала + завершения всех handlers, либо до
// отмены ctx. Возвращает первую ошибку handler-а или ctx.Err().
func (m *Manager) Wait(ctx context.Context) error {
	select {
	case <-m.closed:
		return m.err
	case <-ctx.Done():
		// Если внешний ctx отменен до сигнала — инициируем shutdown сами.
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
		m.closing = true
		hs := append([]Handler(nil), m.handlers...)
		timeout := m.handlerTimeout
		m.mu.Unlock()

		// LIFO: последний зарегистрированный — первый выполняется.
		var firstErr error
		for i := len(hs) - 1; i >= 0; i-- {
			if err := runBounded(hs[i], timeout); err != nil && firstErr == nil {
				firstErr = err
			}
		}
		m.err = firstErr
		m.cancel()
		close(m.closed)
	})
	return m.err
}

// runBounded выполняет handler с ограничением по времени. При превышении timeout'а
// handler бросается (его goroutine продолжит жить до завершения процесса — на фазе
// shutdown это приемлемо), возвращается ErrHandlerTimeout.
func runBounded(h Handler, timeout time.Duration) error {
	if timeout <= 0 {
		return h()
	}
	done := make(chan error, 1)
	go func() { done <- h() }()
	t := time.NewTimer(timeout)
	defer t.Stop()
	select {
	case err := <-done:
		return err
	case <-t.C:
		return ErrHandlerTimeout
	}
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
