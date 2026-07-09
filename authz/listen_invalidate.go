// Copyright (c) PRO-Robotech
// SPDX-License-Identifier: BUSL-1.1

package authz

import (
	"context"
	"errors"
	"log/slog"
	"time"

	"github.com/jackc/pgx/v5"
)

// ListenInvalidator подключается к kacho_iam Postgres через dedicated pgx-conn
// (НЕ из пула — dedicated conn required для LISTEN) и слушает channel
// `kacho_iam_subjects`. На каждый NOTIFY → `cache.InvalidateBySubject(payload)`
// + `ListObjects.InvalidateBySubject(payload)` (если service выставлен).
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

	// Cache — Check-cache, на котором будем invalidate (опционально).
	Cache *Cache

	// ListObjects — ListObjects-cache для list-filtering
	// (опционально). На NOTIFY вызывается InvalidateBySubject(payload).
	ListObjects *ListObjectsService

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
	if li.Cache == nil && li.ListObjects == nil {
		return errors.New("authz.ListenInvalidator: Cache and ListObjects are both nil")
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
					li.invalidateAll()
					logger.Info("authz_periodic_full_cache_clear")
				}
			}
		}()
	}

	return li.reconnectLoop(ctx, logger, li.runOnce, sleepFor)
}

const (
	initialReconnectBackoff = 1 * time.Second
	maxReconnectBackoff     = 30 * time.Second
)

// reconnectLoop drives session (one connect+serve cycle) with exponential
// reconnect backoff. session reports whether it established the LISTEN
// subscription (connected) before returning. A connected session that later
// dropped resets the backoff to initialReconnectBackoff so a long-lived healthy
// subscription reconnects promptly instead of at the ratcheted
// maxReconnectBackoff cap (otherwise a handful of transient drops over the
// process lifetime pin the backoff at the cap and every subsequent brief blip is
// served up to maxReconnectBackoff late with missed subject-revoke NOTIFYs).
//
// sleep is injected so tests observe the requested wait deterministically; it
// returns false if ctx ended during the wait.
func (li *ListenInvalidator) reconnectLoop(
	ctx context.Context,
	logger *slog.Logger,
	session func(context.Context, *slog.Logger) (bool, error),
	sleep func(context.Context, time.Duration) bool,
) error {
	backoff := initialReconnectBackoff
	for {
		connected, err := session(ctx, logger)
		if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
			return nil
		}
		if connected {
			// Healthy session served NOTIFYs then dropped — reset the cycle so the
			// next reconnect is prompt. Missed NOTIFYs during the disconnect window
			// are already covered by the conservative invalidateAll below plus the
			// periodic full-clear.
			backoff = initialReconnectBackoff
		}
		if err != nil {
			logger.Warn("authz_listen_conn_drop", slog.String("err", err.Error()), slog.Duration("backoff", backoff))
			// Conservative — invalidate все, чтобы не пропустить NOTIFY.
			li.invalidateAll()
		}
		if !sleep(ctx, backoff) {
			return nil
		}
		backoff *= 2
		if backoff > maxReconnectBackoff {
			backoff = maxReconnectBackoff
		}
	}
}

// sleepFor waits for d or until ctx is done. Returns false if ctx ended first.
func sleepFor(ctx context.Context, d time.Duration) bool {
	select {
	case <-ctx.Done():
		return false
	case <-time.After(d):
		return true
	}
}

// runOnce runs one connect+LISTEN+serve cycle. The bool reports whether the
// subscription was established (LISTEN succeeded) before the returned error — the
// reconnect loop uses it to reset the backoff after a healthy session.
func (li *ListenInvalidator) runOnce(ctx context.Context, logger *slog.Logger) (bool, error) {
	conn, err := pgx.Connect(ctx, li.ConnString)
	if err != nil {
		return false, err
	}
	defer func() { _ = conn.Close(ctx) }()

	_, err = conn.Exec(ctx, "LISTEN "+li.Channel)
	if err != nil {
		return false, err
	}
	logger.Info("authz_listen_connected")

	for {
		notif, err := conn.WaitForNotification(ctx)
		if err != nil {
			return true, err
		}
		if notif == nil {
			continue
		}
		subjectID := notif.Payload
		if subjectID == "" {
			// Conservative — empty payload means "invalidate all".
			li.invalidateAll()
			logger.Info("authz_invalidate_all_via_notify")
			continue
		}
		li.invalidateBySubject(subjectID)
		logger.Debug("authz_invalidate_subject", slog.String("subject_id", subjectID))
	}
}

// invalidateBySubject вызывает InvalidateBySubject у обоих кэшей (если они заданы).
func (li *ListenInvalidator) invalidateBySubject(subjectID string) {
	if li.Cache != nil {
		li.Cache.InvalidateBySubject(subjectID)
	}
	if li.ListObjects != nil {
		li.ListObjects.InvalidateBySubject(subjectID)
	}
}

// invalidateAll вызывает InvalidateAll у обоих кэшей (если они заданы).
func (li *ListenInvalidator) invalidateAll() {
	if li.Cache != nil {
		li.Cache.InvalidateAll()
	}
	if li.ListObjects != nil {
		li.ListObjects.InvalidateAll()
	}
}
