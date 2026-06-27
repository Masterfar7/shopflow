package database_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"shopflow/internal/platform/database"

	"github.com/jackc/pgx/v5"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// mockTx implements pgx.Tx for testing ExecuteTx transaction lifecycle.
type mockTx struct {
	pgx.Tx
	commitCalled   bool
	rollbackCalled bool
	commitErr      error
	rollbackErr    error
}

func (m *mockTx) Commit(ctx context.Context) error {
	m.commitCalled = true
	return m.commitErr
}

func (m *mockTx) Rollback(ctx context.Context) error {
	m.rollbackCalled = true
	return m.rollbackErr
}

type mockTxBeginner struct {
	tx       *mockTx
	beginErr error
}

func (m *mockTxBeginner) BeginTx(ctx context.Context, opts pgx.TxOptions) (pgx.Tx, error) {
	if m.beginErr != nil {
		return nil, m.beginErr
	}
	return m.tx, nil
}

func TestDefaultConfig(t *testing.T) {
	cfg := database.DefaultConfig("postgres://localhost:5432/shopflow")
	assert.Equal(t, "postgres://localhost:5432/shopflow", cfg.URL)
	assert.Equal(t, int32(25), cfg.MaxConns)
	assert.Equal(t, int32(5), cfg.MinConns)
	assert.Equal(t, 1*time.Hour, cfg.MaxConnLifetime)
	assert.Equal(t, 15*time.Minute, cfg.MaxConnIdleTime)
	assert.Equal(t, 1*time.Minute, cfg.HealthCheckTime)
}

func TestNewPool_InvalidURL(t *testing.T) {
	cfg := database.Config{URL: "invalid://bad url"}
	pool, err := database.NewPool(context.Background(), cfg)
	assert.Error(t, err)
	assert.Nil(t, pool)
}

func TestExecuteTx_CommitOnSuccess(t *testing.T) {
	tx := &mockTx{}
	beginner := &mockTxBeginner{tx: tx}

	err := database.ExecuteTx(context.Background(), beginner, pgx.TxOptions{}, func(currTx pgx.Tx) error {
		return nil
	})

	require.NoError(t, err)
	assert.True(t, tx.commitCalled)
	assert.False(t, tx.rollbackCalled)
}

func TestExecuteTx_RollbackOnError(t *testing.T) {
	tx := &mockTx{}
	beginner := &mockTxBeginner{tx: tx}
	expectedErr := errors.New("business error")

	err := database.ExecuteTx(context.Background(), beginner, pgx.TxOptions{}, func(currTx pgx.Tx) error {
		return expectedErr
	})

	require.ErrorIs(t, err, expectedErr)
	assert.False(t, tx.commitCalled)
	assert.True(t, tx.rollbackCalled)
}

func TestExecuteTx_RollbackOnPanic(t *testing.T) {
	tx := &mockTx{}
	beginner := &mockTxBeginner{tx: tx}

	assert.Panics(t, func() {
		_ = database.ExecuteTx(context.Background(), beginner, pgx.TxOptions{}, func(currTx pgx.Tx) error {
			panic("unexpected failure")
		})
	})

	assert.False(t, tx.commitCalled)
	assert.True(t, tx.rollbackCalled)
}

func TestExecuteTx_BeginError(t *testing.T) {
	expectedErr := errors.New("cannot begin tx")
	beginner := &mockTxBeginner{beginErr: expectedErr}

	err := database.ExecuteTx(context.Background(), beginner, pgx.TxOptions{}, func(currTx pgx.Tx) error {
		return nil
	})

	require.Error(t, err)
	assert.Contains(t, err.Error(), "database: begin tx")
}

func TestExecuteTx_CommitError(t *testing.T) {
	commitErr := errors.New("commit failed")
	tx := &mockTx{commitErr: commitErr}
	beginner := &mockTxBeginner{tx: tx}

	err := database.ExecuteTx(context.Background(), beginner, pgx.TxOptions{}, func(currTx pgx.Tx) error {
		return nil
	})

	require.Error(t, err)
	assert.Contains(t, err.Error(), "database: commit tx")
}
