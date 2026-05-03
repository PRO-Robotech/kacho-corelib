// Package backoff — exponential / constant / zero backoff helpers.
//
// Адаптировано из H-BF/corlib/pkg/backoff (см. README PRO-Robotech/corelib).
// Использует cenkalti/backoff/v4 как базовую реализацию.
package backoff

import (
	"time"

	"github.com/cenkalti/backoff/v4"
)

type (
	// Backoff — alias на backoff.BackOff.
	Backoff = backoff.BackOff

	// BackOffContext — alias на backoff.BackOffContext.
	BackOffContext = backoff.BackOffContext //nolint:revive
)

var (
	// Stop — sentinel-значение, сигнализирующее «больше не повторять».
	Stop = backoff.Stop

	// WithContext оборачивает Backoff в context-aware вариант.
	WithContext = backoff.WithContext
)

// NewConstantBackOff возвращает backoff с фиксированным интервалом d.
func NewConstantBackOff(d time.Duration) Backoff {
	return &backoff.ConstantBackOff{Interval: d}
}

// ExponentialBackoffBuilder создаёт builder для exponential backoff.
//
//	bo := backoff.ExponentialBackoffBuilder().
//	    WithInitialInterval(100 * time.Millisecond).
//	    WithMultiplier(2.0).
//	    WithMaxInterval(5 * time.Second).
//	    WithMaxElapsedThreshold(30 * time.Second).
//	    WithRandomizationFactor(0.2).
//	    Build()
func ExponentialBackoffBuilder() exponentialBackoffBuilder { //nolint:revive
	return exponentialBackoffBuilder{inner: backoff.NewExponentialBackOff()}
}

type exponentialBackoffBuilder struct {
	inner *backoff.ExponentialBackOff
}

// Build возвращает построенный exponential backoff.
func (eb exponentialBackoffBuilder) Build() Backoff {
	ret := new(backoff.ExponentialBackOff)
	*ret = *eb.inner
	return ret
}

// WithRandomizationFactor — фактор jitter (0..1).
func (eb exponentialBackoffBuilder) WithRandomizationFactor(d float64) exponentialBackoffBuilder {
	if d >= 0 {
		eb.inner.RandomizationFactor = d
	}
	return eb
}

// WithInitialInterval — стартовый интервал.
func (eb exponentialBackoffBuilder) WithInitialInterval(d time.Duration) exponentialBackoffBuilder {
	if d >= 0 {
		eb.inner.InitialInterval = d
	}
	return eb
}

// WithMultiplier — множитель экспоненты (>= 1.0).
func (eb exponentialBackoffBuilder) WithMultiplier(d float64) exponentialBackoffBuilder {
	if d >= 0 {
		eb.inner.Multiplier = d
	}
	return eb
}

// WithMaxInterval — максимальная длина интервала между попытками.
func (eb exponentialBackoffBuilder) WithMaxInterval(d time.Duration) exponentialBackoffBuilder {
	if d >= 0 {
		eb.inner.MaxInterval = d
	}
	return eb
}

// WithMaxElapsedThreshold — общий budget на retry. После — Stop.
func (eb exponentialBackoffBuilder) WithMaxElapsedThreshold(d time.Duration) exponentialBackoffBuilder {
	if d >= 0 {
		eb.inner.MaxElapsedTime = d
	}
	return eb
}
