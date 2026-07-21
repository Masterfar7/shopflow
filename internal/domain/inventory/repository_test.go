package inventory_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"shopflow/internal/domain/inventory"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type mockPgxTxForExec struct {
	pgx.Tx
	execErr error
}

func (m *mockPgxTxForExec) Exec(ctx context.Context, sql string, arguments ...any) (pgconn.CommandTag, error) {
	return pgconn.CommandTag{}, m.execErr
}

func TestPostgresRepository_CreateReservation_UniqueViolation23505(t *testing.T) {
	repo := inventory.NewPostgresRepository(nil)
	ctx := context.Background()

	res := &inventory.Reservation{
		ID:        uuid.New(),
		OrderID:   uuid.New(),
		Status:    inventory.ReservationStatusPending,
		ExpiresAt: time.Now().Add(15 * time.Minute),
	}

	// 1. PostgreSQL error 23505 (unique_violation) -> returns ErrDuplicateReservationID
	tx23505 := &mockPgxTxForExec{
		execErr: &pgconn.PgError{
			Code:    "23505",
			Message: "duplicate key value violates unique constraint \"stock_reservations_pkey\"",
		},
	}
	err := repo.CreateReservation(ctx, tx23505, res)
	require.Error(t, err)
	assert.ErrorIs(t, err, inventory.ErrDuplicateReservationID, "SQLSTATE 23505 must map to ErrDuplicateReservationID")

	// 2. Different error code (e.g. 40001 serialization_failure) -> returns wrapped generic error
	txOther := &mockPgxTxForExec{
		execErr: &pgconn.PgError{
			Code:    "40001",
			Message: "serialization failure",
		},
	}
	err = repo.CreateReservation(ctx, txOther, res)
	require.Error(t, err)
	assert.False(t, errors.Is(err, inventory.ErrDuplicateReservationID))
	assert.Contains(t, err.Error(), "inventory: create reservation")

	// 3. Success -> returns nil
	txSuccess := &mockPgxTxForExec{execErr: nil}
	err = repo.CreateReservation(ctx, txSuccess, res)
	assert.NoError(t, err)
}
