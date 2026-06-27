// Copyright (c) PRO-Robotech
// SPDX-License-Identifier: BUSL-1.1

package backoff_test

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"

	"github.com/PRO-Robotech/kacho-corelib/backoff"
)

// Build() обязан учитывать настроенный InitialInterval без ручного Reset:
// первый интервал = InitialInterval, а не cenkalti-дефолт 500ms.
func TestExponential_RespectsInitialInterval_NoManualReset(t *testing.T) {
	bo := backoff.ExponentialBackoffBuilder().
		WithInitialInterval(20 * time.Millisecond).
		WithMultiplier(2.0).
		WithMaxInterval(1 * time.Second).
		WithMaxElapsedThreshold(10 * time.Second).
		WithRandomizationFactor(0). // детерминизм
		Build()

	first := bo.NextBackOff()
	assert.Equal(t, 20*time.Millisecond, first, "первый интервал = настроенный InitialInterval, не 500ms")
	second := bo.NextBackOff()
	assert.Equal(t, 40*time.Millisecond, second, "второй интервал = first * multiplier")
}

// После исчерпания MaxElapsedTime backoff возвращает Stop.
func TestExponential_StopAfterMaxElapsed(t *testing.T) {
	bo := backoff.ExponentialBackoffBuilder().
		WithInitialInterval(1 * time.Millisecond).
		WithMaxElapsedThreshold(2 * time.Millisecond).
		WithRandomizationFactor(0).
		Build()
	time.Sleep(8 * time.Millisecond)
	assert.Equal(t, backoff.Stop, bo.NextBackOff(), "после MaxElapsed → Stop")
}

// Multiplier < 1.0 (убывающий backoff) отвергается — остается валидный дефолт,
// интервалы не убывают.
func TestExponential_RejectsMultiplierBelowOne(t *testing.T) {
	bo := backoff.ExponentialBackoffBuilder().
		WithInitialInterval(10 * time.Millisecond).
		WithMultiplier(0.5). // невалидно — должно игнорироваться
		WithMaxInterval(10 * time.Second).
		WithMaxElapsedThreshold(10 * time.Second).
		WithRandomizationFactor(0).
		Build()
	first := bo.NextBackOff()
	second := bo.NextBackOff()
	assert.GreaterOrEqual(t, second, first, "backoff не должен убывать при невалидном multiplier")
}

// RandomizationFactor > 1 (риск отрицательных интервалов) отвергается/клампится —
// интервалы остаются неотрицательными.
func TestExponential_ClampsRandomizationFactorAboveOne(t *testing.T) {
	bo := backoff.ExponentialBackoffBuilder().
		WithInitialInterval(10 * time.Millisecond).
		WithRandomizationFactor(5.0). // невалидно
		WithMaxInterval(1 * time.Second).
		WithMaxElapsedThreshold(10 * time.Second).
		Build()
	for i := 0; i < 8; i++ {
		d := bo.NextBackOff()
		if d == backoff.Stop {
			break
		}
		assert.GreaterOrEqual(t, d, time.Duration(0), "интервал не должен быть отрицательным")
	}
}

// NewConstantBackOff отдает фиксированный интервал стабильно.
func TestConstantBackOff(t *testing.T) {
	bo := backoff.NewConstantBackOff(30 * time.Millisecond)
	for i := 0; i < 3; i++ {
		assert.Equal(t, 30*time.Millisecond, bo.NextBackOff())
	}
}
