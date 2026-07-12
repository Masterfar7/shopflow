package cart_test

import (
	"context"
	"math"
	"sort"
	"sync"
	"testing"
	"time"

	"shopflow/internal/domain/cart"
	"shopflow/internal/domain/catalog"
	"shopflow/internal/domain/money"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// mockTx implements pgx.Tx for in-memory testing.
type mockTx struct {
	pgx.Tx
}

func (m *mockTx) Commit(ctx context.Context) error   { return nil }
func (m *mockTx) Rollback(ctx context.Context) error { return nil }

type mockTxBeginner struct{}

func (b *mockTxBeginner) BeginTx(ctx context.Context, opts pgx.TxOptions) (pgx.Tx, error) {
	return &mockTx{}, nil
}

type memoryCartRepo struct {
	mu        sync.RWMutex
	carts     map[uuid.UUID]*cart.Cart
	userCarts map[uuid.UUID]uuid.UUID
	items     map[uuid.UUID]map[string]*cart.CartItem
}

func newMemoryCartRepo() *memoryCartRepo {
	return &memoryCartRepo{
		carts:     make(map[uuid.UUID]*cart.Cart),
		userCarts: make(map[uuid.UUID]uuid.UUID),
		items:     make(map[uuid.UUID]map[string]*cart.CartItem),
	}
}

func (m *memoryCartRepo) GetCartByID(ctx context.Context, id uuid.UUID) (*cart.Cart, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()

	c, exists := m.carts[id]
	if !exists {
		return nil, cart.ErrCartNotFound
	}
	cp := *c
	return &cp, nil
}

func (m *memoryCartRepo) GetActiveCartByUserID(ctx context.Context, userID uuid.UUID) (*cart.Cart, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()

	cartID, exists := m.userCarts[userID]
	if !exists {
		return nil, cart.ErrCartNotFound
	}
	c := m.carts[cartID]
	if c.Status != cart.CartStatusActive {
		return nil, cart.ErrCartNotFound
	}
	cp := *c
	return &cp, nil
}

func (m *memoryCartRepo) CreateCart(ctx context.Context, c *cart.Cart) error {
	m.mu.Lock()
	defer m.mu.Unlock()

	if existingID, exists := m.userCarts[c.UserID]; exists {
		existing := m.carts[existingID]
		if existing.Status == cart.CartStatusActive {
			*c = *existing
			return nil
		}
	}

	c.Status = cart.CartStatusActive
	c.Version = 1
	c.CreatedAt = time.Now().UTC()
	c.UpdatedAt = c.CreatedAt

	m.carts[c.ID] = c
	m.userCarts[c.UserID] = c.ID
	m.items[c.ID] = make(map[string]*cart.CartItem)
	return nil
}

func (m *memoryCartRepo) IncrementCartVersion(ctx context.Context, tx pgx.Tx, cartID uuid.UUID, expectedVersion int64) (int64, error) {
	m.mu.Lock()
	defer m.mu.Unlock()

	c, exists := m.carts[cartID]
	if !exists {
		return 0, cart.ErrCartNotFound
	}
	if c.Status != cart.CartStatusActive {
		return 0, cart.ErrCartNotActive
	}
	if c.Version != expectedVersion {
		return 0, cart.ErrOptimisticLockConflict
	}

	c.Version++
	c.UpdatedAt = time.Now().UTC()
	return c.Version, nil
}

func (m *memoryCartRepo) GetCartItems(ctx context.Context, cartID uuid.UUID) ([]cart.CartItem, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()

	itemMap, exists := m.items[cartID]
	if !exists {
		return []cart.CartItem{}, nil
	}

	var items []cart.CartItem
	for _, it := range itemMap {
		items = append(items, *it)
	}

	sort.Slice(items, func(i, j int) bool {
		return items[i].SKU < items[j].SKU
	})
	return items, nil
}

func (m *memoryCartRepo) UpsertCartItem(ctx context.Context, tx pgx.Tx, cartID uuid.UUID, sku string, quantity int, unitPriceMinor int64) error {
	m.mu.Lock()
	defer m.mu.Unlock()

	itemMap, exists := m.items[cartID]
	if !exists {
		itemMap = make(map[string]*cart.CartItem)
		m.items[cartID] = itemMap
	}

	if existing, found := itemMap[sku]; found {
		existing.Quantity += quantity
		existing.UnitPriceMinor = unitPriceMinor
		existing.UpdatedAt = time.Now().UTC()
	} else {
		itemMap[sku] = &cart.CartItem{
			ID:             uuid.New(),
			CartID:         cartID,
			SKU:            sku,
			Quantity:       quantity,
			UnitPriceMinor: unitPriceMinor,
			CreatedAt:      time.Now().UTC(),
			UpdatedAt:      time.Now().UTC(),
		}
	}
	return nil
}

func (m *memoryCartRepo) UpdateCartItemQuantity(ctx context.Context, tx pgx.Tx, cartID uuid.UUID, sku string, quantity int) error {
	m.mu.Lock()
	defer m.mu.Unlock()

	itemMap, exists := m.items[cartID]
	if !exists {
		return cart.ErrItemNotFound
	}
	item, found := itemMap[sku]
	if !found {
		return cart.ErrItemNotFound
	}
	item.Quantity = quantity
	item.UpdatedAt = time.Now().UTC()
	return nil
}

func (m *memoryCartRepo) DeleteCartItem(ctx context.Context, tx pgx.Tx, cartID uuid.UUID, sku string) error {
	m.mu.Lock()
	defer m.mu.Unlock()

	itemMap, exists := m.items[cartID]
	if !exists {
		return cart.ErrItemNotFound
	}
	if _, found := itemMap[sku]; !found {
		return cart.ErrItemNotFound
	}
	delete(itemMap, sku)
	return nil
}

func (m *memoryCartRepo) ClearCartItems(ctx context.Context, tx pgx.Tx, cartID uuid.UUID) error {
	m.mu.Lock()
	defer m.mu.Unlock()

	m.items[cartID] = make(map[string]*cart.CartItem)
	return nil
}

type mockCatalogReader struct {
	products map[string]*catalog.Product
}

func newMockCatalogReader() *mockCatalogReader {
	return &mockCatalogReader{
		products: make(map[string]*catalog.Product),
	}
}

func (r *mockCatalogReader) AddProduct(p *catalog.Product) {
	r.products[p.SKU] = p
}

func (r *mockCatalogReader) GetProduct(ctx context.Context, id uuid.UUID) (*catalog.Product, error) {
	for _, p := range r.products {
		if p.ID == id {
			return p, nil
		}
	}
	return nil, catalog.ErrProductNotFound
}

func (r *mockCatalogReader) GetProductBySKU(ctx context.Context, sku string) (*catalog.Product, error) {
	p, exists := r.products[sku]
	if !exists {
		return nil, catalog.ErrProductNotFound
	}
	return p, nil
}

func (r *mockCatalogReader) GetProductsBySKUs(ctx context.Context, skus []string) ([]catalog.Product, error) {
	var result []catalog.Product
	for _, sku := range skus {
		if p, exists := r.products[sku]; exists {
			result = append(result, *p)
		}
	}
	return result, nil
}

func TestCartService_CreateActiveCart_Idempotent(t *testing.T) {
	repo := newMemoryCartRepo()
	reader := newMockCatalogReader()
	beginner := &mockTxBeginner{}
	svc := cart.NewService(repo, reader, beginner, nil)

	customerID := uuid.New()
	c1, err := svc.CreateOrGetActiveCart(context.Background(), customerID)
	require.NoError(t, err)
	assert.Equal(t, customerID, c1.UserID)
	assert.Equal(t, cart.CartStatusActive, c1.Status)
	assert.Equal(t, int64(1), c1.Version)

	// Second call for same user returns identical active cart
	c2, err := svc.CreateOrGetActiveCart(context.Background(), customerID)
	require.NoError(t, err)
	assert.Equal(t, c1.ID, c2.ID)
}

func TestCartService_AddItem_SnapshotPrice(t *testing.T) {
	repo := newMemoryCartRepo()
	reader := newMockCatalogReader()
	beginner := &mockTxBeginner{}
	svc := cart.NewService(repo, reader, beginner, nil)

	reader.AddProduct(&catalog.Product{
		ID:         uuid.New(),
		SKU:        "SKU-SHIRT-M",
		Title:      "Cotton Shirt Medium",
		PriceMinor: 2500,
		Price:      money.Money{Amount: 2500, Currency: "USD"},
		IsActive:   true,
		Version:    1,
	})

	customerID := uuid.New()
	c, err := svc.CreateOrGetActiveCart(context.Background(), customerID)
	require.NoError(t, err)

	updatedCart, err := svc.AddItem(context.Background(), c.ID, customerID, "SKU-SHIRT-M", 2, 1)
	require.NoError(t, err)
	assert.Equal(t, int64(2), updatedCart.Version)
	require.Len(t, updatedCart.Items, 1)

	item := updatedCart.Items[0]
	assert.Equal(t, "SKU-SHIRT-M", item.SKU)
	assert.Equal(t, "Cotton Shirt Medium", item.Title)
	assert.Equal(t, 2, item.Quantity)
	assert.Equal(t, int64(2500), item.UnitPrice.Amount)
	assert.Equal(t, int64(5000), item.LineTotal.Amount)
	assert.Equal(t, int64(5000), updatedCart.TotalAmount.Amount)
}

func TestCartService_AddItem_InactiveProduct_Rejected(t *testing.T) {
	repo := newMemoryCartRepo()
	reader := newMockCatalogReader()
	beginner := &mockTxBeginner{}
	svc := cart.NewService(repo, reader, beginner, nil)

	reader.AddProduct(&catalog.Product{
		ID:         uuid.New(),
		SKU:        "SKU-DISCONTINUED",
		Title:      "Old Widget",
		PriceMinor: 1000,
		Price:      money.Money{Amount: 1000, Currency: "USD"},
		IsActive:   false, // inactive!
	})

	customerID := uuid.New()
	c, err := svc.CreateOrGetActiveCart(context.Background(), customerID)
	require.NoError(t, err)

	_, err = svc.AddItem(context.Background(), c.ID, customerID, "SKU-DISCONTINUED", 1, 1)
	require.ErrorIs(t, err, cart.ErrProductUnavailable)
}

func TestCartService_AddItem_Ownership_Unauthorized(t *testing.T) {
	repo := newMemoryCartRepo()
	reader := newMockCatalogReader()
	beginner := &mockTxBeginner{}
	svc := cart.NewService(repo, reader, beginner, nil)

	reader.AddProduct(&catalog.Product{
		SKU:        "SKU-TEST",
		PriceMinor: 100,
		Price:      money.Money{Amount: 100, Currency: "USD"},
		IsActive:   true,
	})

	ownerID := uuid.New()
	attackerID := uuid.New()

	c, err := svc.CreateOrGetActiveCart(context.Background(), ownerID)
	require.NoError(t, err)

	_, err = svc.AddItem(context.Background(), c.ID, attackerID, "SKU-TEST", 1, 1)
	require.ErrorIs(t, err, cart.ErrForbidden)
}

func TestCartService_AddItem_OCC_Conflict(t *testing.T) {
	repo := newMemoryCartRepo()
	reader := newMockCatalogReader()
	beginner := &mockTxBeginner{}
	svc := cart.NewService(repo, reader, beginner, nil)

	reader.AddProduct(&catalog.Product{
		SKU:        "SKU-TEST",
		PriceMinor: 100,
		Price:      money.Money{Amount: 100, Currency: "USD"},
		IsActive:   true,
	})

	customerID := uuid.New()
	c, err := svc.CreateOrGetActiveCart(context.Background(), customerID)
	require.NoError(t, err)

	// Pass stale version (e.g. 99 instead of 1)
	_, err = svc.AddItem(context.Background(), c.ID, customerID, "SKU-TEST", 1, 99)
	require.ErrorIs(t, err, cart.ErrOptimisticLockConflict)
}

func TestCartService_UpdateQuantity_And_Remove(t *testing.T) {
	repo := newMemoryCartRepo()
	reader := newMockCatalogReader()
	beginner := &mockTxBeginner{}
	svc := cart.NewService(repo, reader, beginner, nil)

	reader.AddProduct(&catalog.Product{
		SKU:        "SKU-BOOK",
		Title:      "Go Programming",
		PriceMinor: 3000,
		Price:      money.Money{Amount: 3000, Currency: "USD"},
		IsActive:   true,
	})

	customerID := uuid.New()
	c, err := svc.CreateOrGetActiveCart(context.Background(), customerID)
	require.NoError(t, err)

	c, err = svc.AddItem(context.Background(), c.ID, customerID, "SKU-BOOK", 1, 1)
	require.NoError(t, err)
	assert.Equal(t, int64(2), c.Version)

	// Update quantity to 3
	c, err = svc.UpdateQuantity(context.Background(), c.ID, customerID, "SKU-BOOK", 3, 2)
	require.NoError(t, err)
	assert.Equal(t, int64(3), c.Version)
	assert.Equal(t, 3, c.Items[0].Quantity)
	assert.Equal(t, int64(9000), c.TotalAmount.Amount)

	// Update quantity to 0 removes item
	c, err = svc.UpdateQuantity(context.Background(), c.ID, customerID, "SKU-BOOK", 0, 3)
	require.NoError(t, err)
	assert.Equal(t, int64(4), c.Version)
	assert.Empty(t, c.Items)
	assert.Equal(t, int64(0), c.TotalAmount.Amount)
}

func TestCartService_ClearCart(t *testing.T) {
	repo := newMemoryCartRepo()
	reader := newMockCatalogReader()
	beginner := &mockTxBeginner{}
	svc := cart.NewService(repo, reader, beginner, nil)

	reader.AddProduct(&catalog.Product{
		SKU:        "SKU-ITEM",
		PriceMinor: 500,
		Price:      money.Money{Amount: 500, Currency: "USD"},
		IsActive:   true,
	})

	customerID := uuid.New()
	c, err := svc.CreateOrGetActiveCart(context.Background(), customerID)
	require.NoError(t, err)

	c, err = svc.AddItem(context.Background(), c.ID, customerID, "SKU-ITEM", 5, 1)
	require.NoError(t, err)
	require.Len(t, c.Items, 1)

	c, err = svc.ClearCart(context.Background(), c.ID, customerID, 2)
	require.NoError(t, err)
	assert.Empty(t, c.Items)
	assert.Equal(t, int64(0), c.TotalAmount.Amount)
	assert.Equal(t, int64(3), c.Version)
}

func TestCart_CalculateTotals_OverflowProtection(t *testing.T) {
	c := &cart.Cart{
		Items: []cart.CartItem{
			{
				SKU:            "SKU-HUGE",
				Quantity:       2,
				UnitPriceMinor: math.MaxInt64/2 + 10,
			},
		},
	}
	err := c.CalculateTotals("USD")
	require.ErrorIs(t, err, cart.ErrArithmeticOverflow)
}

func TestCartService_NilCustomerID_Unauthorized(t *testing.T) {
	repo := newMemoryCartRepo()
	reader := newMockCatalogReader()
	beginner := &mockTxBeginner{}
	svc := cart.NewService(repo, reader, beginner, nil)

	ctx := context.Background()
	ownerID := uuid.New()
	c, err := svc.CreateOrGetActiveCart(ctx, ownerID)
	require.NoError(t, err)

	// CreateOrGetActiveCart with uuid.Nil -> ErrUnauthorized
	_, err = svc.CreateOrGetActiveCart(ctx, uuid.Nil)
	require.ErrorIs(t, err, cart.ErrUnauthorized)

	// GetCart with uuid.Nil -> ErrUnauthorized
	_, err = svc.GetCart(ctx, c.ID, uuid.Nil)
	require.ErrorIs(t, err, cart.ErrUnauthorized)

	// AddItem with uuid.Nil -> ErrUnauthorized
	_, err = svc.AddItem(ctx, c.ID, uuid.Nil, "SKU-ITEM", 1, 1)
	require.ErrorIs(t, err, cart.ErrUnauthorized)

	// UpdateQuantity with uuid.Nil -> ErrUnauthorized
	_, err = svc.UpdateQuantity(ctx, c.ID, uuid.Nil, "SKU-ITEM", 2, 1)
	require.ErrorIs(t, err, cart.ErrUnauthorized)

	// RemoveItem with uuid.Nil -> ErrUnauthorized
	_, err = svc.RemoveItem(ctx, c.ID, uuid.Nil, "SKU-ITEM", 1)
	require.ErrorIs(t, err, cart.ErrUnauthorized)

	// ClearCart with uuid.Nil -> ErrUnauthorized
	_, err = svc.ClearCart(ctx, c.ID, uuid.Nil, 1)
	require.ErrorIs(t, err, cart.ErrUnauthorized)
}

func TestCartService_MismatchedOwner_Forbidden(t *testing.T) {
	repo := newMemoryCartRepo()
	reader := newMockCatalogReader()
	beginner := &mockTxBeginner{}
	svc := cart.NewService(repo, reader, beginner, nil)

	reader.AddProduct(&catalog.Product{
		SKU:        "SKU-ITEM",
		PriceMinor: 500,
		Price:      money.Money{Amount: 500, Currency: "USD"},
		IsActive:   true,
	})

	ctx := context.Background()
	ownerID := uuid.New()
	attackerID := uuid.New()

	c, err := svc.CreateOrGetActiveCart(ctx, ownerID)
	require.NoError(t, err)

	c, err = svc.AddItem(ctx, c.ID, ownerID, "SKU-ITEM", 2, 1)
	require.NoError(t, err)

	// GetCart by attacker -> ErrForbidden
	_, err = svc.GetCart(ctx, c.ID, attackerID)
	require.ErrorIs(t, err, cart.ErrForbidden)

	// AddItem by attacker -> ErrForbidden
	_, err = svc.AddItem(ctx, c.ID, attackerID, "SKU-ITEM", 1, 2)
	require.ErrorIs(t, err, cart.ErrForbidden)

	// UpdateQuantity by attacker -> ErrForbidden
	_, err = svc.UpdateQuantity(ctx, c.ID, attackerID, "SKU-ITEM", 5, 2)
	require.ErrorIs(t, err, cart.ErrForbidden)

	// RemoveItem by attacker -> ErrForbidden
	_, err = svc.RemoveItem(ctx, c.ID, attackerID, "SKU-ITEM", 2)
	require.ErrorIs(t, err, cart.ErrForbidden)

	// ClearCart by attacker -> ErrForbidden
	_, err = svc.ClearCart(ctx, c.ID, attackerID, 2)
	require.ErrorIs(t, err, cart.ErrForbidden)
}

func TestCartService_CurrencyMismatch(t *testing.T) {
	repo := newMemoryCartRepo()
	reader := newMockCatalogReader()
	beginner := &mockTxBeginner{}
	svc := cart.NewService(repo, reader, beginner, nil)

	reader.AddProduct(&catalog.Product{
		SKU:        "SKU-USD",
		PriceMinor: 1000,
		Price:      money.Money{Amount: 1000, Currency: "USD"},
		IsActive:   true,
	})
	reader.AddProduct(&catalog.Product{
		SKU:        "SKU-EUR",
		PriceMinor: 1000,
		Price:      money.Money{Amount: 1000, Currency: "EUR"},
		IsActive:   true,
	})

	ctx := context.Background()
	customerID := uuid.New()

	c, err := svc.CreateOrGetActiveCart(ctx, customerID)
	require.NoError(t, err)
	assert.Equal(t, "USD", c.Currency)

	// Add USD item -> succeeds
	c, err = svc.AddItem(ctx, c.ID, customerID, "SKU-USD", 1, 1)
	require.NoError(t, err)

	// Add EUR item to USD cart -> ErrCurrencyMismatch
	_, err = svc.AddItem(ctx, c.ID, customerID, "SKU-EUR", 1, 2)
	require.ErrorIs(t, err, cart.ErrCurrencyMismatch)

	// CalculateTotals directly asserting currency mismatch
	testCart := &cart.Cart{
		Currency: "USD",
		Items: []cart.CartItem{
			{
				SKU:            "SKU-1",
				Quantity:       1,
				UnitPriceMinor: 100,
				UnitPrice:      money.Money{Amount: 100, Currency: "USD"},
			},
			{
				SKU:            "SKU-2",
				Quantity:       1,
				UnitPriceMinor: 200,
				UnitPrice:      money.Money{Amount: 200, Currency: "EUR"}, // mismatch!
			},
		},
	}
	err = testCart.CalculateTotals("USD")
	require.ErrorIs(t, err, cart.ErrCurrencyMismatch)

	// CalculateTotals with single item whose currency does not match requested currency
	singleCart := &cart.Cart{
		Items: []cart.CartItem{
			{
				SKU:            "SKU-1",
				Quantity:       1,
				UnitPriceMinor: 100,
				UnitPrice:      money.Money{Amount: 100, Currency: "EUR"},
			},
		},
	}
	err = singleCart.CalculateTotals("USD")
	require.ErrorIs(t, err, cart.ErrCurrencyMismatch)
}

func TestCartService_OCC_Strictness(t *testing.T) {
	repo := newMemoryCartRepo()
	reader := newMockCatalogReader()
	beginner := &mockTxBeginner{}
	svc := cart.NewService(repo, reader, beginner, nil)

	reader.AddProduct(&catalog.Product{
		SKU:        "SKU-OCC",
		PriceMinor: 1000,
		Price:      money.Money{Amount: 1000, Currency: "USD"},
		IsActive:   true,
	})

	ctx := context.Background()
	customerID := uuid.New()

	c, err := svc.CreateOrGetActiveCart(ctx, customerID)
	require.NoError(t, err)

	// AddItem with expectedVersion <= 0 -> ErrInvalidVersion
	_, err = svc.AddItem(ctx, c.ID, customerID, "SKU-OCC", 1, 0)
	require.ErrorIs(t, err, cart.ErrInvalidVersion)

	_, err = svc.AddItem(ctx, c.ID, customerID, "SKU-OCC", 1, -1)
	require.ErrorIs(t, err, cart.ErrInvalidVersion)

	// AddItem with valid version 1 -> succeeds, version becomes 2
	c, err = svc.AddItem(ctx, c.ID, customerID, "SKU-OCC", 1, 1)
	require.NoError(t, err)
	assert.Equal(t, int64(2), c.Version)

	// UpdateQuantity with expectedVersion <= 0 -> ErrInvalidVersion
	_, err = svc.UpdateQuantity(ctx, c.ID, customerID, "SKU-OCC", 3, 0)
	require.ErrorIs(t, err, cart.ErrInvalidVersion)

	_, err = svc.UpdateQuantity(ctx, c.ID, customerID, "SKU-OCC", 3, -1)
	require.ErrorIs(t, err, cart.ErrInvalidVersion)

	// RemoveItem with expectedVersion <= 0 -> ErrInvalidVersion
	_, err = svc.RemoveItem(ctx, c.ID, customerID, "SKU-OCC", 0)
	require.ErrorIs(t, err, cart.ErrInvalidVersion)

	_, err = svc.RemoveItem(ctx, c.ID, customerID, "SKU-OCC", -1)
	require.ErrorIs(t, err, cart.ErrInvalidVersion)

	// ClearCart with expectedVersion <= 0 -> ErrInvalidVersion
	_, err = svc.ClearCart(ctx, c.ID, customerID, 0)
	require.ErrorIs(t, err, cart.ErrInvalidVersion)

	_, err = svc.ClearCart(ctx, c.ID, customerID, -1)
	require.ErrorIs(t, err, cart.ErrInvalidVersion)
}
