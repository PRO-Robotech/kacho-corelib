// Copyright (c) PRO-Robotech
// SPDX-License-Identifier: BUSL-1.1

package authz

import (
	"fmt"
	"sync"
	"testing"
	"time"
)

// clock — управляемый источник времени для детерминированного теста refill.
type clock struct{ t time.Time }

func (c *clock) now() time.Time { return c.t }
func (c *clock) advance(d time.Duration) {
	c.t = c.t.Add(d)
}

// TestRateLimiter_Disabled — ratePerSec ≤ 0 → Allow всегда true (limiter off).
func TestRateLimiter_Disabled(t *testing.T) {
	for _, rate := range []float64{0, -1} {
		rl := newRateLimiter(rate)
		for i := 0; i < 1000; i++ {
			if !rl.Allow("usr_x") {
				t.Fatalf("ratePerSec=%v: Allow must always be true when disabled (denied at i=%d)", rate, i)
			}
		}
	}
}

// TestRateLimiter_BurstThenExhaust — свежий subject стартует с full burst
// (2×rate); после исчерпания burst без refill'а → deny.
func TestRateLimiter_BurstThenExhaust(t *testing.T) {
	clk := &clock{t: time.Date(2026, 7, 5, 0, 0, 0, 0, time.UTC)}
	rl := newRateLimiter(10) // burst = 20
	rl.now = clk.now

	allowed := 0
	for i := 0; i < 100; i++ {
		if rl.Allow("usr_x") {
			allowed++
		}
	}
	// Без продвижения времени refill не происходит → ровно burst разрешений.
	if allowed != 20 {
		t.Fatalf("expected exactly burst=20 allowed before refill, got %d", allowed)
	}
}

// TestRateLimiter_RefillOverTime — по истечении времени токены пополняются
// elapsed×rate; проверяем что после исчерпания и паузы снова можно.
func TestRateLimiter_RefillOverTime(t *testing.T) {
	clk := &clock{t: time.Date(2026, 7, 5, 0, 0, 0, 0, time.UTC)}
	rl := newRateLimiter(10) // rate=10/s, burst=20
	rl.now = clk.now

	// Исчерпываем burst.
	for i := 0; i < 20; i++ {
		if !rl.Allow("usr_x") {
			t.Fatalf("burst not fully consumed at i=%d", i)
		}
	}
	if rl.Allow("usr_x") {
		t.Fatalf("expected deny immediately after burst exhausted")
	}

	// Проходит 0.5s → refill = 0.5×10 = 5 токенов.
	clk.advance(500 * time.Millisecond)
	got := 0
	for i := 0; i < 10; i++ {
		if rl.Allow("usr_x") {
			got++
		}
	}
	if got != 5 {
		t.Fatalf("expected 5 tokens refilled after 0.5s, got %d", got)
	}
}

// TestRateLimiter_RefillCapsAtBurst — длительная пауза не даёт токенам
// превысить burst (иначе DoS-cap обходится).
func TestRateLimiter_RefillCapsAtBurst(t *testing.T) {
	clk := &clock{t: time.Date(2026, 7, 5, 0, 0, 0, 0, time.UTC)}
	rl := newRateLimiter(10) // burst = 20
	rl.now = clk.now

	// Один вызов создаёт bucket (tokens=20 → 19).
	rl.Allow("usr_x")
	// Огромная пауза: refill = 3600×10 = 36000, но cap = burst = 20.
	clk.advance(time.Hour)
	got := 0
	for i := 0; i < 100; i++ {
		if rl.Allow("usr_x") {
			got++
		}
	}
	if got != 20 {
		t.Fatalf("expected refill capped at burst=20, got %d", got)
	}
}

// TestRateLimiter_HardCapBoundsBuckets — buckets map имеет собственный жёсткий
// потолок (CWE-770): при churn'е из множества уникальных principal-id map НЕ
// растёт неограниченно даже без внешнего EvictInactive-sweep'а.
func TestRateLimiter_HardCapBoundsBuckets(t *testing.T) {
	clk := &clock{t: time.Date(2026, 7, 5, 0, 0, 0, 0, time.UTC)}
	rl := newRateLimiterWithLimit(10, 100) // cap = 100 bucket'ов
	rl.now = clk.now

	// 10_000 уникальных subject'ов — на порядки больше потолка. Без внутренней
	// границы map вырос бы до 10k (OOM-вектор).
	for i := 0; i < 10_000; i++ {
		rl.Allow(fmt.Sprintf("usr_%d", i))
		if len(rl.buckets) > 100 {
			t.Fatalf("buckets exceeded hard cap: got %d at i=%d", len(rl.buckets), i)
		}
	}
	if len(rl.buckets) == 0 {
		t.Fatalf("expected some live buckets, got 0")
	}
}

// TestRateLimiter_DefaultCapPresent — конструктор по умолчанию ставит непустой
// внутренний потолок (не 0 = unbounded).
func TestRateLimiter_DefaultCapPresent(t *testing.T) {
	rl := newRateLimiter(10)
	if rl.maxBuckets <= 0 {
		t.Fatalf("default rate limiter must carry a positive maxBuckets, got %d", rl.maxBuckets)
	}
}

// TestRateLimiter_EvictInactive — удаляет только bucket'ы старше maxAge и
// возвращает корректный removed-count.
func TestRateLimiter_EvictInactive(t *testing.T) {
	clk := &clock{t: time.Date(2026, 7, 5, 0, 0, 0, 0, time.UTC)}
	rl := newRateLimiter(10)
	rl.now = clk.now

	// Старый subject: lastSeen = t0.
	rl.Allow("usr_old")
	// Проходит 2 минуты.
	clk.advance(2 * time.Minute)
	// Свежий subject: lastSeen = t0+2m.
	rl.Allow("usr_fresh")

	// Evict всё, что старше 1 минуты → должен уйти только usr_old.
	removed := rl.EvictInactive(1 * time.Minute)
	if removed != 1 {
		t.Fatalf("expected 1 bucket evicted, got %d", removed)
	}
	if _, ok := rl.buckets["usr_old"]; ok {
		t.Fatalf("usr_old bucket must be evicted")
	}
	if _, ok := rl.buckets["usr_fresh"]; !ok {
		t.Fatalf("usr_fresh bucket must survive")
	}
}

// TestRateLimiter_Concurrent — data-race guard: rateLimiter documented Thread-safe,
// но до этого теста ни один кейс не гонял Allow/EvictInactive из нескольких
// goroutine (в отличие от Cache, у которого есть TestCache_Concurrent). Спавним N
// goroutine, бьющих Allow по пересекающимся И уникальным subject-id, пока ещё одна
// goroutine периодически зовёт EvictInactive — весь map/bucket-mutation-путь под
// rl.mu. Прогоняется под -race; падает (concurrent map write / detected race), если
// будущая оптимизация сузит или уберёт lock в Allow или eviction-sweep.
func TestRateLimiter_Concurrent(t *testing.T) {
	rl := newRateLimiter(1000) // положительный rate → Allow идёт по locked-пути
	const goroutines = 32
	const iterations = 500

	var wg sync.WaitGroup
	wg.Add(goroutines + 1)

	// Writer-heavy: Allow по overlapping (usr_shared_%2) и уникальным (usr_g%d_i%d)
	// subject'ам — вставка новых bucket'ов конкурирует с eviction-sweep'ом.
	for g := 0; g < goroutines; g++ {
		go func(g int) {
			defer wg.Done()
			for i := 0; i < iterations; i++ {
				if i%2 == 0 {
					rl.Allow(fmt.Sprintf("usr_shared_%d", i%2))
				} else {
					rl.Allow(fmt.Sprintf("usr_g%d_i%d", g, i))
				}
			}
		}(g)
	}

	// Конкурентный eviction-sweep на том же mutation-пути.
	go func() {
		defer wg.Done()
		for i := 0; i < iterations; i++ {
			rl.EvictInactive(time.Nanosecond)
		}
	}()

	wg.Wait()

	// Sanity: лимитер работоспособен после конкурентной нагрузки.
	if !rl.Allow("usr_after_load") {
		t.Fatalf("rate limiter must admit a fresh subject after concurrent load")
	}
}
