package cart_test

import (
	"bytes"
	"fmt"
	"math"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"

	"shopflow/internal/domain/cart"
	"shopflow/internal/domain/catalog"
	"shopflow/internal/domain/money"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestAdversarialCart_ConcurrentOCC_MatchingVersions verifies that when 50 concurrent
// workers attempt to add items to a cart presenting the exact same matching version ("1"),
// exactly ONE worker succeeds and all other 49 fail with HTTP 412 Precondition Failed.
func TestAdversarialCart_ConcurrentOCC_MatchingVersions(t *testing.T) {
	router, svc, reader := setupCartTestRouter(t)
	ownerID := uuid.New()

	reader.AddProduct(&catalog.Product{
		SKU:        "SKU-ADV-MATCH",
		Title:      "Adversarial Matching Item",
		PriceMinor: 1500,
		Price:      money.Money{Amount: 1500, Currency: "USD"},
		IsActive:   true,
	})

	c, err := svc.CreateOrGetActiveCart(t.Context(), ownerID)
	require.NoError(t, err)
	assert.Equal(t, int64(1), c.Version)

	url := fmt.Sprintf("/api/v1/carts/%s/items", c.ID.String())
	concurrency := 50
	var wg sync.WaitGroup
	wg.Add(concurrency)

	var mu sync.Mutex
	var successCount int
	var conflictCount int
	var otherStatusCodes []int

	for i := 0; i < concurrency; i++ {
		workerID := i
		go func() {
			defer wg.Done()
			body := []byte(fmt.Sprintf(`{"sku":"SKU-ADV-MATCH","quantity":%d}`, workerID+1))
			req := httptest.NewRequest(http.MethodPost, url, bytes.NewReader(body))
			req.Header.Set("Content-Type", "application/json")
			req.Header.Set("X-User-ID", ownerID.String())
			req.Header.Set("If-Match", `"1"`)
			rec := httptest.NewRecorder()
			router.ServeHTTP(rec, req)

			mu.Lock()
			defer mu.Unlock()
			switch rec.Code {
			case http.StatusOK:
				successCount++
			case http.StatusPreconditionFailed:
				conflictCount++
			default:
				otherStatusCodes = append(otherStatusCodes, rec.Code)
			}
		}()
	}

	wg.Wait()

	assert.Equal(t, 1, successCount, "Exactly ONE concurrent AddItem with If-Match '1' must succeed")
	assert.Equal(t, concurrency-1, conflictCount, "All other 49 concurrent AddItems must fail with 412")
	assert.Empty(t, otherStatusCodes, "No unexpected status codes")

	// Verify cart is now at version 2 with exactly 1 line item
	finalCart, err := svc.GetCart(t.Context(), c.ID, ownerID)
	require.NoError(t, err)
	assert.Equal(t, int64(2), finalCart.Version)
	assert.Len(t, finalCart.Items, 1)
}

// TestAdversarialCart_ConcurrentOCC_NonMatchingVersions verifies that when 50 concurrent
// workers attempt updates with stale ("0"), negative ("-1"), or future ("999") versions,
// ZERO updates succeed and all 50 fail.
func TestAdversarialCart_ConcurrentOCC_NonMatchingVersions(t *testing.T) {
	router, svc, reader := setupCartTestRouter(t)
	ownerID := uuid.New()

	reader.AddProduct(&catalog.Product{
		SKU:        "SKU-ADV-NONMATCH",
		Title:      "Adversarial Non-Matching Item",
		PriceMinor: 1000,
		Price:      money.Money{Amount: 1000, Currency: "USD"},
		IsActive:   true,
	})

	c, err := svc.CreateOrGetActiveCart(t.Context(), ownerID)
	require.NoError(t, err)

	url := fmt.Sprintf("/api/v1/carts/%s/items", c.ID.String())
	concurrency := 50
	var wg sync.WaitGroup
	wg.Add(concurrency)

	var mu sync.Mutex
	var successCount int
	var failureCount int

	invalidVersions := []string{`"0"`, `"-1"`, `"999"`, `"888"`, `"55"`}

	for i := 0; i < concurrency; i++ {
		workerID := i
		versionHeader := invalidVersions[workerID%len(invalidVersions)]
		go func() {
			defer wg.Done()
			body := []byte(fmt.Sprintf(`{"sku":"SKU-ADV-NONMATCH","quantity":%d}`, workerID+1))
			req := httptest.NewRequest(http.MethodPost, url, bytes.NewReader(body))
			req.Header.Set("Content-Type", "application/json")
			req.Header.Set("X-User-ID", ownerID.String())
			req.Header.Set("If-Match", versionHeader)
			rec := httptest.NewRecorder()
			router.ServeHTTP(rec, req)

			mu.Lock()
			defer mu.Unlock()
			if rec.Code == http.StatusOK {
				successCount++
			} else {
				failureCount++
			}
		}()
	}

	wg.Wait()

	assert.Equal(t, 0, successCount, "Zero updates must succeed with non-matching versions")
	assert.Equal(t, concurrency, failureCount, "All 50 updates must be rejected")

	// Version must remain at 1 and cart empty
	finalCart, err := svc.GetCart(t.Context(), c.ID, ownerID)
	require.NoError(t, err)
	assert.Equal(t, int64(1), finalCart.Version)
	assert.Empty(t, finalCart.Items)
}

// TestAdversarialCart_ConcurrentOCC_MixedCompetingOperations verifies concurrency safety
// across multiple distinct mutating operations (AddItem, UpdateQuantity, RemoveItem, ClearCart)
// competing on the exact same cart version.
func TestAdversarialCart_ConcurrentOCC_MixedCompetingOperations(t *testing.T) {
	router, svc, reader := setupCartTestRouter(t)
	ownerID := uuid.New()

	reader.AddProduct(&catalog.Product{
		SKU:        "SKU-ADV-MIXED-1",
		Title:      "Mixed Item 1",
		PriceMinor: 2000,
		Price:      money.Money{Amount: 2000, Currency: "USD"},
		IsActive:   true,
	})
	reader.AddProduct(&catalog.Product{
		SKU:        "SKU-ADV-MIXED-2",
		Title:      "Mixed Item 2",
		PriceMinor: 3000,
		Price:      money.Money{Amount: 3000, Currency: "USD"},
		IsActive:   true,
	})

	c, err := svc.CreateOrGetActiveCart(t.Context(), ownerID)
	require.NoError(t, err)

	// Add an initial item so UpdateQuantity, RemoveItem, and ClearCart have data to operate on
	c, err = svc.AddItem(t.Context(), c.ID, ownerID, "SKU-ADV-MIXED-1", 2, 1)
	require.NoError(t, err)
	assert.Equal(t, int64(2), c.Version) // Active version is now 2

	cartID := c.ID.String()
	concurrency := 40
	var wg sync.WaitGroup
	wg.Add(concurrency)

	var mu sync.Mutex
	var successCount int
	var failureCount int

	for i := 0; i < concurrency; i++ {
		workerID := i
		// Worker 13 gets the valid matching version "2", all other 39 get non-matching/stale versions
		var versionHeader string
		if workerID == 13 {
			versionHeader = `"2"`
		} else {
			staleVersions := []string{`"0"`, `"-1"`, `"1"`, `"999"`, `"888"`}
			versionHeader = staleVersions[workerID%len(staleVersions)]
		}

		go func() {
			defer wg.Done()
			var req *http.Request
			switch workerID % 4 {
			case 0:
				// AddItem
				body := []byte(`{"sku":"SKU-ADV-MIXED-2","quantity":1}`)
				req = httptest.NewRequest(http.MethodPost, fmt.Sprintf("/api/v1/carts/%s/items", cartID), bytes.NewReader(body))
				req.Header.Set("Content-Type", "application/json")
			case 1:
				// UpdateQuantity
				body := []byte(`{"quantity":5}`)
				req = httptest.NewRequest(http.MethodPut, fmt.Sprintf("/api/v1/carts/%s/items/SKU-ADV-MIXED-1", cartID), bytes.NewReader(body))
				req.Header.Set("Content-Type", "application/json")
			case 2:
				// RemoveItem
				req = httptest.NewRequest(http.MethodDelete, fmt.Sprintf("/api/v1/carts/%s/items/SKU-ADV-MIXED-1", cartID), nil)
			case 3:
				// ClearCart
				req = httptest.NewRequest(http.MethodDelete, fmt.Sprintf("/api/v1/carts/%s/clear", cartID), nil)
			}

			req.Header.Set("X-User-ID", ownerID.String())
			req.Header.Set("If-Match", versionHeader)
			rec := httptest.NewRecorder()
			router.ServeHTTP(rec, req)

			mu.Lock()
			defer mu.Unlock()
			if rec.Code == http.StatusOK || rec.Code == http.StatusNoContent {
				successCount++
			} else {
				failureCount++
			}
		}()
	}

	wg.Wait()

	assert.Equal(t, 1, successCount, "Exactly ONE competing operation with matching version must succeed")
	assert.Equal(t, concurrency-1, failureCount, "All other 39 operations must be rejected")

	finalCart, err := svc.GetCart(t.Context(), c.ID, ownerID)
	require.NoError(t, err)
	assert.Equal(t, int64(3), finalCart.Version)
}

// TestAdversarialCart_QuantityBoundaryFuzzing verifies boundary handling of zero,
// negative, and large quantities in cart operations and totals calculations.
func TestAdversarialCart_QuantityBoundaryFuzzing(t *testing.T) {
	router, svc, reader := setupCartTestRouter(t)
	ownerID := uuid.New()

	reader.AddProduct(&catalog.Product{
		SKU:        "SKU-QTY-BOUND",
		Title:      "Quantity Boundary Item",
		PriceMinor: 1000, // $10.00
		Price:      money.Money{Amount: 1000, Currency: "USD"},
		IsActive:   true,
	})

	c, err := svc.CreateOrGetActiveCart(t.Context(), ownerID)
	require.NoError(t, err)
	cartID := c.ID.String()

	t.Run("Zero_Quantity_AddItem_Rejected", func(t *testing.T) {
		// HTTP layer: AddItem with quantity 0 -> 400 Bad Request
		body := []byte(`{"sku":"SKU-QTY-BOUND","quantity":0}`)
		req := httptest.NewRequest(http.MethodPost, fmt.Sprintf("/api/v1/carts/%s/items", cartID), bytes.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("X-User-ID", ownerID.String())
		req.Header.Set("If-Match", `"1"`)
		rec := httptest.NewRecorder()
		router.ServeHTTP(rec, req)

		assert.Equal(t, http.StatusBadRequest, rec.Code)

		// Service layer: AddItem with quantity 0 -> ErrInvalidQuantity
		_, err := svc.AddItem(t.Context(), c.ID, ownerID, "SKU-QTY-BOUND", 0, 1)
		require.ErrorIs(t, err, cart.ErrInvalidQuantity)
	})

	t.Run("Negative_Quantity_AddItem_Rejected", func(t *testing.T) {
		negativeQuantities := []int{-1, -100, -99999}
		for _, nq := range negativeQuantities {
			// HTTP layer
			body := []byte(fmt.Sprintf(`{"sku":"SKU-QTY-BOUND","quantity":%d}`, nq))
			req := httptest.NewRequest(http.MethodPost, fmt.Sprintf("/api/v1/carts/%s/items", cartID), bytes.NewReader(body))
			req.Header.Set("Content-Type", "application/json")
			req.Header.Set("X-User-ID", ownerID.String())
			req.Header.Set("If-Match", `"1"`)
			rec := httptest.NewRecorder()
			router.ServeHTTP(rec, req)

			assert.Equal(t, http.StatusBadRequest, rec.Code)

			// Service layer
			_, err := svc.AddItem(t.Context(), c.ID, ownerID, "SKU-QTY-BOUND", nq, 1)
			require.ErrorIs(t, err, cart.ErrInvalidQuantity)
		}
	})

	t.Run("UpdateQuantity_Zero_And_Negative_Removes_Item", func(t *testing.T) {
		// Add item first (version becomes 2)
		c, err := svc.AddItem(t.Context(), c.ID, ownerID, "SKU-QTY-BOUND", 5, 1)
		require.NoError(t, err)
		assert.Equal(t, int64(2), c.Version)
		require.Len(t, c.Items, 1)

		// UpdateQuantity with quantity 0 should remove the item (version becomes 3)
		c, err = svc.UpdateQuantity(t.Context(), c.ID, ownerID, "SKU-QTY-BOUND", 0, 2)
		require.NoError(t, err)
		assert.Equal(t, int64(3), c.Version)
		assert.Empty(t, c.Items)

		// Add item again (version becomes 4)
		c, err = svc.AddItem(t.Context(), c.ID, ownerID, "SKU-QTY-BOUND", 5, 3)
		require.NoError(t, err)
		assert.Equal(t, int64(4), c.Version)

		// UpdateQuantity with quantity -5 should also remove the item (version becomes 5)
		c, err = svc.UpdateQuantity(t.Context(), c.ID, ownerID, "SKU-QTY-BOUND", -5, 4)
		require.NoError(t, err)
		assert.Equal(t, int64(5), c.Version)
		assert.Empty(t, c.Items)
	})

	t.Run("CalculateTotals_Multiplication_Overflow_Near_MaxInt64", func(t *testing.T) {
		unitPrice := int64(1000) // $10.00
		maxSafeQty := int(math.MaxInt64 / unitPrice)

		// Exact safe boundary: maxSafeQty * 1000 <= math.MaxInt64
		safeCart := &cart.Cart{
			Currency: "USD",
			Items: []cart.CartItem{
				{
					SKU:            "SKU-SAFE",
					Quantity:       maxSafeQty,
					UnitPriceMinor: unitPrice,
				},
			},
		}
		err := safeCart.CalculateTotals("USD")
		require.NoError(t, err)
		assert.Equal(t, int64(maxSafeQty)*unitPrice, safeCart.TotalAmount.Amount)

		// One unit above safe boundary: (maxSafeQty + 1) * 1000 > math.MaxInt64
		overflowCart := &cart.Cart{
			Currency: "USD",
			Items: []cart.CartItem{
				{
					SKU:            "SKU-OVERFLOW",
					Quantity:       maxSafeQty + 1,
					UnitPriceMinor: unitPrice,
				},
			},
		}
		err = overflowCart.CalculateTotals("USD")
		require.ErrorIs(t, err, cart.ErrArithmeticOverflow)
	})

	t.Run("CalculateTotals_Addition_Overflow_Across_Multiple_Items", func(t *testing.T) {
		halfMax := int64(math.MaxInt64/2 + 100)
		multiItemCart := &cart.Cart{
			Currency: "USD",
			Items: []cart.CartItem{
				{
					SKU:            "SKU-HALF-1",
					Quantity:       1,
					UnitPriceMinor: halfMax,
				},
				{
					SKU:            "SKU-HALF-2",
					Quantity:       1,
					UnitPriceMinor: halfMax,
				},
			},
		}
		// Sum of line totals exceeds math.MaxInt64
		err := multiItemCart.CalculateTotals("USD")
		require.ErrorIs(t, err, cart.ErrArithmeticOverflow)
	})

	t.Run("CalculateTotals_Negative_Price_And_Quantity_Rejected", func(t *testing.T) {
		// Negative unit price
		negPriceCart := &cart.Cart{
			Currency: "USD",
			Items: []cart.CartItem{
				{
					SKU:            "SKU-NEG-PRICE",
					Quantity:       1,
					UnitPriceMinor: -500,
				},
			},
		}
		err := negPriceCart.CalculateTotals("USD")
		require.ErrorIs(t, err, cart.ErrInvalidPrice)

		// Negative quantity
		negQtyCart := &cart.Cart{
			Currency: "USD",
			Items: []cart.CartItem{
				{
					SKU:            "SKU-NEG-QTY",
					Quantity:       -2,
					UnitPriceMinor: 500,
				},
			},
		}
		err = negQtyCart.CalculateTotals("USD")
		require.ErrorIs(t, err, cart.ErrInvalidQuantity)
	})
}

// TestAdversarialCart_ConcurrentReadersAndWriters verifies that concurrent reads
// and writes on cart instances do not trigger race conditions or data corruption.
func TestAdversarialCart_ConcurrentReadersAndWriters(t *testing.T) {
	router, svc, reader := setupCartTestRouter(t)
	ownerID := uuid.New()

	reader.AddProduct(&catalog.Product{
		SKU:        "SKU-CONC-RW",
		Title:      "Concurrent RW Item",
		PriceMinor: 2500,
		Price:      money.Money{Amount: 2500, Currency: "USD"},
		IsActive:   true,
	})

	c, err := svc.CreateOrGetActiveCart(t.Context(), ownerID)
	require.NoError(t, err)
	cartID := c.ID.String()

	concurrency := 25
	var wg sync.WaitGroup
	wg.Add(concurrency * 2)

	// 25 Concurrent readers
	for i := 0; i < concurrency; i++ {
		go func() {
			defer wg.Done()
			req := httptest.NewRequest(http.MethodGet, fmt.Sprintf("/api/v1/carts/%s", cartID), nil)
			req.Header.Set("X-User-ID", ownerID.String())
			rec := httptest.NewRecorder()
			router.ServeHTTP(rec, req)
			assert.Equal(t, http.StatusOK, rec.Code)
		}()
	}

	// 25 Concurrent writers (all competing on version 1)
	var mu sync.Mutex
	var writeSuccesses int
	var writeConflicts int

	for i := 0; i < concurrency; i++ {
		workerID := i
		go func() {
			defer wg.Done()
			body := []byte(fmt.Sprintf(`{"sku":"SKU-CONC-RW","quantity":%d}`, workerID+1))
			req := httptest.NewRequest(http.MethodPost, fmt.Sprintf("/api/v1/carts/%s/items", cartID), bytes.NewReader(body))
			req.Header.Set("Content-Type", "application/json")
			req.Header.Set("X-User-ID", ownerID.String())
			req.Header.Set("If-Match", `"1"`)
			rec := httptest.NewRecorder()
			router.ServeHTTP(rec, req)

			mu.Lock()
			defer mu.Unlock()
			if rec.Code == http.StatusOK {
				writeSuccesses++
			} else if rec.Code == http.StatusPreconditionFailed {
				writeConflicts++
			}
		}()
	}

	wg.Wait()

	assert.Equal(t, 1, writeSuccesses, "Exactly 1 concurrent writer with If-Match '1' must succeed")
	assert.Equal(t, concurrency-1, writeConflicts, "All 24 other concurrent writers must fail with 412")
}
