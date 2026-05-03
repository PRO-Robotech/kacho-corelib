package watch

import (
	"context"
	"sync"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

const (
	ringBufferSize    = 1024
	pollInterval      = 100 * time.Millisecond
	catchupMaxRows    = 10000
	liveChanCap       = 100
)

// SelectorMatcher — функция-фильтр событий для подписчика.
// Если selectors пусты, функция должна возвращать true (принимать всё).
type SelectorMatcher func(evt Event) bool

// Subscription представляет активную подписку на Watch Hub.
// События доступны через канал C.
type Subscription struct {
	id       uint64
	C        chan Event
	matchers []SelectorMatcher
	hub      *Hub
}

// Unsubscribe отписывается от Hub.
func (s *Subscription) Unsubscribe() {
	s.hub.unsubscribe(s)
}

// Hub — in-process Watch Hub.
// При старте запускает goroutine, которая слушает pg_notify и опрашивает
// таблицу resource_events, ведя ring buffer последних 1024 событий.
type Hub struct {
	pool        *pgxpool.Pool
	channelName string // "kacho_<svc>"

	mu        sync.RWMutex
	cursorRV  int64
	ring      [ringBufferSize]Event
	ringHead  int // индекс следующей записи (по модулю)
	ringCount int // сколько событий в буфере (до ringBufferSize)

	subs      map[uint64]*Subscription
	nextSubID uint64

	notifyCh chan struct{}
}

// NewHub создаёт Hub и запускает фоновые goroutine.
// ctx управляет временем жизни Hub.
func NewHub(ctx context.Context, pool *pgxpool.Pool, channelName string) *Hub {
	h := &Hub{
		pool:        pool,
		channelName: channelName,
		subs:        make(map[uint64]*Subscription),
		notifyCh:    make(chan struct{}, 1),
	}
	go h.listenLoop(ctx)
	go h.pumpLoop(ctx)
	return h
}

// Subscribe создаёт подписку на события Hub.
// fromRV — resource_version, с которого нужно начать (события с rv > fromRV).
// matchers — список фильтров (OR между ними). Пустой список = принять всё.
//
// Метод выполняет catch-up фазу:
//   - если fromRV >= cursorRV-1024 → replay из ring buffer
//   - иначе → SELECT из outbox (до catchupMaxRows)
//   - если в outbox нет событий и fromRV < min(outbox) → возвращает ErrGone
//
// После catch-up подписка переходит в live-режим.
func (h *Hub) Subscribe(ctx context.Context, fromRV int64, matchers ...SelectorMatcher) (*Subscription, error) {
	// Сначала собираем catch-up события, не держа мьютекс
	h.mu.RLock()
	cursorRV := h.cursorRV
	h.mu.RUnlock()

	var catchupEvents []Event
	var err error

	if fromRV >= cursorRV-int64(ringBufferSize) {
		// Catch-up из ring buffer
		catchupEvents = h.collectFromRing(fromRV)
	} else {
		// Catch-up из outbox
		catchupEvents, err = h.collectFromOutbox(ctx, fromRV)
		if err != nil {
			return nil, err
		}
	}

	// Фильтруем catch-up события
	filtered := make([]Event, 0, len(catchupEvents))
	for _, evt := range catchupEvents {
		if h.matchesMatchers(matchers, evt) {
			filtered = append(filtered, evt)
		}
	}

	// Создаём канал с достаточной ёмкостью для catch-up + живых событий
	chanCap := liveChanCap + len(filtered)
	sub := &Subscription{
		C:        make(chan Event, chanCap),
		matchers: matchers,
		hub:      h,
	}

	// Регистрируем подписчика и добавляем catch-up события атомарно
	h.mu.Lock()
	sub.id = h.nextSubID
	h.nextSubID++
	h.subs[sub.id] = sub

	// Отправляем catch-up события в канал (гарантированно помещаются, канал достаточного размера)
	for _, evt := range filtered {
		sub.C <- evt
	}
	h.mu.Unlock()

	return sub, nil
}

// collectFromRing возвращает события из ring buffer с resource_version > fromRV.
func (h *Hub) collectFromRing(fromRV int64) []Event {
	h.mu.RLock()
	defer h.mu.RUnlock()

	var events []Event
	count := h.ringCount
	head := h.ringHead
	for i := 0; i < count; i++ {
		idx := (head - count + i + ringBufferSize) % ringBufferSize
		evt := h.ring[idx]
		if evt.ResourceVersion > fromRV {
			events = append(events, evt)
		}
	}
	return events
}

// collectFromOutbox запрашивает события из resource_events при fromRV вне ring.
// Возвращает ErrGone если fromRV устарел (< min outbox) и событий нет.
func (h *Hub) collectFromOutbox(ctx context.Context, fromRV int64) ([]Event, error) {
	rows, err := h.pool.Query(ctx,
		`SELECT resource_version, event_type, resource_kind, resource_uid, data
		 FROM resource_events
		 WHERE resource_version > $1
		 ORDER BY resource_version ASC
		 LIMIT $2`,
		fromRV, catchupMaxRows,
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var events []Event
	for rows.Next() {
		var evt Event
		if scanErr := rows.Scan(
			&evt.ResourceVersion,
			&evt.EventType,
			&evt.ResourceKind,
			&evt.ResourceUID,
			&evt.Data,
		); scanErr != nil {
			return nil, scanErr
		}
		events = append(events, evt)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}

	if len(events) == 0 {
		// Проверяем, не устарел ли fromRV (outbox retention exceeded).
		// Возможные случаи:
		//   1. minRV > 0 && fromRV < minRV: outbox не пуст, но события с rv > fromRV удалены → Gone
		//   2. minRV = 0 (outbox пуст) && fromRV < cursorRV: retention удалил все события → Gone
		//   3. fromRV >= cursorRV: клиент актуален, нет новых событий → нормально

		var minRV int64
		if scanErr := h.pool.QueryRow(ctx,
			`SELECT COALESCE(min(resource_version), 0) FROM resource_events`,
		).Scan(&minRV); scanErr != nil {
			return nil, scanErr
		}

		h.mu.RLock()
		cursorRV := h.cursorRV
		h.mu.RUnlock()

		if minRV > 0 && fromRV < minRV {
			// outbox не пуст, но запрошенная версия слишком старая
			return nil, ErrGone
		}
		if minRV == 0 && cursorRV > 0 && fromRV < cursorRV {
			// outbox полностью опустошён retention, но Hub знает о более новых событиях
			return nil, ErrGone
		}
		// Нормальный случай: нет новых событий
		return nil, nil
	}

	return events, nil
}

// unsubscribe удаляет подписчика и закрывает его канал.
func (h *Hub) unsubscribe(sub *Subscription) {
	h.mu.Lock()
	defer h.mu.Unlock()
	if _, ok := h.subs[sub.id]; ok {
		delete(h.subs, sub.id)
		close(sub.C)
	}
}

// matchesMatchers проверяет, совпадает ли событие с фильтрами.
// При пустом списке матчеров — всегда true.
func (h *Hub) matchesMatchers(matchers []SelectorMatcher, evt Event) bool {
	if len(matchers) == 0 {
		return true
	}
	for _, m := range matchers {
		if m(evt) {
			return true
		}
	}
	return false
}

// listenLoop открывает соединение для LISTEN/NOTIFY и пишет в notifyCh при получении.
func (h *Hub) listenLoop(ctx context.Context) {
	conn, err := h.pool.Acquire(ctx)
	if err != nil {
		return
	}
	rawConn := conn.Hijack()
	defer rawConn.Close(ctx)

	if _, err := rawConn.Exec(ctx, "LISTEN "+pgx.Identifier{h.channelName}.Sanitize()); err != nil {
		return
	}

	for {
		notification, err := rawConn.WaitForNotification(ctx)
		if err != nil {
			if ctx.Err() != nil {
				return
			}
			time.Sleep(50 * time.Millisecond)
			continue
		}
		if notification != nil {
			select {
			case h.notifyCh <- struct{}{}:
			default:
				// Уже есть pending notify
			}
		}
	}
}

// pumpLoop читает новые события из outbox и рассылает подписчикам.
func (h *Hub) pumpLoop(ctx context.Context) {
	ticker := time.NewTicker(pollInterval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-h.notifyCh:
			h.pump(ctx)
		case <-ticker.C:
			h.pump(ctx)
		}
	}
}

// pump читает новые события из resource_events начиная с cursorRV+1 и рассылает подписчикам.
func (h *Hub) pump(ctx context.Context) {
	h.mu.RLock()
	cursorRV := h.cursorRV
	h.mu.RUnlock()

	rows, err := h.pool.Query(ctx,
		`SELECT resource_version, event_type, resource_kind, resource_uid, data
		 FROM resource_events
		 WHERE resource_version > $1
		 ORDER BY resource_version ASC
		 LIMIT 1000`,
		cursorRV,
	)
	if err != nil {
		return
	}
	defer rows.Close()

	var events []Event
	for rows.Next() {
		var evt Event
		if err := rows.Scan(
			&evt.ResourceVersion,
			&evt.EventType,
			&evt.ResourceKind,
			&evt.ResourceUID,
			&evt.Data,
		); err != nil {
			return
		}
		events = append(events, evt)
	}
	if err := rows.Err(); err != nil {
		return
	}

	if len(events) == 0 {
		return
	}

	h.mu.Lock()
	defer h.mu.Unlock()

	for _, evt := range events {
		// Добавляем в ring buffer
		h.ring[h.ringHead] = evt
		h.ringHead = (h.ringHead + 1) % ringBufferSize
		if h.ringCount < ringBufferSize {
			h.ringCount++
		}
		if evt.ResourceVersion > h.cursorRV {
			h.cursorRV = evt.ResourceVersion
		}

		// Fan-out на подписчиков
		for _, sub := range h.subs {
			if h.matchesMatchers(sub.matchers, evt) {
				select {
				case sub.C <- evt:
				default:
					// Канал подписчика переполнен — не блокируем Hub
				}
			}
		}
	}
}

// CursorRV возвращает текущую позицию курсора Hub (последняя доставленная resource_version).
func (h *Hub) CursorRV() int64 {
	h.mu.RLock()
	defer h.mu.RUnlock()
	return h.cursorRV
}
