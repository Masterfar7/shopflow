package order

import (
	"context"
	"encoding/json"
	"fmt"
	"math"
	"sync"
	"testing"
	"time"

	"shopflow/internal/domain/catalog"
	"shopflow/internal/domain/money"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// MockCatalogReader provides in-memory catalog data for tests.
type MockCatalogReader struct {
	products map[string]catalog.Product
}

func NewMockCatalogReader() *MockCatalogReader {
	return &MockCatalogReader{products: make(map[string]catalog.Product)}
}

func (m *MockCatalogReader) AddProduct(p catalog.Product) {
	m.products[p.SKU] = p
}

func (m *MockCatalogReader) GetProduct(ctx context.Context, id uuid.UUID) (*catalog.Product, error) {
	for _, p := range m.products {
		if p.ID == id {
			cp := p
			return &cp, nil
		}
	}
	return nil, catalog.ErrProductNotFound
}

func (m *MockCatalogReader) GetProductBySKU(ctx context.Context, sku string) (*catalog.Product, error) {
	p, ok := m.products[sku]
	if !ok {
		return nil, catalog.ErrProductNotFound
	}
	cp := p
	return &cp, nil
}

func (m *MockCatalogReader) GetProductsBySKUs(ctx context.Context, skus []string) ([]catalog.Product, error) {
	var result []catalog.Product
	for _, sku := range skus {
		if p, ok := m.products[sku]; ok {
			result = append(result, p)
		}
	}
	return result, nil
}

// MockRepository provides an in-memory implementation of Repository for unit tests.
type MockRepository struct {
	mu              sync.Mutex
	orders          map[uuid.UUID]Order
	orderItems      map[uuid.UUID][]OrderItem
	idempotencyKeys map[string]IdempotencyKeyRecord // key format: userID.String() + ":" + key
	outboxMessages  []map[string]any
}

func NewMockRepository() *MockRepository {
	return &MockRepository{
		orders:          make(map[uuid.UUID]Order),
		orderItems:      make(map[uuid.UUID][]OrderItem),
		idempotencyKeys: make(map[string]IdempotencyKeyRecord),
	}
}

func (m *MockRepository) LockIdempotencyKey(ctx context.Context, tx pgx.Tx, key string, userID uuid.UUID, reqHash string, ttl time.Duration) (*IdempotencyKeyRecord, bool, error) {
	m.mu.Lock()
	defer m.mu.Unlock()

	mapKey := userID.String() + ":" + key
	rec, exists := m.idempotencyKeys[mapKey]

	now := time.Now()
	if !exists || rec.ExpiresAt.Before(now) {
		newRec := IdempotencyKeyRecord{
			Key:         key,
			UserID:      userID,
			RequestHash: reqHash,
			Status:      IdempotencyStatusProcessing,
			CreatedAt:   now,
			ExpiresAt:   now.Add(ttl),
		}
		m.idempotencyKeys[mapKey] = newRec
		return &newRec, true, nil
	}

	if rec.RequestHash != reqHash {
		return nil, false, ErrIdempotencyConflict
	}

	if rec.Status == IdempotencyStatusProcessing {
		return &rec, false, ErrConcurrentProcessing
	}

	return &rec, false, nil
}

func (m *MockRepository) CompleteIdempotencyKey(ctx context.Context, tx pgx.Tx, key string, userID uuid.UUID, statusCode int, responseBody []byte, orderID uuid.UUID) error {
	m.mu.Lock()
	defer m.mu.Unlock()

	mapKey := userID.String() + ":" + key
	rec, exists := m.idempotencyKeys[mapKey]
	if !exists {
		return ErrOrderNotFound
	}
	rec.Status = IdempotencyStatusCompleted
	rec.ResponseCode = &statusCode
	rec.ResponseBody = responseBody
	rec.OrderID = &orderID
	m.idempotencyKeys[mapKey] = rec
	return nil
}

func (m *MockRepository) GetIdempotencyKey(ctx context.Context, key string, userID uuid.UUID) (*IdempotencyKeyRecord, error) {
	m.mu.Lock()
	defer m.mu.Unlock()

	mapKey := userID.String() + ":" + key
	rec, exists := m.idempotencyKeys[mapKey]
	if !exists {
		return nil, nil
	}
	cp := rec
	return &cp, nil
}

func (m *MockRepository) SaveIdempotencyKey(ctx context.Context, tx pgx.Tx, rec *IdempotencyKeyRecord) error {
	m.mu.Lock()
	defer m.mu.Unlock()

	mapKey := rec.UserID.String() + ":" + rec.Key
	m.idempotencyKeys[mapKey] = *rec
	return nil
}

func (m *MockRepository) UpdateIdempotencyKey(ctx context.Context, tx pgx.Tx, rec *IdempotencyKeyRecord) error {
	return m.SaveIdempotencyKey(ctx, tx, rec)
}

func (m *MockRepository) CreateOrder(ctx context.Context, tx pgx.Tx, o *Order) error {
	m.mu.Lock()
	defer m.mu.Unlock()

	if o.ID == uuid.Nil {
		o.ID = uuid.New()
	}
	o.Version = 1
	o.CreatedAt = time.Now()
	o.UpdatedAt = time.Now()
	m.orders[o.ID] = *o
	return nil
}

func (m *MockRepository) CreateOrderItems(ctx context.Context, tx pgx.Tx, items []OrderItem) error {
	m.mu.Lock()
	defer m.mu.Unlock()

	for _, it := range items {
		if it.ID == uuid.Nil {
			it.ID = uuid.New()
		}
		it.CreatedAt = time.Now()
		m.orderItems[it.OrderID] = append(m.orderItems[it.OrderID], it)
	}
	return nil
}

func (m *MockRepository) CreateOrderWithItemsAndOutbox(ctx context.Context, tx pgx.Tx, o *Order, outboxPayload any) error {
	if err := m.CreateOrder(ctx, tx, o); err != nil {
		return err
	}
	if err := m.CreateOrderItems(ctx, tx, o.Items); err != nil {
		return err
	}
	if outboxPayload != nil {
		if err := m.InsertOutboxMessage(ctx, tx, o.ID, "OrderCreated", outboxPayload); err != nil {
			return err
		}
	}
	return nil
}

func (m *MockRepository) GetOrderByID(ctx context.Context, id uuid.UUID) (*Order, error) {
	m.mu.Lock()
	defer m.mu.Unlock()

	o, exists := m.orders[id]
	if !exists {
		return nil, ErrOrderNotFound
	}
	cp := o
	return &cp, nil
}

func (m *MockRepository) GetOrderByUserAndIdempotencyKey(ctx context.Context, userID uuid.UUID, key string) (*Order, error) {
	m.mu.Lock()
	defer m.mu.Unlock()

	for _, o := range m.orders {
		if o.UserID == userID && o.IdempotencyKey == key {
			cp := o
			cp.Items = m.orderItems[o.ID]
			return &cp, nil
		}
	}
	return nil, ErrOrderNotFound
}

func (m *MockRepository) GetOrderItems(ctx context.Context, orderID uuid.UUID) ([]OrderItem, error) {
	m.mu.Lock()
	defer m.mu.Unlock()

	items := m.orderItems[orderID]
	res := make([]OrderItem, len(items))
	copy(res, items)
	return res, nil
}

func (m *MockRepository) ListOrders(ctx context.Context, params ListOrdersParams) ([]Order, string, bool, error) {
	m.mu.Lock()
	defer m.mu.Unlock()

	var result []Order
	for _, o := range m.orders {
		if o.UserID == params.UserID {
			if params.Status == nil || o.Status == *params.Status {
				cp := o
				cp.Items = m.orderItems[o.ID]
				result = append(result, cp)
			}
		}
	}
	return result, "", false, nil
}

func (m *MockRepository) UpdateOrderStatus(ctx context.Context, tx pgx.Tx, orderID uuid.UUID, fromStatus, toStatus OrderStatus, expectedVersion int64) (int64, error) {
	m.mu.Lock()
	defer m.mu.Unlock()

	o, exists := m.orders[orderID]
	if !exists {
		return 0, ErrOrderNotFound
	}
	if o.Status == toStatus {
		return o.Version, nil // Idempotent
	}
	if o.Status != fromStatus || o.Version != expectedVersion {
		if o.Status.IsTerminal() {
			return 0, ErrOrderTerminal
		}
		return 0, ErrOptimisticLockConflict
	}
	o.Status = toStatus
	o.Version++
	o.UpdatedAt = time.Now()
	m.orders[orderID] = o
	return o.Version, nil
}

func (m *MockRepository) InsertOutboxMessage(ctx context.Context, tx pgx.Tx, aggregateID uuid.UUID, eventType string, payload any) error {
	m.mu.Lock()
	defer m.mu.Unlock()

	m.outboxMessages = append(m.outboxMessages, map[string]any{
		"aggregate_id": aggregateID,
		"event_type":   eventType,
		"payload":      payload,
	})
	return nil
}

// Tests

func TestService_CreateOrder_Success(t *testing.T) {
	repo := NewMockRepository()
	cat := NewMockCatalogReader()
	cat.AddProduct(catalog.Product{
		ID:         uuid.New(),
		SKU:        "SKU-1",
		Title:      "Product One",
		PriceMinor: 2500,
		Currency:   "USD",
		IsActive:   true,
	})
	cat.AddProduct(catalog.Product{
		ID:         uuid.New(),
		SKU:        "SKU-2",
		Title:      "Product Two",
		PriceMinor: 1000,
		Currency:   "USD",
		IsActive:   true,
	})

	svc := NewService(repo, cat, nil, nil)
	userID := uuid.New()
	idemKey := "idem-test-1"

	req := CreateOrderRequest{
		Currency: "USD",
		Items: []OrderItemRequest{
			{SKU: "SKU-1", Quantity: 2},
			{SKU: "SKU-2", Quantity: 3},
		},
	}
	rawPayload, _ := json.Marshal(req)

	order, isCached, err := svc.CreateOrder(context.Background(), userID, idemKey, req, rawPayload)
	require.NoError(t, err)
	assert.False(t, isCached)
	require.NotNil(t, order)

	assert.Equal(t, userID, order.UserID)
	assert.Equal(t, StatusPending, order.Status)
	assert.Equal(t, "USD", order.Currency)
	assert.Equal(t, int64(8000), order.TotalAmountMinor) // 2x2500 + 3x1000 = 8000
	assert.Len(t, order.Items, 2)

	// Verify price snapshots
	assert.Equal(t, "SKU-1", order.Items[0].SKU)
	assert.Equal(t, "Product One", order.Items[0].TitleSnapshot)
	assert.Equal(t, int64(2500), order.Items[0].UnitPriceMinor)
	assert.Equal(t, int64(5000), order.Items[0].SubtotalMinor)

	assert.Equal(t, "SKU-2", order.Items[1].SKU)
	assert.Equal(t, "Product Two", order.Items[1].TitleSnapshot)
	assert.Equal(t, int64(1000), order.Items[1].UnitPriceMinor)
	assert.Equal(t, int64(3000), order.Items[1].SubtotalMinor)

	// Verify Outbox message created
	repo.mu.Lock()
	assert.Len(t, repo.outboxMessages, 1)
	assert.Equal(t, "OrderCreated", repo.outboxMessages[0]["event_type"])
	repo.mu.Unlock()
}

func TestService_CreateOrder_IdempotencyReplay(t *testing.T) {
	repo := NewMockRepository()
	cat := NewMockCatalogReader()
	cat.AddProduct(catalog.Product{
		ID:         uuid.New(),
		SKU:        "SKU-IDEM",
		Title:      "Idem Item",
		PriceMinor: 1200,
		Currency:   "USD",
		IsActive:   true,
	})

	svc := NewService(repo, cat, nil, nil)
	userID := uuid.New()
	idemKey := "idem-replay-key"

	req := CreateOrderRequest{
		Currency: "USD",
		Items: []OrderItemRequest{
			{SKU: "SKU-IDEM", Quantity: 2},
		},
	}
	rawPayload, _ := json.Marshal(req)

	// 1. Initial creation
	order1, isCached1, err := svc.CreateOrder(context.Background(), userID, idemKey, req, rawPayload)
	require.NoError(t, err)
	assert.False(t, isCached1)
	require.NotNil(t, order1)

	// 2. Exact duplicate replay
	order2, isCached2, err := svc.CreateOrder(context.Background(), userID, idemKey, req, rawPayload)
	require.NoError(t, err)
	assert.True(t, isCached2)
	require.NotNil(t, order2)
	assert.Equal(t, order1.ID, order2.ID)
	assert.Equal(t, order1.TotalAmountMinor, order2.TotalAmountMinor)

	// Assert no second order inserted
	repo.mu.Lock()
	assert.Len(t, repo.orders, 1)
	repo.mu.Unlock()
}

func TestService_CreateOrder_IdempotencyTamperedPayloadConflict(t *testing.T) {
	repo := NewMockRepository()
	cat := NewMockCatalogReader()
	cat.AddProduct(catalog.Product{
		ID:         uuid.New(),
		SKU:        "SKU-CONFLICT",
		Title:      "Conflict Item",
		PriceMinor: 1500,
		Currency:   "USD",
		IsActive:   true,
	})

	svc := NewService(repo, cat, nil, nil)
	userID := uuid.New()
	idemKey := "idem-conflict-key"

	req1 := CreateOrderRequest{
		Currency: "USD",
		Items: []OrderItemRequest{
			{SKU: "SKU-CONFLICT", Quantity: 1},
		},
	}
	raw1, _ := json.Marshal(req1)

	order1, isCached, err := svc.CreateOrder(context.Background(), userID, idemKey, req1, raw1)
	require.NoError(t, err)
	assert.False(t, isCached)
	require.NotNil(t, order1)

	// Replay with different quantity
	req2 := CreateOrderRequest{
		Currency: "USD",
		Items: []OrderItemRequest{
			{SKU: "SKU-CONFLICT", Quantity: 99},
		},
	}
	raw2, _ := json.Marshal(req2)

	_, _, err = svc.CreateOrder(context.Background(), userID, idemKey, req2, raw2)
	require.Error(t, err)
	assert.ErrorIs(t, err, ErrIdempotencyConflict)
}

func TestService_CreateOrder_MissingOrEmptyItems(t *testing.T) {
	repo := NewMockRepository()
	cat := NewMockCatalogReader()
	svc := NewService(repo, cat, nil, nil)

	_, _, err := svc.CreateOrder(context.Background(), uuid.New(), "key", CreateOrderRequest{Items: nil}, nil)
	assert.ErrorIs(t, err, ErrEmptyOrderItems)
}

func TestService_CreateOrder_MaxItemsExceeded(t *testing.T) {
	repo := NewMockRepository()
	cat := NewMockCatalogReader()
	svc := NewService(repo, cat, nil, nil)

	items := make([]OrderItemRequest, 101)
	for i := 0; i < 101; i++ {
		items[i] = OrderItemRequest{SKU: "SKU-X", Quantity: 1}
	}

	_, _, err := svc.CreateOrder(context.Background(), uuid.New(), "key", CreateOrderRequest{Items: items}, nil)
	assert.ErrorIs(t, err, ErrMaxItemsExceeded)
}

func TestService_CreateOrder_InvalidQuantity(t *testing.T) {
	repo := NewMockRepository()
	cat := NewMockCatalogReader()
	cat.AddProduct(catalog.Product{
		SKU:        "SKU-Q",
		PriceMinor: 100,
		IsActive:   true,
	})
	svc := NewService(repo, cat, nil, nil)

	req := CreateOrderRequest{
		Items: []OrderItemRequest{{SKU: "SKU-Q", Quantity: 0}},
	}
	_, _, err := svc.CreateOrder(context.Background(), uuid.New(), "key", req, nil)
	assert.ErrorIs(t, err, ErrInvalidQuantity)
}

func TestService_CreateOrder_CurrencyMismatch(t *testing.T) {
	repo := NewMockRepository()
	cat := NewMockCatalogReader()
	cat.AddProduct(catalog.Product{
		SKU:        "SKU-EUR",
		PriceMinor: 1000,
		Currency:   "EUR",
		IsActive:   true,
	})
	svc := NewService(repo, cat, nil, nil)

	req := CreateOrderRequest{
		Currency: "USD",
		Items:    []OrderItemRequest{{SKU: "SKU-EUR", Quantity: 1}},
	}
	_, _, err := svc.CreateOrder(context.Background(), uuid.New(), "key", req, nil)
	assert.ErrorIs(t, err, ErrCurrencyMismatch)
}

func TestService_CreateOrder_ProductNotFound(t *testing.T) {
	repo := NewMockRepository()
	cat := NewMockCatalogReader()
	svc := NewService(repo, cat, nil, nil)

	req := CreateOrderRequest{
		Currency: "USD",
		Items:    []OrderItemRequest{{SKU: "UNKNOWN-SKU", Quantity: 1}},
	}
	_, _, err := svc.CreateOrder(context.Background(), uuid.New(), "key", req, nil)
	assert.ErrorIs(t, err, ErrProductNotFound)
}

func TestService_CreateOrder_ProductInactive(t *testing.T) {
	repo := NewMockRepository()
	cat := NewMockCatalogReader()
	cat.AddProduct(catalog.Product{
		SKU:        "SKU-INACTIVE",
		PriceMinor: 1000,
		Currency:   "USD",
		IsActive:   false,
	})
	svc := NewService(repo, cat, nil, nil)

	req := CreateOrderRequest{
		Currency: "USD",
		Items:    []OrderItemRequest{{SKU: "SKU-INACTIVE", Quantity: 1}},
	}
	_, _, err := svc.CreateOrder(context.Background(), uuid.New(), "key", req, nil)
	assert.ErrorIs(t, err, ErrProductUnavailable)
}

func TestService_CreateOrder_ArithmeticOverflow(t *testing.T) {
	repo := NewMockRepository()
	cat := NewMockCatalogReader()
	cat.AddProduct(catalog.Product{
		SKU:        "SKU-OVERFLOW",
		PriceMinor: math.MaxInt64,
		Currency:   "USD",
		IsActive:   true,
	})
	svc := NewService(repo, cat, nil, nil)

	req := CreateOrderRequest{
		Currency: "USD",
		Items:    []OrderItemRequest{{SKU: "SKU-OVERFLOW", Quantity: 2}},
	}
	_, _, err := svc.CreateOrder(context.Background(), uuid.New(), "key", req, nil)
	assert.ErrorIs(t, err, ErrArithmeticOverflow)
}

func TestService_GetOrder(t *testing.T) {
	repo := NewMockRepository()
	cat := NewMockCatalogReader()
	cat.AddProduct(catalog.Product{
		SKU:        "SKU-GET",
		Title:      "Get Item",
		PriceMinor: 500,
		Currency:   "USD",
		IsActive:   true,
	})
	svc := NewService(repo, cat, nil, nil)
	user1 := uuid.New()
	user2 := uuid.New()

	order, _, err := svc.CreateOrder(context.Background(), user1, "key-get", CreateOrderRequest{
		Currency: "USD",
		Items:    []OrderItemRequest{{SKU: "SKU-GET", Quantity: 1}},
	}, nil)
	require.NoError(t, err)

	// Success
	fetched, err := svc.GetOrder(context.Background(), order.ID, user1)
	require.NoError(t, err)
	assert.Equal(t, order.ID, fetched.ID)
	assert.Len(t, fetched.Items, 1)

	// Forbidden for other user
	_, err = svc.GetOrder(context.Background(), order.ID, user2)
	assert.ErrorIs(t, err, ErrForbidden)

	// Not found
	_, err = svc.GetOrder(context.Background(), uuid.New(), user1)
	assert.ErrorIs(t, err, ErrOrderNotFound)

	// Unauthorized
	_, err = svc.GetOrder(context.Background(), order.ID, uuid.Nil)
	assert.ErrorIs(t, err, ErrUnauthorized)
}

func TestService_CancelOrder(t *testing.T) {
	repo := NewMockRepository()
	cat := NewMockCatalogReader()
	cat.AddProduct(catalog.Product{
		SKU:        "SKU-CANCEL",
		PriceMinor: 500,
		Currency:   "USD",
		IsActive:   true,
	})
	svc := NewService(repo, cat, nil, nil)
	user := uuid.New()

	order, _, err := svc.CreateOrder(context.Background(), user, "key-cancel", CreateOrderRequest{
		Currency: "USD",
		Items:    []OrderItemRequest{{SKU: "SKU-CANCEL", Quantity: 1}},
	}, nil)
	require.NoError(t, err)

	// Cancel order
	cancelled, err := svc.CancelOrder(context.Background(), order.ID, user, "cancel-key", "Mistake order")
	require.NoError(t, err)
	assert.Equal(t, StatusCancelled, cancelled.Status)

	// Idempotent cancel
	cancelled2, err := svc.CancelOrder(context.Background(), order.ID, user, "cancel-key", "Duplicate cancel")
	require.NoError(t, err)
	assert.Equal(t, StatusCancelled, cancelled2.Status)

	// Cannot cancel if confirmed (terminal state)
	confirmedOrder := &Order{
		ID:               uuid.New(),
		UserID:           user,
		Status:           StatusConfirmed,
		TotalAmountMinor: 1000,
		Currency:         "USD",
		Version:          1,
	}
	require.NoError(t, repo.CreateOrder(context.Background(), nil, confirmedOrder))

	_, err = svc.CancelOrder(context.Background(), confirmedOrder.ID, user, "k", "Cancel confirmed")
	assert.ErrorIs(t, err, ErrOrderTerminal)
}

func TestService_ConfirmOrder_And_Transitions(t *testing.T) {
	repo := NewMockRepository()
	cat := NewMockCatalogReader()
	svc := NewService(repo, cat, nil, nil)
	user := uuid.New()

	o := &Order{
		ID:               uuid.New(),
		UserID:           user,
		Status:           StatusPending,
		TotalAmountMinor: 1000,
		Currency:         "USD",
		Version:          1,
	}
	require.NoError(t, repo.CreateOrder(context.Background(), nil, o))

	// Cannot directly confirm from PENDING
	_, err := svc.ConfirmOrder(context.Background(), o.ID, 1)
	assert.ErrorIs(t, err, ErrInvalidStateTransition)

	// Step through proper state machine:
	// PENDING -> RESERVING_STOCK
	o, err = svc.TransitionStatus(context.Background(), o.ID, StatusPending, StatusReservingStock, 1)
	require.NoError(t, err)
	assert.Equal(t, StatusReservingStock, o.Status)
	assert.Equal(t, int64(2), o.Version)

	// RESERVING_STOCK -> STOCK_RESERVED
	o, err = svc.TransitionStatus(context.Background(), o.ID, StatusReservingStock, StatusStockReserved, 2)
	require.NoError(t, err)
	assert.Equal(t, StatusStockReserved, o.Status)
	assert.Equal(t, int64(3), o.Version)

	// STOCK_RESERVED -> PAYING
	o, err = svc.TransitionStatus(context.Background(), o.ID, StatusStockReserved, StatusPaying, 3)
	require.NoError(t, err)
	assert.Equal(t, StatusPaying, o.Status)
	assert.Equal(t, int64(4), o.Version)

	// PAYING -> PAID
	o, err = svc.TransitionStatus(context.Background(), o.ID, StatusPaying, StatusPaid, 4)
	require.NoError(t, err)
	assert.Equal(t, StatusPaid, o.Status)
	assert.Equal(t, int64(5), o.Version)

	// PAID -> CONFIRMED via ConfirmOrder
	o, err = svc.ConfirmOrder(context.Background(), o.ID, 5)
	require.NoError(t, err)
	assert.Equal(t, StatusConfirmed, o.Status)
	assert.Equal(t, int64(6), o.Version)

	// Idempotent ConfirmOrder
	o, err = svc.ConfirmOrder(context.Background(), o.ID, 6)
	require.NoError(t, err)
	assert.Equal(t, StatusConfirmed, o.Status)

	// Cannot transition out of terminal state CONFIRMED
	_, err = svc.TransitionStatus(context.Background(), o.ID, StatusConfirmed, StatusCancelled, 6)
	assert.ErrorIs(t, err, ErrInvalidStateTransition)
}

func TestService_PriceFromMoneyStruct(t *testing.T) {
	repo := NewMockRepository()
	cat := NewMockCatalogReader()
	cat.AddProduct(catalog.Product{
		SKU:        "SKU-MONEY",
		Title:      "Money Item",
		PriceMinor: 0, // Fallback to Price.Amount
		Price:      money.Money{Amount: 4200, Currency: "USD"},
		Currency:   "USD",
		IsActive:   true,
	})

	svc := NewService(repo, cat, nil, nil)
	userID := uuid.New()
	order, _, err := svc.CreateOrder(context.Background(), userID, "money-key", CreateOrderRequest{
		Currency: "USD",
		Items:    []OrderItemRequest{{SKU: "SKU-MONEY", Quantity: 1}},
	}, nil)
	require.NoError(t, err)
	assert.Equal(t, int64(4200), order.TotalAmountMinor)
	assert.Equal(t, int64(4200), order.Items[0].UnitPriceMinor)
}

func TestService_ConfirmOrder_And_CancelOrder_RichOutboxPayloads(t *testing.T) {
	repo := NewMockRepository()
	cat := NewMockCatalogReader()
	cat.AddProduct(catalog.Product{
		SKU:        "SKU-RICH-1",
		Title:      "Rich Item",
		PriceMinor: 1500,
		Currency:   "USD",
		IsActive:   true,
	})

	svc := NewService(repo, cat, nil, nil)
	userID := uuid.New()
	order, _, err := svc.CreateOrder(context.Background(), userID, "rich-key-1", CreateOrderRequest{
		Currency: "USD",
		Items:    []OrderItemRequest{{SKU: "SKU-RICH-1", Quantity: 2}},
	}, nil)
	require.NoError(t, err)
	assert.Equal(t, int64(3000), order.TotalAmountMinor)

	// Step to PAID
	o, err := svc.TransitionStatus(context.Background(), order.ID, StatusPending, StatusReservingStock, 1)
	require.NoError(t, err)
	o, err = svc.TransitionStatus(context.Background(), o.ID, StatusReservingStock, StatusStockReserved, 2)
	require.NoError(t, err)
	o, err = svc.TransitionStatus(context.Background(), o.ID, StatusStockReserved, StatusPaying, 3)
	require.NoError(t, err)
	o, err = svc.TransitionStatus(context.Background(), o.ID, StatusPaying, StatusPaid, 4)
	require.NoError(t, err)

	// Clear previous outbox messages to inspect ConfirmOrder specifically
	repo.mu.Lock()
	repo.outboxMessages = nil
	repo.mu.Unlock()

	// ConfirmOrder -> emits OrderConfirmed with rich payload
	confirmedOrder, err := svc.ConfirmOrder(context.Background(), o.ID, 5)
	require.NoError(t, err)
	assert.Equal(t, StatusConfirmed, confirmedOrder.Status)

	repo.mu.Lock()
	var confirmedOutbox map[string]any
	for _, om := range repo.outboxMessages {
		if om["event_type"] == "OrderConfirmed" {
			confirmedOutbox = om["payload"].(map[string]any)
			break
		}
	}
	repo.mu.Unlock()

	require.NotNil(t, confirmedOutbox, "OrderConfirmed outbox message must be emitted")
	assert.Equal(t, order.ID, confirmedOutbox["order_id"])
	assert.Equal(t, fmt.Sprintf("user-%s@shopflow.io", userID), confirmedOutbox["customer_email"])
	assert.Equal(t, int64(3000), confirmedOutbox["total_amount_minor"])
	assert.Equal(t, "USD", confirmedOutbox["currency"])
	lineItems, ok := confirmedOutbox["line_items"].([]map[string]any)
	require.True(t, ok)
	require.Len(t, lineItems, 1)
	assert.Equal(t, "SKU-RICH-1", lineItems[0]["sku"])
	assert.Equal(t, "Rich Item", lineItems[0]["title"])
	assert.Equal(t, int64(1500), lineItems[0]["unit_price_minor"])
	assert.Equal(t, 2, lineItems[0]["quantity"])
	assert.Equal(t, int64(3000), lineItems[0]["subtotal_minor"])

	// Test CancelOrder on a separate pending order
	order2, _, err := svc.CreateOrder(context.Background(), userID, "rich-key-2", CreateOrderRequest{
		Currency: "USD",
		Items:    []OrderItemRequest{{SKU: "SKU-RICH-1", Quantity: 1}},
	}, nil)
	require.NoError(t, err)

	repo.mu.Lock()
	repo.outboxMessages = nil
	repo.mu.Unlock()

	cancelledOrder, err := svc.CancelOrder(context.Background(), order2.ID, userID, "cancel-key-2", "Defective product")
	require.NoError(t, err)
	assert.Equal(t, StatusCancelled, cancelledOrder.Status)

	repo.mu.Lock()
	var cancelledOutbox map[string]any
	for _, om := range repo.outboxMessages {
		if om["event_type"] == "OrderCancelled" {
			cancelledOutbox = om["payload"].(map[string]any)
			break
		}
	}
	repo.mu.Unlock()

	require.NotNil(t, cancelledOutbox, "OrderCancelled outbox message must be emitted")
	assert.Equal(t, order2.ID, cancelledOutbox["order_id"])
	assert.Equal(t, fmt.Sprintf("user-%s@shopflow.io", userID), cancelledOutbox["customer_email"])
	assert.Equal(t, int64(1500), cancelledOutbox["total_amount_minor"])
	assert.Equal(t, "USD", cancelledOutbox["currency"])
	assert.Equal(t, "Defective product", cancelledOutbox["reason"])
	cancLineItems, ok := cancelledOutbox["line_items"].([]map[string]any)
	require.True(t, ok)
	require.Len(t, cancLineItems, 1)
	assert.Equal(t, "SKU-RICH-1", cancLineItems[0]["sku"])
}

