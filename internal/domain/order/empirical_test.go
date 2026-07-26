package order

import (
	"context"
	"encoding/json"
	"go/ast"
	"go/parser"
	"go/token"
	"math"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"

	"shopflow/internal/domain/catalog"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// Invariant Architecture Guidelines §4.1: float32 and float64 are strictly banned.
func TestEmpirical_NoFloatingPointTypesInOrderPackage(t *testing.T) {
	files, err := filepath.Glob("*.go")
	require.NoError(t, err)
	require.NotEmpty(t, files)

	fset := token.NewFileSet()
	for _, file := range files {
		node, err := parser.ParseFile(fset, file, nil, 0)
		require.NoError(t, err, "failed to parse %s", file)

		ast.Inspect(node, func(n ast.Node) bool {
			if ident, ok := n.(*ast.Ident); ok {
				if ident.Name == "float32" || ident.Name == "float64" {
					t.Fatalf("VIOLATION of Architecture Guidelines §4.1: floating-point type %s found in %s at %v",
						ident.Name, file, fset.Position(ident.Pos()))
				}
			}
			return true
		})
	}
}

// Invariant Architecture Guidelines §4.2: Monetary arithmetic overflow checks.
func TestEmpirical_MonetaryArithmeticOverflowBoundaries(t *testing.T) {
	// Case 1: UnitPrice * Quantity overflows math.MaxInt64
	orderOverflowMult := &Order{
		Items: []OrderItem{
			{
				SKU:            "SKU-HUGE",
				Quantity:       2,
				UnitPriceMinor: math.MaxInt64/2 + 1, // (MaxInt64/2 + 1) * 2 overflows MaxInt64
			},
		},
	}
	err := orderOverflowMult.CalculateTotals()
	assert.ErrorIs(t, err, ErrArithmeticOverflow, "expected arithmetic overflow on multiplication")

	// Case 2: Max possible safe product fits exactly
	orderBoundaryMult := &Order{
		Items: []OrderItem{
			{
				SKU:            "SKU-SAFE",
				Quantity:       1,
				UnitPriceMinor: math.MaxInt64,
			},
		},
	}
	err = orderBoundaryMult.CalculateTotals()
	assert.NoError(t, err)
	assert.Equal(t, int64(math.MaxInt64), orderBoundaryMult.TotalAmountMinor)

	// Case 3: Sum of line items subtotals overflows math.MaxInt64
	orderOverflowSum := &Order{
		Items: []OrderItem{
			{
				SKU:            "SKU-A",
				Quantity:       1,
				UnitPriceMinor: math.MaxInt64 - 10,
			},
			{
				SKU:            "SKU-B",
				Quantity:       1,
				UnitPriceMinor: 20, // (MaxInt64 - 10) + 20 overflows MaxInt64
			},
		},
	}
	err = orderOverflowSum.CalculateTotals()
	assert.ErrorIs(t, err, ErrArithmeticOverflow, "expected arithmetic overflow on addition sum")
}

// Invariant FEAT-ORD-02: Orders lock historical SKU titles and unit prices snapshot.
func TestEmpirical_HistoricalPriceSnapshotImmutability(t *testing.T) {
	repo := NewMockRepository()
	cat := NewMockCatalogReader()

	sku := "SKU-TIMELESS"
	initialTitle := "Original Mechanical Keyboard"
	initialPrice := int64(14900) // $149.00

	cat.AddProduct(catalog.Product{
		ID:         uuid.New(),
		SKU:        sku,
		Title:      initialTitle,
		PriceMinor: initialPrice,
		Currency:   "USD",
		IsActive:   true,
	})

	svc := NewService(repo, cat, nil, nil)
	userID := uuid.New()

	req := CreateOrderRequest{
		Currency: "USD",
		Items: []OrderItemRequest{
			{SKU: sku, Quantity: 2},
		},
	}
	raw, _ := json.Marshal(req)

	// 1. Place order
	order, _, err := svc.CreateOrder(context.Background(), userID, "snapshot-key", req, raw)
	require.NoError(t, err)
	require.NotNil(t, order)
	assert.Equal(t, initialTitle, order.Items[0].TitleSnapshot)
	assert.Equal(t, initialPrice, order.Items[0].UnitPriceMinor)
	assert.Equal(t, int64(29800), order.TotalAmountMinor)

	// 2. Catalog alters price & title dramatically
	cat.AddProduct(catalog.Product{
		ID:         order.Items[0].ID,
		SKU:        sku,
		Title:      "Modernized RGB Keyboard v2",
		PriceMinor: 29900, // $299.00
		Currency:   "USD",
		IsActive:   true,
	})

	// 3. Fetch historical order from service -> must be strictly unchanged!
	fetched, err := svc.GetOrder(context.Background(), order.ID, userID)
	require.NoError(t, err)
	assert.Equal(t, initialTitle, fetched.Items[0].TitleSnapshot, "Order title snapshot must NEVER mutate")
	assert.Equal(t, initialPrice, fetched.Items[0].UnitPriceMinor, "Order price snapshot must NEVER mutate")
	assert.Equal(t, int64(29800), fetched.TotalAmountMinor, "Order grand total must NEVER mutate")
}

// Invariant FEAT-ORD-03: Complete Order State Machine lifecycle and terminal state protection.
func TestEmpirical_OrderStateTransitions(t *testing.T) {
	allStatuses := []OrderStatus{
		StatusPending,
		StatusReservingStock,
		StatusStockReserved,
		StatusPaying,
		StatusPaid,
		StatusConfirmed,
		StatusCancelled,
		StatusRefunded,
	}

	// Valid state transition map: from -> allowed next states (excluding self)
	validTransitions := map[OrderStatus][]OrderStatus{
		StatusPending:        {StatusReservingStock, StatusCancelled},
		StatusReservingStock: {StatusStockReserved, StatusCancelled},
		StatusStockReserved:  {StatusPaying, StatusCancelled},
		StatusPaying:         {StatusPaid, StatusCancelled},
		StatusPaid:           {StatusConfirmed, StatusRefunded},
		StatusConfirmed:      {}, // Terminal
		StatusCancelled:      {}, // Terminal
		StatusRefunded:       {}, // Terminal
	}

	for _, from := range allStatuses {
		allowedList := validTransitions[from]
		allowedMap := make(map[OrderStatus]bool)
		for _, a := range allowedList {
			allowedMap[a] = true
		}

		for _, to := range allStatuses {
			if from == to {
				assert.True(t, from.CanTransitionTo(to), "idempotent transition from %s to %s must be allowed", from, to)
				continue
			}

			can := from.CanTransitionTo(to)
			if allowedMap[to] {
				assert.True(t, can, "transition from %s to %s should be valid", from, to)
			} else {
				assert.False(t, can, "transition from %s to %s MUST NOT be valid", from, to)
			}
		}

		// Verify terminal state property
		if len(allowedList) == 0 {
			assert.True(t, from.IsTerminal(), "%s must be recognized as terminal", from)
		} else {
			assert.False(t, from.IsTerminal(), "%s must NOT be terminal", from)
		}
	}
}

// Invariant FEAT-ORD-04: High-concurrency duplicate requests with exact same idempotency key.
func TestEmpirical_ConcurrentDuplicateRequests(t *testing.T) {
	repo := NewMockRepository()
	cat := NewMockCatalogReader()
	cat.AddProduct(catalog.Product{
		ID:         uuid.New(),
		SKU:        "SKU-CONCURRENT",
		Title:      "Concurrent Item",
		PriceMinor: 2500,
		Currency:   "USD",
		IsActive:   true,
	})

	svc := NewService(repo, cat, nil, nil)
	userID := uuid.New()
	idemKey := "concurrent-race-idem-key"

	req := CreateOrderRequest{
		Currency: "USD",
		Items: []OrderItemRequest{
			{SKU: "SKU-CONCURRENT", Quantity: 2},
		},
	}
	raw, _ := json.Marshal(req)

	concurrency := 30
	var wg sync.WaitGroup
	wg.Add(concurrency)

	type result struct {
		order    *Order
		isCached bool
		err      error
	}

	results := make([]result, concurrency)

	// Pre-create or run first request to establish record, then race concurrent retries
	order1, isCached1, err1 := svc.CreateOrder(context.Background(), userID, idemKey, req, raw)
	require.NoError(t, err1)
	assert.False(t, isCached1)
	require.NotNil(t, order1)

	// Now launch concurrent identical replays
	for i := 0; i < concurrency; i++ {
		idx := i
		go func() {
			defer wg.Done()
			ord, cached, err := svc.CreateOrder(context.Background(), userID, idemKey, req, raw)
			results[idx] = result{order: ord, isCached: cached, err: err}
		}()
	}

	wg.Wait()

	// Every single concurrent replay must succeed and return cached response
	for i, res := range results {
		require.NoError(t, res.err, "request %d failed", i)
		assert.True(t, res.isCached, "request %d should be cached", i)
		require.NotNil(t, res.order, "request %d order should not be nil", i)
		assert.Equal(t, order1.ID, res.order.ID, "request %d order ID mismatch", i)
		assert.Equal(t, order1.TotalAmountMinor, res.order.TotalAmountMinor, "request %d total amount mismatch", i)
	}

	// Verify exactly ONE order exists in persistence
	repo.mu.Lock()
	assert.Len(t, repo.orders, 1)
	repo.mu.Unlock()
}

// Invariant: Simulating simultaneous initial lock attempts
func TestEmpirical_ConcurrentInitialLockAttempts(t *testing.T) {
	repo := NewMockRepository()
	cat := NewMockCatalogReader()
	cat.AddProduct(catalog.Product{
		ID:         uuid.New(),
		SKU:        "SKU-LOCK-RACE",
		Title:      "Race Item",
		PriceMinor: 1000,
		Currency:   "USD",
		IsActive:   true,
	})

	svc := NewService(repo, cat, nil, nil)
	userID := uuid.New()
	idemKey := "initial-lock-race-key"

	req := CreateOrderRequest{
		Currency: "USD",
		Items: []OrderItemRequest{
			{SKU: "SKU-LOCK-RACE", Quantity: 1},
		},
	}
	raw, _ := json.Marshal(req)

	concurrency := 20
	var wg sync.WaitGroup
	wg.Add(concurrency)

	var createdCount int32
	var cachedCount int32
	var conflictCount int32

	for i := 0; i < concurrency; i++ {
		go func() {
			defer wg.Done()
			ord, isCached, err := svc.CreateOrder(context.Background(), userID, idemKey, req, raw)
			if err == nil && ord != nil {
				if isCached {
					atomic.AddInt32(&cachedCount, 1)
				} else {
					atomic.AddInt32(&createdCount, 1)
				}
			} else if err != nil && (err == ErrConcurrentProcessing || err == ErrIdempotencyConflict) {
				atomic.AddInt32(&conflictCount, 1)
			}
		}()
	}

	wg.Wait()

	// Exactly ONE creation must have occurred
	assert.Equal(t, int32(1), atomic.LoadInt32(&createdCount), "exactly 1 order must be created")

	// Total attempts handled gracefully
	totalHandled := atomic.LoadInt32(&createdCount) + atomic.LoadInt32(&cachedCount) + atomic.LoadInt32(&conflictCount)
	assert.Equal(t, int32(concurrency), totalHandled)

	// In database, exactly 1 order
	repo.mu.Lock()
	assert.Len(t, repo.orders, 1)
	repo.mu.Unlock()
}
