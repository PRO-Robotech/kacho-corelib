package authz

import (
	"context"
	"errors"
	"log/slog"
	"time"

	"github.com/jackc/pgx/v5"
)

// ListenInvalidator подключается к kacho_iam Postgres через dedicated pgx-conn
// (НЕ из пула — required для LISTEN; godzila §16) и слушает channel
// `kacho_iam_subjects`. На каждый NOTIFY → `cache.InvalidateBySubject(payload)`.
//
// Lifecycle:
//   - Run(ctx) — блокирующий loop, до cancel ctx.
//   - При conn drop → reconnect (exponential backoff 1s → 2s → 4s → 8s → 30s cap).
//   - После reconnect → conservative `cache.InvalidateAll()` (чтобы не пропустить
//     NOTIFY в окне disconnect'а).
type ListenInvalidator struct {
	// ConnString — pgx connection string на kacho_iam Postgres.
	// Пример: "postgres://kacho_iam_listener:pwd@host:5432/kacho_iam?sslmode=disable".
	ConnString string

	// Channel — обычно "kacho_iam_subjects".
	Channel string

	// Cache — кеш, на котором будем invalidate.
	Cache *Cache

	// Logger.
	Logger *slog.Logger

	// FullCacheClearInterval — periodic full-clear как defensive measure.
	// 0 = disabled. Default 60s через env `KACHO_<SVC>_AUTHZ__FULL_CACHE_CLEAR_INTERVAL=60s`.
	FullCacheClearInterval time.Duration
}

// Run блокирующий loop. Возвращается на ctx.Done() или fatal err.
func (li *ListenInvalidator) Run(ctx context.Context) error {
	if li.Channel == "" {
		li.Channel = "kacho_iam_subjects"
	}
	if li.Cache == nil {
		return errors.New("authz.ListenInvalidator: Cache is nil")
	}
	logger := li.Logger
	if logger == nil {
		logger = slog.Default()
	}
	logger = logger.With(slog.String("component", "authz_listen_invalidator"), slog.String("channel", li.Channel))

	// Periodic full-clear (defensive).
	var fullClearTicker *time.Ticker
	if li.FullCacheClearInterval > 0 {
		fullClearTicker = time.NewTicker(li.FullCacheClearInterval)
		defer fullClearTicker.Stop()
		go func() {
			for {
				select {
				case <-ctx.Done():
					return
				case <-fullClearTicker.C:
					li.Cache.InvalidateAll()
					logger.Info("authz_periodic_full_cache_clear")
				}
			}
		}()
	}

	backoff := 1 * time.Second
	const maxBackoff = 30 * time.Second

	for {
		err := li.runOnce(ctx, logger)
		if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
			return nil
		}
		if err != nil {
			logger.Warn("authz_listen_conn_drop", slog.String("err", err.Error()), slog.Duration("backoff", backoff))
			// Conservative — invalidate всё, чтобы не пропустить NOTIFY.
			li.Cache.InvalidateAll()
		}
		select {
		case <-ctx.Done():
			return nil
		case <-time.After(backoff):
		}
		backoff *= 2
		if backoff > maxBackoff {
			backoff = maxBackoff
		}
	}
}

func (li *ListenInvalidator) runOnce(ctx context.Context, logger *slog.Logger) error {
	conn, err := pgx.Connect(ctx, li.ConnString)
	if err != nil {
		return err
	}
	defer func() { _ = conn.Close(ctx) }()

	_, err = conn.Exec(ctx, "LISTEN "+li.Channel)
	if err != nil {
		return err
	}
	logger.Info("authz_listen_connected")

	for {
		notif, err := conn.WaitForNotification(ctx)
		if err != nil {
			return err
		}
		if notif == nil {
			continue
		}
		subjectID := notif.Payload
		if subjectID == "" {
			// Conservative — empty payload means "invalidate all".
			li.Cache.InvalidateAll()
			logger.Info("authz_invalidate_all_via_notify")
			continue
		}
		li.Cache.InvalidateBySubject(subjectID)
		logger.Debug("authz_invalidate_subject", slog.String("subject_id", subjectID))
	}
}
