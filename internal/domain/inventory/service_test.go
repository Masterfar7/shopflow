package inventory_test

import (
	"context"
	"errors"
	"sort"
	"sync"
	"testing"
	"time"

	"shopflow/internal/domain/inventory"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// memoryTx implements pgx.Tx with rollback support and lock releasing.
type memoryTx struct {
	pgx.Tx
	repo        *memoryInventoryRepo
	lockedSKUs  []string
	rollbackFns []func()
	committed   bool
	mu          sync.Mutex
}

func (tx *memoryTx) Commit(ctx context.Context) error {
	tx.mu.Lock()
	defer tx.mu.Unlock()
	tx.committed = true
	tx.unlockAll()
	return nil
}

func (tx *memoryTx) Rollback(ctx context.Context) error {
	tx.mu.Lock()
	defer tx.mu.Unlock()
	if tx.committed {
		return nil
	}
	for i := len(tx.rollbackFns) - 1; i >= 0; i-- {
		tx.rollbackFns[i]()
	}
	tx.unlockAll()
	return nil
}

func (tx *memoryTx) unlockAll() {
	for _, sku := range tx.lockedSKUs {
		tx.repo.unlockSKU(sku)
	}
	tx.lockedSKUs = nil
}

// memoryTxBeginner provides transaction context for the service.
type memoryTxBeginner struct {
	repo *memoryInventoryRepo
}

func (b *memoryTxBeginner) BeginTx(ctx context.Context, opts pgx.TxOptions) (pgx.Tx, error) {
	return &memoryTx{
		repo: b.repo,
	}, nil
}

// memoryInventoryRepo simulates Postgres inventory storage with full invariant checking.
type memoryInventoryRepo struct {
	mu                  sync.RWMutex
	items               map[string]*inventory.Item
	reservations        map[uuid.UUID]*inventory.Reservation
	reservationsByOrder map[uuid.UUID]uuid.UUID
	reservationItems    map[uuid.UUID][]inventory.ReservationItem

	// Row locking simulation
	lockMu       sync.Mutex
	skuLocks     map[string]*sync.Mutex
	lockOrderLog [][]string
}

func newMemoryInventoryRepo() *memoryInventoryRepo {
	return &memoryInventoryRepo{
		items:               make(map[string]*inventory.Item),
		reservations:        make(map[uuid.UUID]*inventory.Reservation),
		reservationsByOrder: make(map[uuid.UUID]uuid.UUID),
		reservationItems:    make(map[uuid.UUID][]inventory.ReservationItem),
		skuLocks:            make(map[string]*sync.Mutex),
	}
}

func (m *memoryInventoryRepo) getSKUMutex(sku string) *sync.Mutex {
	m.lockMu.Lock()
	defer m.lockMu.Unlock()
	l, exists := m.skuLocks[sku]
	if !exists {
		l = &sync.Mutex{}
		m.skuLocks[sku] = l
	}
	return l
}

func (m *memoryInventoryRepo) unlockSKU(sku string) {
	l := m.getSKUMutex(sku)
	l.Unlock()
}

func (m *memoryInventoryRepo) GetStock(ctx context.Context, sku string) (*inventory.Item, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()

	item, exists := m.items[sku]
	if !exists {
		return nil, inventory.ErrSKUNotFound
	}
	cp := *item
	cp.Available = cp.OnHand - cp.Reserved
	return &cp, nil
}

func (m *memoryInventoryRepo) GetStockForSKUs(ctx context.Context, skus []string) ([]inventory.Item, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()

	var result []inventory.Item
	for _, sku := range skus {
		if item, exists := m.items[sku]; exists {
			cp := *item
			cp.Available = cp.OnHand - cp.Reserved
			result = append(result, cp)
		}
	}
	sort.Slice(result, func(i, j int) bool { return result[i].SKU < result[j].SKU })
	return result, nil
}

func (m *memoryInventoryRepo) UpsertStock(ctx context.Context, sku string, quantity int) (*inventory.Item, error) {
	m.mu.Lock()
	defer m.mu.Unlock()

	item, exists := m.items[sku]
	if !exists {
		item = &inventory.Item{
			SKU:       sku,
			OnHand:    quantity,
			Reserved:  0,
			Version:   1,
			UpdatedAt: time.Now(),
		}
		m.items[sku] = item
	} else {
		item.OnHand += quantity
		item.Version++
		item.UpdatedAt = time.Now()
	}

	cp := *item
	cp.Available = cp.OnHand - cp.Reserved
	return &cp, nil
}

func (m *memoryInventoryRepo) LockSKUsForUpdate(ctx context.Context, tx pgx.Tx, sortedSKUs []string) (map[string]inventory.Item, error) {
	if len(sortedSKUs) == 0 {
		return make(map[string]inventory.Item), nil
	}

	// Invariant: SKU slice must be strictly sorted ascending
	if !sort.StringsAreSorted(sortedSKUs) {
		return nil, inventory.ErrDeadlockAvoidanceViolation
	}

	// Record lock order for deterministic locking verification
	m.lockMu.Lock()
	cpOrder := make([]string, len(sortedSKUs))
	copy(cpOrder, sortedSKUs)
	m.lockOrderLog = append(m.lockOrderLog, cpOrder)
	m.lockMu.Unlock()

	// Acquire row-level mutexes in sorted ascending order
	memTx, ok := tx.(*memoryTx)
	if ok {
		for _, sku := range sortedSKUs {
			l := m.getSKUMutex(sku)
			l.Lock()
			memTx.lockedSKUs = append(memTx.lockedSKUs, sku)
		}
	}

	m.mu.RLock()
	defer m.mu.RUnlock()

	result := make(map[string]inventory.Item, len(sortedSKUs))
	for _, sku := range sortedSKUs {
		if item, exists := m.items[sku]; exists {
			cp := *item
			cp.Available = cp.OnHand - cp.Reserved
			result[sku] = cp
		}
	}
	return result, nil
}

func (m *memoryInventoryRepo) IncrementReserved(ctx context.Context, tx pgx.Tx, sku string, qty int) error {
	m.mu.Lock()
	defer m.mu.Unlock()

	item, exists := m.items[sku]
	if !exists {
		return inventory.ErrSKUNotFound
	}

	if item.Reserved+qty > item.OnHand {
		return inventory.ErrStockConservationViolation
	}

	item.Reserved += qty
	item.Version++
	item.UpdatedAt = time.Now()

	if memTx, ok := tx.(*memoryTx); ok {
		memTx.rollbackFns = append(memTx.rollbackFns, func() {
			m.mu.Lock()
			defer m.mu.Unlock()
			item.Reserved -= qty
		})
	}
	return nil
}

func (m *memoryInventoryRepo) DecrementReserved(ctx context.Context, tx pgx.Tx, sku string, qty int) error {
	m.mu.Lock()
	defer m.mu.Unlock()

	item, exists := m.items[sku]
	if !exists {
		return inventory.ErrSKUNotFound
	}

	if item.Reserved-qty < 0 {
		return inventory.ErrStockConservationViolation
	}

	item.Reserved -= qty
	item.Version++
	item.UpdatedAt = time.Now()

	if memTx, ok := tx.(*memoryTx); ok {
		memTx.rollbackFns = append(memTx.rollbackFns, func() {
			m.mu.Lock()
			defer m.mu.Unlock()
			item.Reserved += qty
		})
	}
	return nil
}

func (m *memoryInventoryRepo) CommitStockOnHand(ctx context.Context, tx pgx.Tx, sku string, qty int) error {
	m.mu.Lock()
	defer m.mu.Unlock()

	item, exists := m.items[sku]
	if !exists {
		return inventory.ErrSKUNotFound
	}

	if item.OnHand-qty < 0 || item.Reserved-qty < 0 {
		return inventory.ErrStockConservationViolation
	}

	item.OnHand -= qty
	item.Reserved -= qty
	item.Version++
	item.UpdatedAt = time.Now()

	if memTx, ok := tx.(*memoryTx); ok {
		memTx.rollbackFns = append(memTx.rollbackFns, func() {
			m.mu.Lock()
			defer m.mu.Unlock()
			item.OnHand += qty
			item.Reserved += qty
		})
	}
	return nil
}

func (m *memoryInventoryRepo) CreateReservation(ctx context.Context, tx pgx.Tx, res *inventory.Reservation) error {
	m.mu.Lock()
	defer m.mu.Unlock()

	if _, exists := m.reservations[res.ID]; exists {
		return inventory.ErrDuplicateReservationID
	}
	if _, exists := m.reservationsByOrder[res.OrderID]; exists {
		return inventory.ErrDuplicateReservationID
	}

	cp := *res
	cp.CreatedAt = time.Now()
	cp.UpdatedAt = time.Now()

	m.reservations[res.ID] = &cp
	m.reservationsByOrder[res.OrderID] = res.ID

	if memTx, ok := tx.(*memoryTx); ok {
		memTx.rollbackFns = append(memTx.rollbackFns, func() {
			m.mu.Lock()
			defer m.mu.Unlock()
			delete(m.reservations, res.ID)
			delete(m.reservationsByOrder, res.OrderID)
		})
	}
	return nil
}

func (m *memoryInventoryRepo) CreateReservationItems(ctx context.Context, tx pgx.Tx, resID uuid.UUID, items []inventory.StockItemRequest) error {
	m.mu.Lock()
	defer m.mu.Unlock()

	var resItems []inventory.ReservationItem
	for _, item := range items {
		resItems = append(resItems, inventory.ReservationItem{
			ID:            uuid.New(),
			ReservationID: resID,
			SKU:           item.SKU,
			Quantity:      item.Quantity,
			CreatedAt:     time.Now(),
		})
	}
	m.reservationItems[resID] = resItems

	if memTx, ok := tx.(*memoryTx); ok {
		memTx.rollbackFns = append(memTx.rollbackFns, func() {
			m.mu.Lock()
			defer m.mu.Unlock()
			delete(m.reservationItems, resID)
		})
	}
	return nil
}

func (m *memoryInventoryRepo) GetReservationByID(ctx context.Context, id uuid.UUID) (*inventory.Reservation, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()

	res, exists := m.reservations[id]
	if !exists {
		return nil, inventory.ErrReservationNotFound
	}
	cp := *res
	return &cp, nil
}

func (m *memoryInventoryRepo) GetReservationByOrderID(ctx context.Context, orderID uuid.UUID) (*inventory.Reservation, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()

	resID, exists := m.reservationsByOrder[orderID]
	if !exists {
		return nil, inventory.ErrReservationNotFound
	}
	res, exists := m.reservations[resID]
	if !exists {
		return nil, inventory.ErrReservationNotFound
	}
	cp := *res
	return &cp, nil
}

func (m *memoryInventoryRepo) LockReservationForUpdate(ctx context.Context, tx pgx.Tx, id uuid.UUID) (*inventory.Reservation, error) {
	return m.GetReservationByID(ctx, id)
}

func (m *memoryInventoryRepo) LockReservationByOrderForUpdate(ctx context.Context, tx pgx.Tx, orderID uuid.UUID) (*inventory.Reservation, error) {
	return m.GetReservationByOrderID(ctx, orderID)
}

func (m *memoryInventoryRepo) GetReservationItems(ctx context.Context, resID uuid.UUID) ([]inventory.ReservationItem, error) {
	return m.GetReservationItemsTx(ctx, nil, resID)
}

func (m *memoryInventoryRepo) GetReservationItemsTx(ctx context.Context, tx pgx.Tx, resID uuid.UUID) ([]inventory.ReservationItem, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()

	items, exists := m.reservationItems[resID]
	if !exists {
		return []inventory.ReservationItem{}, nil
	}
	cp := make([]inventory.ReservationItem, len(items))
	copy(cp, items)
	return cp, nil
}

func (m *memoryInventoryRepo) UpdateReservationStatus(ctx context.Context, tx pgx.Tx, id uuid.UUID, status inventory.ReservationStatus) error {
	m.mu.Lock()
	defer m.mu.Unlock()

	res, exists := m.reservations[id]
	if !exists {
		return inventory.ErrReservationNotFound
	}
	oldStatus := res.Status
	res.Status = status
	res.UpdatedAt = time.Now()

	if memTx, ok := tx.(*memoryTx); ok {
		memTx.rollbackFns = append(memTx.rollbackFns, func() {
			m.mu.Lock()
			defer m.mu.Unlock()
			res.Status = oldStatus
		})
	}
	return nil
}

func setupInventoryService() (*inventory.Service, *memoryInventoryRepo) {
	repo := newMemoryInventoryRepo()
	beginner := &memoryTxBeginner{repo: repo}
	service := inventory.NewService(repo, beginner, nil)
	return service, repo
}

// ---------------------- Service Tests ----------------------

func TestGetStock_Success(t *testing.T) {
	svc, repo := setupInventoryService()
	ctx := context.Background()

	_, err := repo.UpsertStock(ctx, "SKU-GET-1", 50)
	require.NoError(t, err)

	item, err := svc.GetStock(ctx, "SKU-GET-1")
	require.NoError(t, err)
	assert.Equal(t, "SKU-GET-1", item.SKU)
	assert.Equal(t, 50, item.OnHand)
	assert.Equal(t, 0, item.Reserved)
	assert.Equal(t, 50, item.Available)
}

func TestGetStock_NotFound(t *testing.T) {
	svc, _ := setupInventoryService()
	ctx := context.Background()

	_, err := svc.GetStock(ctx, "SKU-NONEXISTENT")
	require.ErrorIs(t, err, inventory.ErrSKUNotFound)
}

func TestGetStock_EmptySKU(t *testing.T) {
	svc, _ := setupInventoryService()
	ctx := context.Background()

	_, err := svc.GetStock(ctx, "   ")
	require.ErrorIs(t, err, inventory.ErrEmptySKU)
}

func TestReplenishStock_NewSKU(t *testing.T) {
	svc, _ := setupInventoryService()
	ctx := context.Background()

	item, err := svc.ReplenishStock(ctx, "SKU-REPL-1", 100, "PO-1001")
	require.NoError(t, err)
	assert.Equal(t, "SKU-REPL-1", item.SKU)
	assert.Equal(t, 100, item.OnHand)
	assert.Equal(t, 0, item.Reserved)
	assert.Equal(t, 100, item.Available)
}

func TestReplenishStock_ExistingSKU(t *testing.T) {
	svc, _ := setupInventoryService()
	ctx := context.Background()

	_, err := svc.ReplenishStock(ctx, "SKU-REPL-2", 40, "PO-1")
	require.NoError(t, err)

	item, err := svc.ReplenishStock(ctx, "SKU-REPL-2", 60, "PO-2")
	require.NoError(t, err)
	assert.Equal(t, 100, item.OnHand)
	assert.Equal(t, 0, item.Reserved)
	assert.Equal(t, 100, item.Available)
	assert.Equal(t, int64(2), item.Version)
}

func TestReplenishStock_InvalidQty(t *testing.T) {
	svc, _ := setupInventoryService()
	ctx := context.Background()

	_, err := svc.ReplenishStock(ctx, "SKU-INV-QTY", 0, "PO-0")
	require.ErrorIs(t, err, inventory.ErrInvalidQuantity)

	_, err = svc.ReplenishStock(ctx, "SKU-INV-QTY", -10, "PO-NEG")
	require.ErrorIs(t, err, inventory.ErrInvalidQuantity)
}

func TestReplenishStock_EmptySKU(t *testing.T) {
	svc, _ := setupInventoryService()
	ctx := context.Background()

	_, err := svc.ReplenishStock(ctx, "   ", 10, "PO-EMPTY")
	require.ErrorIs(t, err, inventory.ErrEmptySKU)
}

func TestReserveStock_SingleSKU_Success(t *testing.T) {
	svc, repo := setupInventoryService()
	ctx := context.Background()

	_, err := repo.UpsertStock(ctx, "SKU-RES-1", 20)
	require.NoError(t, err)

	orderID := uuid.New()
	resID := uuid.New()

	result, err := svc.ReserveStock(ctx, inventory.ReserveStockRequest{
		ReservationID: resID,
		OrderID:       orderID,
		Items: []inventory.StockItemRequest{
			{SKU: "SKU-RES-1", Quantity: 5},
		},
		TTLSeconds: 600,
	})
	require.NoError(t, err)
	require.NotNil(t, result)
	assert.Equal(t, resID, result.ReservationID)
	assert.Equal(t, orderID, result.OrderID)
	assert.Equal(t, inventory.ReservationStatusPending, result.Status)

	// Verify inventory state
	item, err := repo.GetStock(ctx, "SKU-RES-1")
	require.NoError(t, err)
	assert.Equal(t, 20, item.OnHand)
	assert.Equal(t, 5, item.Reserved)
	assert.Equal(t, 15, item.Available)
}

func TestReserveStock_MultiSKU_AscendingOrder(t *testing.T) {
	svc, repo := setupInventoryService()
	ctx := context.Background()

	// Seed in arbitrary order
	_, _ = repo.UpsertStock(ctx, "SKU-Z", 10)
	_, _ = repo.UpsertStock(ctx, "SKU-A", 10)
	_, _ = repo.UpsertStock(ctx, "SKU-M", 10)

	orderID := uuid.New()

	// Submit in reverse alphabetical order: Z, M, A
	result, err := svc.ReserveStock(ctx, inventory.ReserveStockRequest{
		OrderID: orderID,
		Items: []inventory.StockItemRequest{
			{SKU: "SKU-Z", Quantity: 2},
			{SKU: "SKU-M", Quantity: 3},
			{SKU: "SKU-A", Quantity: 1},
		},
	})
	require.NoError(t, err)
	require.NotNil(t, result)

	// Verify that the repository recorded locking in strictly ascending order: SKU-A, SKU-M, SKU-Z
	repo.lockMu.Lock()
	require.NotEmpty(t, repo.lockOrderLog)
	lastLog := repo.lockOrderLog[len(repo.lockOrderLog)-1]
	repo.lockMu.Unlock()

	require.Equal(t, []string{"SKU-A", "SKU-M", "SKU-Z"}, lastLog, "Locks must be acquired in ascending lexicographical order")
}

func TestReserveStock_DuplicateSKU_Consolidation(t *testing.T) {
	svc, repo := setupInventoryService()
	ctx := context.Background()

	_, _ = repo.UpsertStock(ctx, "SKU-CONSOLIDATE", 20)

	orderID := uuid.New()

	// Request same SKU twice: 3 + 4 = 7 units
	result, err := svc.ReserveStock(ctx, inventory.ReserveStockRequest{
		OrderID: orderID,
		Items: []inventory.StockItemRequest{
			{SKU: "SKU-CONSOLIDATE", Quantity: 3},
			{SKU: "SKU-CONSOLIDATE", Quantity: 4},
		},
	})
	require.NoError(t, err)
	require.NotNil(t, result)
	require.Len(t, result.Items, 1)
	assert.Equal(t, 7, result.Items[0].Quantity)

	// Check stock
	item, err := repo.GetStock(ctx, "SKU-CONSOLIDATE")
	require.NoError(t, err)
	assert.Equal(t, 20, item.OnHand)
	assert.Equal(t, 7, item.Reserved)
	assert.Equal(t, 13, item.Available)
}

func TestReserveStock_InsufficientStock_AbortsAll(t *testing.T) {
	svc, repo := setupInventoryService()
	ctx := context.Background()

	// SKU-A has 10 units; SKU-B has 2 units
	_, _ = repo.UpsertStock(ctx, "SKU-AVAIL", 10)
	_, _ = repo.UpsertStock(ctx, "SKU-DEPLETED", 2)

	orderID := uuid.New()

	// Request 5 of SKU-AVAIL and 5 of SKU-DEPLETED (which only has 2)
	result, err := svc.ReserveStock(ctx, inventory.ReserveStockRequest{
		OrderID: orderID,
		Items: []inventory.StockItemRequest{
			{SKU: "SKU-AVAIL", Quantity: 5},
			{SKU: "SKU-DEPLETED", Quantity: 5},
		},
	})
	require.Error(t, err)
	assert.Nil(t, result)

	var insufficientErr *inventory.ErrInsufficientStock
	require.True(t, errors.As(err, &insufficientErr), "expected ErrInsufficientStock")
	assert.Contains(t, insufficientErr.FailedSKUs, "SKU-DEPLETED")
	assert.Equal(t, 5, insufficientErr.Details["SKU-DEPLETED"].Requested)
	assert.Equal(t, 2, insufficientErr.Details["SKU-DEPLETED"].Available)

	// Invariant check: ZERO partial reservations allowed! SKU-AVAIL must still have 0 reserved.
	itemAvail, err := repo.GetStock(ctx, "SKU-AVAIL")
	require.NoError(t, err)
	assert.Equal(t, 0, itemAvail.Reserved, "SKU-AVAIL must not have any reserved units after aborted transaction")

	itemDepl, err := repo.GetStock(ctx, "SKU-DEPLETED")
	require.NoError(t, err)
	assert.Equal(t, 0, itemDepl.Reserved)
}

func TestReserveStock_MissingSKU_AbortsAll(t *testing.T) {
	svc, repo := setupInventoryService()
	ctx := context.Background()

	_, _ = repo.UpsertStock(ctx, "SKU-EXISTS", 10)

	result, err := svc.ReserveStock(ctx, inventory.ReserveStockRequest{
		OrderID: uuid.New(),
		Items: []inventory.StockItemRequest{
			{SKU: "SKU-EXISTS", Quantity: 2},
			{SKU: "SKU-DOES-NOT-EXIST", Quantity: 1},
		},
	})
	require.Error(t, err)
	assert.Nil(t, result)

	var insufficientErr *inventory.ErrInsufficientStock
	require.True(t, errors.As(err, &insufficientErr))
	assert.Contains(t, insufficientErr.FailedSKUs, "SKU-DOES-NOT-EXIST")

	// Verify rollback
	item, err := repo.GetStock(ctx, "SKU-EXISTS")
	require.NoError(t, err)
	assert.Equal(t, 0, item.Reserved)
}

func TestReserveStock_EmptyItems(t *testing.T) {
	svc, _ := setupInventoryService()
	ctx := context.Background()

	_, err := svc.ReserveStock(ctx, inventory.ReserveStockRequest{
		OrderID: uuid.New(),
		Items:   []inventory.StockItemRequest{},
	})
	require.ErrorIs(t, err, inventory.ErrEmptyItems)
}

func TestReserveStock_InvalidOrderID(t *testing.T) {
	svc, _ := setupInventoryService()
	ctx := context.Background()

	_, err := svc.ReserveStock(ctx, inventory.ReserveStockRequest{
		OrderID: uuid.Nil,
		Items: []inventory.StockItemRequest{
			{SKU: "SKU-TEST", Quantity: 1},
		},
	})
	require.ErrorIs(t, err, inventory.ErrInvalidOrderID)
}

func TestCommitStock_Success(t *testing.T) {
	svc, repo := setupInventoryService()
	ctx := context.Background()

	_, _ = repo.UpsertStock(ctx, "SKU-COMMIT-1", 50)

	resID := uuid.New()
	orderID := uuid.New()
	_, err := svc.ReserveStock(ctx, inventory.ReserveStockRequest{
		ReservationID: resID,
		OrderID:       orderID,
		Items: []inventory.StockItemRequest{
			{SKU: "SKU-COMMIT-1", Quantity: 10},
		},
	})
	require.NoError(t, err)

	// Commit the stock
	err = svc.CommitStock(ctx, resID)
	require.NoError(t, err)

	// Verify inventory state: on_hand decremented by 10 (40), reserved decremented by 10 (0), available unchanged (40)
	item, err := repo.GetStock(ctx, "SKU-COMMIT-1")
	require.NoError(t, err)
	assert.Equal(t, 40, item.OnHand)
	assert.Equal(t, 0, item.Reserved)
	assert.Equal(t, 40, item.Available)

	// Verify reservation record status is COMMITTED
	res, err := repo.GetReservationByID(ctx, resID)
	require.NoError(t, err)
	assert.Equal(t, inventory.ReservationStatusCommitted, res.Status)
}

func TestCommitStock_Idempotent(t *testing.T) {
	svc, repo := setupInventoryService()
	ctx := context.Background()

	_, _ = repo.UpsertStock(ctx, "SKU-IDEM-COMMIT", 50)
	resID := uuid.New()

	_, err := svc.ReserveStock(ctx, inventory.ReserveStockRequest{
		ReservationID: resID,
		OrderID:       uuid.New(),
		Items: []inventory.StockItemRequest{
			{SKU: "SKU-IDEM-COMMIT", Quantity: 10},
		},
	})
	require.NoError(t, err)

	// First commit
	require.NoError(t, svc.CommitStock(ctx, resID))

	// Second commit (must succeed idempotently without double-decrementing stock)
	require.NoError(t, svc.CommitStock(ctx, resID))

	item, err := repo.GetStock(ctx, "SKU-IDEM-COMMIT")
	require.NoError(t, err)
	assert.Equal(t, 40, item.OnHand, "on_hand should only be decremented once")
	assert.Equal(t, 0, item.Reserved)
}

func TestCommitStock_AlreadyReleased(t *testing.T) {
	svc, repo := setupInventoryService()
	ctx := context.Background()

	_, _ = repo.UpsertStock(ctx, "SKU-COMMIT-REL", 30)
	resID := uuid.New()

	_, err := svc.ReserveStock(ctx, inventory.ReserveStockRequest{
		ReservationID: resID,
		OrderID:       uuid.New(),
		Items: []inventory.StockItemRequest{
			{SKU: "SKU-COMMIT-REL", Quantity: 5},
		},
	})
	require.NoError(t, err)

	// Release first
	require.NoError(t, svc.ReleaseStock(ctx, resID, "user cancelled"))

	// Attempt to commit a released reservation must fail
	err = svc.CommitStock(ctx, resID)
	require.ErrorIs(t, err, inventory.ErrReservationAlreadyReleased)
}

func TestCommitStock_NotFound(t *testing.T) {
	svc, _ := setupInventoryService()
	ctx := context.Background()

	err := svc.CommitStock(ctx, uuid.New())
	require.ErrorIs(t, err, inventory.ErrReservationNotFound)
}

func TestCommitStockByOrderID_Success(t *testing.T) {
	svc, repo := setupInventoryService()
	ctx := context.Background()

	_, _ = repo.UpsertStock(ctx, "SKU-BY-ORDER", 50)
	orderID := uuid.New()

	_, err := svc.ReserveStock(ctx, inventory.ReserveStockRequest{
		OrderID: orderID,
		Items: []inventory.StockItemRequest{
			{SKU: "SKU-BY-ORDER", Quantity: 15},
		},
	})
	require.NoError(t, err)

	err = svc.CommitStockByOrderID(ctx, orderID)
	require.NoError(t, err)

	item, err := repo.GetStock(ctx, "SKU-BY-ORDER")
	require.NoError(t, err)
	assert.Equal(t, 35, item.OnHand)
	assert.Equal(t, 0, item.Reserved)
}

func TestReleaseStock_Success(t *testing.T) {
	svc, repo := setupInventoryService()
	ctx := context.Background()

	_, _ = repo.UpsertStock(ctx, "SKU-REL-1", 50)
	resID := uuid.New()

	_, err := svc.ReserveStock(ctx, inventory.ReserveStockRequest{
		ReservationID: resID,
		OrderID:       uuid.New(),
		Items: []inventory.StockItemRequest{
			{SKU: "SKU-REL-1", Quantity: 15},
		},
	})
	require.NoError(t, err)

	// Release stock
	err = svc.ReleaseStock(ctx, resID, "payment failed")
	require.NoError(t, err)

	// Verify inventory state: on_hand unchanged (50), reserved restored to 0, available restored to 50
	item, err := repo.GetStock(ctx, "SKU-REL-1")
	require.NoError(t, err)
	assert.Equal(t, 50, item.OnHand)
	assert.Equal(t, 0, item.Reserved)
	assert.Equal(t, 50, item.Available)

	// Verify reservation status is RELEASED
	res, err := repo.GetReservationByID(ctx, resID)
	require.NoError(t, err)
	assert.Equal(t, inventory.ReservationStatusReleased, res.Status)
}

func TestReleaseStock_Idempotent(t *testing.T) {
	svc, repo := setupInventoryService()
	ctx := context.Background()

	_, _ = repo.UpsertStock(ctx, "SKU-IDEM-REL", 50)
	resID := uuid.New()

	_, err := svc.ReserveStock(ctx, inventory.ReserveStockRequest{
		ReservationID: resID,
		OrderID:       uuid.New(),
		Items: []inventory.StockItemRequest{
			{SKU: "SKU-IDEM-REL", Quantity: 10},
		},
	})
	require.NoError(t, err)

	// First release
	require.NoError(t, svc.ReleaseStock(ctx, resID, "cancelled"))

	// Second release (idempotent no-op)
	require.NoError(t, svc.ReleaseStock(ctx, resID, "cancelled again"))

	item, err := repo.GetStock(ctx, "SKU-IDEM-REL")
	require.NoError(t, err)
	assert.Equal(t, 50, item.OnHand)
	assert.Equal(t, 0, item.Reserved)
}

func TestReleaseStock_AlreadyCommitted(t *testing.T) {
	svc, repo := setupInventoryService()
	ctx := context.Background()

	_, _ = repo.UpsertStock(ctx, "SKU-REL-COM", 40)
	resID := uuid.New()

	_, err := svc.ReserveStock(ctx, inventory.ReserveStockRequest{
		ReservationID: resID,
		OrderID:       uuid.New(),
		Items: []inventory.StockItemRequest{
			{SKU: "SKU-REL-COM", Quantity: 10},
		},
	})
	require.NoError(t, err)

	// Commit first
	require.NoError(t, svc.CommitStock(ctx, resID))

	// Attempting to release a committed reservation must fail
	err = svc.ReleaseStock(ctx, resID, "too late")
	require.ErrorIs(t, err, inventory.ErrReservationAlreadyCommitted)
}

func TestReleaseStock_NotFound(t *testing.T) {
	svc, _ := setupInventoryService()
	ctx := context.Background()

	err := svc.ReleaseStock(ctx, uuid.New(), "missing")
	require.ErrorIs(t, err, inventory.ErrReservationNotFound)
}

func TestReleaseStockByOrderID_Success(t *testing.T) {
	svc, repo := setupInventoryService()
	ctx := context.Background()

	_, _ = repo.UpsertStock(ctx, "SKU-REL-ORDER", 50)
	orderID := uuid.New()

	_, err := svc.ReserveStock(ctx, inventory.ReserveStockRequest{
		OrderID: orderID,
		Items: []inventory.StockItemRequest{
			{SKU: "SKU-REL-ORDER", Quantity: 20},
		},
	})
	require.NoError(t, err)

	err = svc.ReleaseStockByOrderID(ctx, orderID, "order timeout")
	require.NoError(t, err)

	item, err := repo.GetStock(ctx, "SKU-REL-ORDER")
	require.NoError(t, err)
	assert.Equal(t, 50, item.OnHand)
	assert.Equal(t, 0, item.Reserved)
}
