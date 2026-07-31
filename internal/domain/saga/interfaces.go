package saga

import (
	"context"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
)

// DBTX is an abstract database interface satisfied by *pgxpool.Pool and pgx.Tx.
type DBTX interface {
	Exec(ctx context.Context, sql string, arguments ...any) (pgconn.CommandTag, error)
	Query(ctx context.Context, sql string, args ...any) (pgx.Rows, error)
	QueryRow(ctx context.Context, sql string, args ...any) pgx.Row
}

// ReservationItem represents a single SKU reservation request.
type ReservationItem struct {
	SKU      string `json:"sku"`
	Quantity int    `json:"quantity"`
}

// ReservationResult encapsulates the outcome of an inventory reservation.
type ReservationResult struct {
	ReservationID uuid.UUID
	Success       bool
	FailedSKUs    []string
	ErrorMessage  string
}

// InventoryCommander defines the synchronous/in-process commands the Saga Coordinator
// invokes on the Inventory bounded context.
type InventoryCommander interface {
	ReserveStock(ctx context.Context, orderID uuid.UUID, items []ReservationItem) (*ReservationResult, error)
	ReleaseStock(ctx context.Context, reservationID uuid.UUID, reason string) error
	CommitStock(ctx context.Context, reservationID uuid.UUID) error
}

// OrderCommander defines the commands the Saga Coordinator invokes on the Order bounded context.
type OrderCommander interface {
	UpdateStatus(ctx context.Context, orderID uuid.UUID, status string) error
	CancelOrder(ctx context.Context, orderID uuid.UUID, reason string) error
	ConfirmOrder(ctx context.Context, orderID uuid.UUID) error
}

// PaymentCommander defines the commands the Saga Coordinator invokes on the Payment bounded context.
type PaymentCommander interface {
	AuthorizeAndCapture(ctx context.Context, orderID uuid.UUID, customerID uuid.UUID, amountMinor int64, currency string, paymentToken string, idempotencyKey string) (paymentID uuid.UUID, err error)
	Refund(ctx context.Context, paymentID uuid.UUID, orderID uuid.UUID, amountMinor int64, reason string, idempotencyKey string) error
}

// OutboxRecorder is an optional hook allowing the coordinator to insert outbox events in local tx.
type OutboxRecorder interface {
	RecordEvent(ctx context.Context, tx DBTX, aggregateType string, aggregateID string, eventType string, payload any) error
}
