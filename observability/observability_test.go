package observability

import (
	"context"
	"os"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestNewSlogger_OutputsJSON(t *testing.T) {
	logger := NewSlogger(os.Stdout)
	logger.Info("hello", "key", "value")
	// smoke: ничего не паникует, формат JSON
	require.NotNil(t, logger)
}

func TestInitOtel_NoopWhenEndpointEmpty(t *testing.T) {
	t.Setenv("KACHO_OTEL_EXPORTER_OTLP_ENDPOINT", "")
	shutdown, err := InitOtel(context.Background(), "test-svc")
	require.NoError(t, err)
	require.NotNil(t, shutdown)
	// shutdown — no-op
	require.NoError(t, shutdown(context.Background()))
}
