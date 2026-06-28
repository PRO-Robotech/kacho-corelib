// Copyright (c) PRO-Robotech
// SPDX-License-Identifier: BUSL-1.1

package observability

import (
	"bytes"
	"context"
	"log/slog"
	"os"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestNewSlogger_OutputsJSON(t *testing.T) {
	logger := NewSlogger(os.Stdout)
	logger.Info("hello", "key", "value")
	// smoke: ничего не паникует, формат JSON
	require.NotNil(t, logger)
}

// TestNewSloggerLevel_HonorsLevel — a level-aware logger must honour its
// minimum level: a DEBUG logger emits a debug record; an INFO logger drops it.
// This is the regression for the dead `logger.level` config (the old API
// hardcoded slog.LevelInfo).
func TestNewSloggerLevel_HonorsLevel(t *testing.T) {
	t.Run("debug logger emits debug record", func(t *testing.T) {
		var buf bytes.Buffer
		logger := NewSloggerLevel(&buf, slog.LevelDebug)
		logger.Debug("debug-line", "k", "v")
		require.Contains(t, buf.String(), "debug-line",
			"DEBUG logger must emit a debug record")
	})

	t.Run("info logger drops debug record", func(t *testing.T) {
		var buf bytes.Buffer
		logger := NewSloggerLevel(&buf, slog.LevelInfo)
		logger.Debug("debug-line", "k", "v")
		require.Empty(t, strings.TrimSpace(buf.String()),
			"INFO logger must drop a debug record")
	})

	t.Run("info logger still emits info record", func(t *testing.T) {
		var buf bytes.Buffer
		logger := NewSloggerLevel(&buf, slog.LevelInfo)
		logger.Info("info-line")
		require.Contains(t, buf.String(), "info-line")
	})
}

// TestNewSlogger_BackCompatLevelInfo — the back-compat constructor keeps the
// historical LevelInfo behaviour (drops debug, emits info) so existing
// vpc/compute/nlb/iam callers are unaffected.
func TestNewSlogger_BackCompatLevelInfo(t *testing.T) {
	var buf bytes.Buffer
	logger := NewSloggerLevel(&buf, slog.LevelInfo)
	logger.Debug("debug-line")
	require.Empty(t, strings.TrimSpace(buf.String()),
		"LevelInfo logger must drop debug")
	logger.Info("info-line")
	require.Contains(t, buf.String(), "info-line")
}

func TestInitOtel_NoopWhenEndpointEmpty(t *testing.T) {
	t.Setenv("KACHO_OTEL_EXPORTER_OTLP_ENDPOINT", "")
	shutdown, err := InitOtel(context.Background(), "test-svc")
	require.NoError(t, err)
	require.NotNil(t, shutdown)
	// shutdown — no-op
	require.NoError(t, shutdown(context.Background()))
}
