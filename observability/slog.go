package observability

import (
	"io"
	"log/slog"
)

// NewSlogger создаёт структурированный JSON-логгер, пишущий в w.
// Минимальный уровень — Info.
func NewSlogger(w io.Writer) *slog.Logger {
	return slog.New(slog.NewJSONHandler(w, &slog.HandlerOptions{Level: slog.LevelInfo}))
}
