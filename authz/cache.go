// Copyright (c) PRO-Robotech
// SPDX-License-Identifier: BUSL-1.1

package authz

import (
	"sync"
	"time"
)

// Cache хранит positive Check-results c TTL = 5s.
//
// Семантика:
//   - Кешируются ТОЛЬКО `allowed=true` (positive results).
//   - Negative (deny) НЕ кешируются — иначе grant binding'а не проявится до
//     истечения TTL → расходится с UX «дал права — почему не работает?».
//   - На revoke binding'а → `pg_notify('kacho_iam_subjects', subject_id)` →
//     `Cache.InvalidateBySubject(subject_id)` (см. listen_invalidate.go).
//
// Thread-safe: используется из нескольких gRPC-handler goroutines одновременно.
type Cache struct {
	mu  sync.RWMutex
	ttl time.Duration

	// store: ключ = subjectID, значение = map[entryKey]entry.
	// Двухуровневый dict позволяет O(1) invalidateBySubject(subjectID) —
	// просто `delete(c.store, subjectID)`.
	store map[string]map[entryKey]entry

	// count — точное текущее число entry во всех subject-bucket'ах. Поддерживается
	// инкрементально на insert/delete, чтобы решение об эвикции по потолку было O(1).
	count int

	// maxEntries — жёсткий потолок числа entry (CWE-770 защита от unbounded roста).
	maxEntries int

	// now — функция текущего времени, переопределяема в тестах.
	now func() time.Time
}

// entryKey — composite-ключ (relation, object_type, object_id).
// subjectID — внешний уровень map.
type entryKey struct {
	relation   string
	objectType string
	objectID   string
}

// entry — кешируемое значение. Кешируются только positive-результаты (negative
// не кешируется, см. package-doc), поэтому «разрешено» — структурный инвариант:
// сам факт живой entry означает allowed=true. Отдельного allowed-поля нет —
// иначе SetDenied-путь мог бы записать allowed=false и молча вернуть negative-
// кеширование, запрещённое контрактом пакета.
type entry struct {
	expiresAt time.Time // unix-time истечения
}

// defaultMaxEntries — верхняя граница числа кешируемых entry по умолчанию
// (защита от неограниченного роста map при enumeration-нагрузке, CWE-770).
// Один entry ≈ несколько десятков байт → 100k ≈ единицы МБ, потолок памяти жёсткий.
const defaultMaxEntries = 100_000

// NewCache создает кеш с указанным TTL. ttl ≤ 0 → defaults to 5*time.Second.
// Число entry ограничено defaultMaxEntries (см. NewCacheWithLimit).
func NewCache(ttl time.Duration) *Cache {
	return NewCacheWithLimit(ttl, defaultMaxEntries)
}

// NewCacheWithLimit создает кеш с указанным TTL и жёстким потолком числа entry.
// ttl ≤ 0 → 5s; maxEntries ≤ 0 → defaultMaxEntries. При достижении потолка insert
// нового ключа сперва вычищает просроченные записи, а если и после этого кеш полон —
// эвиктит произвольные entry до low-water (см. evictLocked). Cache-miss всегда
// безопасен (fallback на авторитетный Check), поэтому произвольная эвикция не влияет
// на корректность авторизации — только на hit-rate.
func NewCacheWithLimit(ttl time.Duration, maxEntries int) *Cache {
	if ttl <= 0 {
		ttl = 5 * time.Second
	}
	if maxEntries <= 0 {
		maxEntries = defaultMaxEntries
	}
	return &Cache{
		ttl:        ttl,
		store:      make(map[string]map[entryKey]entry, 64),
		now:        time.Now,
		maxEntries: maxEntries,
	}
}

// Get возвращает (true, true) если есть валидная positive-запись.
// Возвращает (false, false) в остальных случаях (miss / expired).
//
// На expiry — синхронно удаляет stale-entry (lazy eviction).
func (c *Cache) Get(subjectID, relation, objectType, objectID string) (allowed bool, ok bool) {
	c.mu.RLock()
	subMap, exists := c.store[subjectID]
	if !exists {
		c.mu.RUnlock()
		return false, false
	}
	e, exists := subMap[entryKey{relation, objectType, objectID}]
	c.mu.RUnlock()

	if !exists {
		return false, false
	}
	if c.now().After(e.expiresAt) {
		// Lazy delete — guarded против clobber конкурентно записанного свежего
		// entry (см. evictIfStale).
		c.evictIfStale(subjectID, entryKey{relation, objectType, objectID}, e.expiresAt)
		return false, false
	}
	// Живая entry ⇒ positive-результат (negative не кешируется).
	return true, true
}

// evictIfStale удаляет entry (subjectID, key) под write lock, но ТОЛЬКО если
// сохранённый expiresAt всё ещё равен observedExpiresAt — тому stale-значению,
// которое Get наблюдал под RLock перед тем, как отпустить его.
//
// Зачем: между RUnlock и Lock в Get конкурентный SetAllowed мог записать свежий
// entry (новый expiresAt в будущем). Безусловный delete выкинул бы этот валидный
// positive-результат (потеря → лишний Check round-trip в kacho-iam). Сравнение
// expiresAt гарантирует, что мы удаляем именно ту stale-запись, а не свежую.
func (c *Cache) evictIfStale(subjectID string, key entryKey, observedExpiresAt time.Time) {
	c.mu.Lock()
	defer c.mu.Unlock()
	subMap, ok := c.store[subjectID]
	if !ok {
		return
	}
	cur, ok := subMap[key]
	if !ok {
		return
	}
	if !cur.expiresAt.Equal(observedExpiresAt) {
		// Свежий entry записан конкурентно — не трогаем.
		return
	}
	delete(subMap, key)
	c.count--
	if len(subMap) == 0 {
		delete(c.store, subjectID)
	}
}

// evictLocked приводит размер кеша под потолок. Вызывается под write lock ПЕРЕД
// вставкой нового ключа, когда count достиг maxEntries. Фаза 1 — удалить все
// просроченные entry (дешевле и точнее). Фаза 2 (если и после этого полно) —
// удалить произвольные entry до low-water (maxEntries*7/8), чтобы вставка нового
// ключа гарантированно осталась под потолком. Произвольная эвикция безопасна:
// cache-miss всегда откатывается на авторитетный Check → корректность не страдает.
func (c *Cache) evictLocked() {
	now := c.now()
	for sid, sm := range c.store {
		for k, e := range sm {
			if now.After(e.expiresAt) {
				delete(sm, k)
				c.count--
			}
		}
		if len(sm) == 0 {
			delete(c.store, sid)
		}
	}
	if c.count < c.maxEntries {
		return
	}
	// Всё ещё полно — эвиктим произвольные entry до low-water.
	target := c.maxEntries - c.maxEntries/8
	if target < 0 {
		target = 0
	}
	for sid, sm := range c.store {
		for k := range sm {
			if c.count <= target {
				break
			}
			delete(sm, k)
			c.count--
		}
		if len(sm) == 0 {
			delete(c.store, sid)
		}
		if c.count <= target {
			break
		}
	}
}

// SetAllowed — кеширует positive result (TTL).
//
// Set negative — не делается; если allowed=false, вызывающий не должен
// звать SetAllowed.
func (c *Cache) SetAllowed(subjectID, relation, objectType, objectID string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	key := entryKey{relation, objectType, objectID}
	// Проверяем, новый ли это ключ (эвикция по потолку нужна только для новых —
	// перезапись существующего размер не меняет).
	if sm, ok := c.store[subjectID]; ok {
		if _, keyExists := sm[key]; keyExists {
			sm[key] = entry{expiresAt: c.now().Add(c.ttl)}
			return
		}
	}
	// Новый ключ. Держим потолок ДО вставки (evictLocked может удалить subject-bucket).
	if c.count >= c.maxEntries {
		c.evictLocked()
	}
	subMap, exists := c.store[subjectID]
	if !exists {
		subMap = make(map[entryKey]entry, 8)
		c.store[subjectID] = subMap
	}
	subMap[key] = entry{expiresAt: c.now().Add(c.ttl)}
	c.count++
}

// InvalidateBySubject удаляет ВСЕ записи для subjectID.
//
// Вызывается:
//   - из listen_invalidate.go при NOTIFY `kacho_iam_subjects` (push-invalidate).
//   - может вызываться вручную (например в тесте).
//
// Idempotent.
func (c *Cache) InvalidateBySubject(subjectID string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if sm, ok := c.store[subjectID]; ok {
		c.count -= len(sm)
		delete(c.store, subjectID)
	}
}

// InvalidateAll удаляет весь кеш. Используется:
//   - в periodic full-cache-clear (см. KACHO_<SVC>_AUTHZ__FULL_CACHE_CLEAR_INTERVAL).
//   - в LISTEN-loop reconnect (conservative — иначе риск пропустить NOTIFY
//     во время disconnect).
func (c *Cache) InvalidateAll() {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.store = make(map[string]map[entryKey]entry, 64)
	c.count = 0
}

// Size возвращает (subjectsCount, entriesCount). Используется в метриках.
func (c *Cache) Size() (subjects int, entries int) {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return len(c.store), c.count
}

// SetNowFunc — для тестов: подмена time.Now.
func (c *Cache) SetNowFunc(now func() time.Time) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.now = now
}
