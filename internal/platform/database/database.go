// internal/platform/database/database.go
package database

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
)

// Config defines connection pool parameters.
type Config struct {
	URL             string
	MaxConns        int32
	MinConns        int32
	MaxConnLifetime time.Duration
	MaxConnIdleTime time.Duration
	HealthCheckTime time.Duration
}

// DefaultConfig returns production-ready defaults.
func DefaultConfig(url string) Config {
	return Config{
		URL:             url,
		MaxConns:        25,
		MinConns:        5,
		MaxConnLifetime: 1 * time.Hour,
		MaxConnIdleTime: 15 * time.Minute,
		HealthCheckTime: 1 * time.Minute,
	}
}

// DBTX is a common interface satisfied by *pgxpool.Pool and pgx.Tx.
type DBTX interface {
	Exec(ctx context.Context, sql string, arguments ...any) (pgconn.CommandTag, error)
	Query(ctx context.Context, sql string, args ...any) (pgx.Rows, error)
	QueryRow(ctx context.Context, sql string, args ...any) pgx.Row
}

// TxBeginner is an interface for starting transactions, satisfied by *pgxpool.Pool.
type TxBeginner interface {
	BeginTx(ctx context.Context, txOptions pgx.TxOptions) (pgx.Tx, error)
}

// NewPool initializes and validates a pgxpool.Pool.
func NewPool(ctx context.Context, cfg Config) (*pgxpool.Pool, error) {
	pgxCfg, err := pgxpool.ParseConfig(cfg.URL)
	if err != nil {
		return nil, fmt.Errorf("database: parse config: %w", err)
	}

	if cfg.MaxConns > 0 {
		pgxCfg.MaxConns = cfg.MaxConns
	}
	if cfg.MinConns > 0 {
		pgxCfg.MinConns = cfg.MinConns
	}
	if cfg.MaxConnLifetime > 0 {
		pgxCfg.MaxConnLifetime = cfg.MaxConnLifetime
	}
	if cfg.MaxConnIdleTime > 0 {
		pgxCfg.MaxConnIdleTime = cfg.MaxConnIdleTime
	}
	if cfg.HealthCheckTime > 0 {
		pgxCfg.HealthCheckPeriod = cfg.HealthCheckTime
	}

	pool, err := pgxpool.NewWithConfig(ctx, pgxCfg)
	if err != nil {
		return nil, fmt.Errorf("database: create pool: %w", err)
	}

	// Immediate connectivity check
	if err := pool.Ping(ctx); err != nil {
		pool.Close()
		return nil, fmt.Errorf("database: ping on startup: %w", err)
	}

	return pool, nil
}

// Ping verifies database connectivity with a timeout context.
func Ping(ctx context.Context, pool *pgxpool.Pool) error {
	pingCtx, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()
	return pool.Ping(pingCtx)
}

// WithTx executes the provided function within a pgx database transaction.
// It commits on success, rolls back on error or panic, and propagates context.
func WithTx(ctx context.Context, pool *pgxpool.Pool, fn func(tx pgx.Tx) error) error {
	return WithTxOptions(ctx, pool, pgx.TxOptions{}, fn)
}

// WithTxOptions executes fn within a transaction with explicit pgx.TxOptions.
func WithTxOptions(ctx context.Context, pool *pgxpool.Pool, opts pgx.TxOptions, fn func(tx pgx.Tx) error) error {
	return ExecuteTx(ctx, pool, opts, fn)
}

// ExecuteTx executes fn within a transaction started by any TxBeginner.
func ExecuteTx(ctx context.Context, beginner TxBeginner, opts pgx.TxOptions, fn func(tx pgx.Tx) error) error {
	tx, err := beginner.BeginTx(ctx, opts)
	if err != nil {
		return fmt.Errorf("database: begin tx: %w", err)
	}

	panicked := true
	defer func() {
		if panicked {
			_ = tx.Rollback(ctx)
		}
	}()

	if err := fn(tx); err != nil {
		panicked = false
		if rbErr := tx.Rollback(ctx); rbErr != nil && !errors.Is(rbErr, pgx.ErrTxClosed) {
			return fmt.Errorf("database: tx error (%v), rollback error: %w", err, rbErr)
		}
		return err
	}

	panicked = false
	if err := tx.Commit(ctx); err != nil {
		return fmt.Errorf("database: commit tx: %w", err)
	}

	return nil
}
