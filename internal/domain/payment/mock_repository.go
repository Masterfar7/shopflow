package payment

import (
	"context"
	"fmt"
	"sync"
	"time"

	"github.com/google/uuid"
)

// MockRepository provides a thread-safe in-memory implementation of Repository.
type MockRepository struct {
	mu           sync.RWMutex
	payments     map[uuid.UUID]*Payment
	idempotency  map[string]uuid.UUID // "userID:key" -> paymentID
	refunds      map[uuid.UUID][]*Refund
	failOnCreate error
}

// NewMockRepository initializes an empty MockRepository.
func NewMockRepository() *MockRepository {
	return &MockRepository{
		payments:    make(map[uuid.UUID]*Payment),
		idempotency: make(map[string]uuid.UUID),
		refunds:     make(map[uuid.UUID][]*Refund),
	}
}

// SetFailOnCreate injects an error into CreatePayment for error testing.
func (m *MockRepository) SetFailOnCreate(err error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.failOnCreate = err
}

// CreatePayment inserts a payment in memory.
func (m *MockRepository) CreatePayment(ctx context.Context, p *Payment) error {
	m.mu.Lock()
	defer m.mu.Unlock()

	if m.failOnCreate != nil {
		return m.failOnCreate
	}

	if err := p.Validate(); err != nil {
		return err
	}

	idemKey := fmt.Sprintf("%s:%s", p.UserID.String(), p.IdempotencyKey)
	if _, exists := m.idempotency[idemKey]; exists {
		return ErrIdempotencyConflict
	}

	cp := *p
	if cp.CreatedAt.IsZero() {
		cp.CreatedAt = time.Now().UTC()
	}
	if cp.UpdatedAt.IsZero() {
		cp.UpdatedAt = cp.CreatedAt
	}
	if cp.Provider == "" {
		cp.Provider = "SIMULATED"
	}

	m.payments[p.ID] = &cp
	m.idempotency[idemKey] = p.ID
	return nil
}

// GetPaymentByID retrieves a payment by UUID.
func (m *MockRepository) GetPaymentByID(ctx context.Context, id uuid.UUID) (*Payment, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()

	p, ok := m.payments[id]
	if !ok {
		return nil, ErrPaymentNotFound
	}
	cp := *p
	return &cp, nil
}

// GetPaymentByIdempotency retrieves a payment by user ID and idempotency key.
func (m *MockRepository) GetPaymentByIdempotency(ctx context.Context, userID uuid.UUID, key string) (*Payment, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()

	idemKey := fmt.Sprintf("%s:%s", userID.String(), key)
	paymentID, ok := m.idempotency[idemKey]
	if !ok {
		return nil, ErrPaymentNotFound
	}

	p := m.payments[paymentID]
	cp := *p
	return &cp, nil
}

// UpdatePaymentStatus transitions a payment status in memory.
func (m *MockRepository) UpdatePaymentStatus(ctx context.Context, id uuid.UUID, status PaymentStatus, errorCode, failureReason *string) error {
	m.mu.Lock()
	defer m.mu.Unlock()

	p, ok := m.payments[id]
	if !ok {
		return ErrPaymentNotFound
	}

	p.Status = status
	p.ErrorCode = errorCode
	p.FailureReason = failureReason
	p.UpdatedAt = time.Now().UTC()
	return nil
}

// TransitionPaymentStatus atomically transitions payment status in memory.
func (m *MockRepository) TransitionPaymentStatus(ctx context.Context, id uuid.UUID, fromStatus, toStatus PaymentStatus) error {
	m.mu.Lock()
	defer m.mu.Unlock()

	p, ok := m.payments[id]
	if !ok {
		return ErrPaymentNotFound
	}

	if p.Status != fromStatus {
		if p.Status == PaymentStatusRefunded {
			return ErrPaymentAlreadyRefunded
		}
		return ErrPaymentCannotBeRefunded
	}

	p.Status = toStatus
	p.UpdatedAt = time.Now().UTC()
	return nil
}

// CreateRefund inserts a refund in memory.
func (m *MockRepository) CreateRefund(ctx context.Context, refund *Refund) error {
	m.mu.Lock()
	defer m.mu.Unlock()

	cp := *refund
	if cp.ID == uuid.Nil {
		cp.ID = uuid.New()
	}
	if cp.CreatedAt.IsZero() {
		cp.CreatedAt = time.Now().UTC()
	}
	if cp.UpdatedAt.IsZero() {
		cp.UpdatedAt = cp.CreatedAt
	}
	if cp.Status == "" {
		cp.Status = RefundStatusSuccess
	}

	m.refunds[cp.PaymentID] = append(m.refunds[cp.PaymentID], &cp)
	return nil
}

// GetRefundsByPaymentID retrieves all refunds for a payment.
func (m *MockRepository) GetRefundsByPaymentID(ctx context.Context, paymentID uuid.UUID) ([]Refund, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()

	list, ok := m.refunds[paymentID]
	if !ok {
		return nil, nil
	}

	result := make([]Refund, len(list))
	for i, r := range list {
		result[i] = *r
	}
	return result, nil
}
