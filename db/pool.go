package db

import (
	"context"

	"github.com/jackc/pgx/v5/pgxpool"
)

// NewPool создаёт pgxpool с установленным statement_timeout=30s.
func NewPool(ctx context.Context, dsn string) (*pgxpool.Pool, error) {
	cfg, err := pgxpool.ParseConfig(dsn)
	if err != nil {
		return nil, err
	}
	if cfg.ConnConfig.RuntimeParams == nil {
		cfg.ConnConfig.RuntimeParams = map[string]string{}
	}
	cfg.ConnConfig.RuntimeParams["statement_timeout"] = "30000"
	return pgxpool.NewWithConfig(ctx, cfg)
}
