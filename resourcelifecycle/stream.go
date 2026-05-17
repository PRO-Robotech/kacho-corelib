package resourcelifecycle

import (
	"context"
	"errors"
	"log/slog"
	"time"
)

// Event — lifecycle-событие ресурса.
type Event struct {
	EventID          string
	Kind             string
	ResourceID       string
	Op               string // Create | Delete | Move
	ProjectID        string
	ParentResourceID string
	OldProjectID     string
	CreatedAt        time.Time
}

// Source — порт чтения lifecycle-событий из per-service outbox.
type Source interface {
	// ReadFrom возвращает события с event_id > cursor, отфильтрованные по
	// kinds (empty = все), не более limit штук, отсортированные по event_id ASC.
	ReadFrom(ctx context.Context, kinds []string, cursor string, limit int) ([]Event, error)

	// WaitForChange блокируется до LISTEN-notify (peer pg_notify trigger на
	// INSERT в outbox) ИЛИ timeout. Возвращает nil на notify, err на conn-drop /
	// ctx.Done().
	WaitForChange(ctx context.Context, timeout time.Duration) error
}

// Sink — порт отправки события подписчику (consumer-у через gRPC server-stream).
type Sink interface {
	Send(Event) error
}

// StreamOptions — конфигурация цикла.
type StreamOptions struct {
	BatchLimit int           // максимум событий за одну Read'у (default 100)
	IdleSleep  time.Duration // максимальный sleep до повторной попытки read когда нет change-notify (default 1s)
	Logger     *slog.Logger
}

// Stream — основной server-loop для одного подписчика.
//
// Логика:
//  1. ReadFrom(cursor) → batch events.
//  2. Forward через Sink.Send. На каждый успех обновляет cursor.
//  3. Если batch пустой → WaitForChange(IdleSleep), затем repeat.
//  4. На context.Canceled / Sink.Send err / Source err → возвращает err.
//
// Восстановление cursor'а — забота consumer'а (kacho-iam передаёт
// `resume_from_event_id` в SubscribeRequest; peer передаёт его в эту функцию
// как initial cursor).
func Stream(
	ctx context.Context,
	src Source,
	sink Sink,
	kinds []string,
	cursor string,
	opts StreamOptions,
) error {
	if opts.BatchLimit <= 0 {
		opts.BatchLimit = 100
	}
	if opts.IdleSleep <= 0 {
		opts.IdleSleep = 1 * time.Second
	}
	logger := opts.Logger
	if logger == nil {
		logger = slog.Default()
	}
	logger = logger.With(slog.String("component", "resourcelifecycle_stream"))

	for {
		if err := ctx.Err(); err != nil {
			return err
		}
		events, err := src.ReadFrom(ctx, kinds, cursor, opts.BatchLimit)
		if err != nil {
			return err
		}
		if len(events) == 0 {
			if err := src.WaitForChange(ctx, opts.IdleSleep); err != nil {
				if errors.Is(err, context.Canceled) {
					return nil
				}
				// On wait error (timeout / conn drop) — loop and retry ReadFrom.
				continue
			}
			continue
		}
		for _, ev := range events {
			if err := sink.Send(ev); err != nil {
				return err
			}
			cursor = ev.EventID
		}
		logger.Debug("resourcelifecycle_batch_sent",
			slog.Int("count", len(events)),
			slog.String("last_event_id", cursor))
	}
}
