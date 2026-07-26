package order

import (
	"bytes"
	"context"
	"encoding/json"
	"go/ast"
	"go/parser"
	"go/token"
	"math"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"

	"shopflow/internal/domain/catalog"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.uber.org/goleak"
)

// TestChallenger_Idempotency_ExactReplay_ReturnsCachedResponse verifies that repeating identical
// order creation requests returns the cached response without creating duplicate orders or outbox events.
func TestChallenger_Idempotency_ExactReplay_ReturnsCachedResponse(t *testing.T) {
	repo := NewMockRepository()
	cat := NewMockCatalogReader()

	cat.AddProduct(catalog.Product{
		ID:         uuid.New(),
		SKU:        "SKU-IDEM-1",
		Title:      "Idempotency Test Item",
		PriceMinor: 2500, // $25.00
		Currency:   "USD",
		IsActive:   true,
	})

	svc := NewService(repo, cat, nil, nil)
	userID := uuid.New()
	idemKey := "test-idempotency-key-001"

	req := CreateOrderRequest{
		Currency: "USD",
		Items: []OrderItemRequest{
			{SKU: "SKU-IDEM-1", Quantity: 2},
		},
	}
	rawPayload, err := json.Marshal(req)
	require.NoError(t, err)

	// 1. Initial Order Placement
	firstOrder, isCached1, err := svc.CreateOrder(context.Background(), userID, idemKey, req, rawPayload)
	require.NoError(t, err)
	assert.False(t, isCached1, "initial request must NOT be cached")
	require.NotNil(t, firstOrder)
	assert.Equal(t, int64(5000), firstOrder.TotalAmountMinor)
	assert.Equal(t, StatusPending, firstOrder.Status)

	// Verify persistence state after 1st request
	repo.mu.Lock()
	assert.Len(t, repo.orders, 1, "exactly 1 order must be persisted")
	assert.Len(t, repo.outboxMessages, 1, "exactly 1 outbox message must be created")
	repo.mu.Unlock()

	// 2. Replay the identical request 10 times consecutively
	for i := 1; i <= 10; i++ {
		replayedOrder, isCached, repErr := svc.CreateOrder(context.Background(), userID, idemKey, req, rawPayload)
		require.NoError(t, repErr, "replayed request #%d should not error", i)
		assert.True(t, isCached, "replayed request #%d MUST be cached", i)
		require.NotNil(t, replayedOrder)

		// Assert complete state equivalence
		assert.Equal(t, firstOrder.ID, replayedOrder.ID, "order ID must match cached order")
		assert.Equal(t, firstOrder.TotalAmountMinor, replayedOrder.TotalAmountMinor)
		assert.Equal(t, firstOrder.Currency, replayedOrder.Currency)
		assert.Equal(t, len(firstOrder.Items), len(replayedOrder.Items))
		assert.Equal(t, firstOrder.Items[0].SKU, replayedOrder.Items[0].SKU)
		assert.Equal(t, firstOrder.Items[0].UnitPriceMinor, replayedOrder.Items[0].UnitPriceMinor)

		// Verify that repository STILL contains exactly 1 order and 1 outbox message
		repo.mu.Lock()
		assert.Len(t, repo.orders, 1, "replay #%d must NOT create duplicate order rows", i)
		assert.Len(t, repo.outboxMessages, 1, "replay #%d must NOT create duplicate outbox messages", i)
		repo.mu.Unlock()
	}
}

// TestChallenger_Idempotency_TamperedPayload_ReturnsConflict tests that sending a different payload
// with an existing idempotency key triggers HTTP 409 Conflict (ErrIdempotencyConflict).
func TestChallenger_Idempotency_TamperedPayload_ReturnsConflict(t *testing.T) {
	r, svc, _, cat := setupTestRouter()
	userID := uuid.New()
	idemKey := "tamper-test-key-999"

	cat.AddProduct(catalog.Product{
		ID:         uuid.New(),
		SKU:        "SKU-T1",
		Title:      "Tamper Item 1",
		PriceMinor: 1000,
		Currency:   "USD",
		IsActive:   true,
	})
	cat.AddProduct(catalog.Product{
		ID:         uuid.New(),
		SKU:        "SKU-T2",
		Title:      "Tamper Item 2",
		PriceMinor: 2000,
		Currency:   "USD",
		IsActive:   true,
	})
	cat.AddProduct(catalog.Product{
		ID:         uuid.New(),
		SKU:        "SKU-T-EUR",
		Title:      "Tamper Item EUR",
		PriceMinor: 2000,
		Currency:   "EUR",
		IsActive:   true,
	})

	initialReq := CreateOrderRequest{
		Currency: "USD",
		Items: []OrderItemRequest{
			{SKU: "SKU-T1", Quantity: 2},
		},
	}
	initialRaw, _ := json.Marshal(initialReq)

	// Step 1: Create original order via HTTP
	httpReq := httptest.NewRequest(http.MethodPost, "/orders", bytes.NewReader(initialRaw))
	httpReq.Header.Set("Content-Type", "application/json")
	httpReq.Header.Set("X-User-ID", userID.String())
	httpReq.Header.Set("Idempotency-Key", idemKey)

	httpRec := httptest.NewRecorder()
	r.ServeHTTP(httpRec, httpReq)
	require.Equal(t, http.StatusCreated, httpRec.Code)

	// Step 2: Test tampered payload variants with SAME idempotency key
	tamperedCases := []struct {
		name    string
		payload CreateOrderRequest
	}{
		{
			name: "Altered_Quantity",
			payload: CreateOrderRequest{
				Currency: "USD",
				Items:    []OrderItemRequest{{SKU: "SKU-T1", Quantity: 5}},
			},
		},
		{
			name: "Altered_Currency",
			payload: CreateOrderRequest{
				Currency: "EUR",
				Items:    []OrderItemRequest{{SKU: "SKU-T-EUR", Quantity: 2}},
			},
		},
		{
			name: "Altered_SKU",
			payload: CreateOrderRequest{
				Currency: "USD",
				Items:    []OrderItemRequest{{SKU: "SKU-T2", Quantity: 2}},
			},
		},
		{
			name: "Added_Line_Item",
			payload: CreateOrderRequest{
				Currency: "USD",
				Items: []OrderItemRequest{
					{SKU: "SKU-T1", Quantity: 2},
					{SKU: "SKU-T2", Quantity: 1},
				},
			},
		},
	}

	for _, tc := range tamperedCases {
		t.Run(tc.name, func(t *testing.T) {
			rawTampered, _ := json.Marshal(tc.payload)

			// 1. Direct Service Call
			_, _, err := svc.CreateOrder(context.Background(), userID, idemKey, tc.payload, rawTampered)
			require.ErrorIs(t, err, ErrIdempotencyConflict, "service should return ErrIdempotencyConflict")

			// 2. HTTP Route Call
			tamperHttpReq := httptest.NewRequest(http.MethodPost, "/orders", bytes.NewReader(rawTampered))
			tamperHttpReq.Header.Set("Content-Type", "application/json")
			tamperHttpReq.Header.Set("X-User-ID", userID.String())
			tamperHttpReq.Header.Set("Idempotency-Key", idemKey)

			tamperHttpRec := httptest.NewRecorder()
			r.ServeHTTP(tamperHttpRec, tamperHttpReq)

			assert.Equal(t, http.StatusConflict, tamperHttpRec.Code, "HTTP response code must be 409 Conflict")
			assert.Equal(t, "application/problem+json", tamperHttpRec.Header().Get("Content-Type"))

			var problem map[string]any
			err = json.Unmarshal(tamperHttpRec.Body.Bytes(), &problem)
			require.NoError(t, err)
			assert.Equal(t, "IDEMPOTENCY_CONFLICT", problem["code"])
			assert.EqualValues(t, http.StatusConflict, problem["status"])
		})
	}
}

// TestChallenger_Idempotency_UserIsolation verifies that idempotency keys are strictly scoped per user:
// User A and User B using the exact same idempotency key string create distinct orders without conflict.
func TestChallenger_Idempotency_UserIsolation(t *testing.T) {
	repo := NewMockRepository()
	cat := NewMockCatalogReader()

	cat.AddProduct(catalog.Product{
		ID:         uuid.New(),
		SKU:        "SKU-SHARED",
		Title:      "Shared Key Item",
		PriceMinor: 1500,
		Currency:   "USD",
		IsActive:   true,
	})

	svc := NewService(repo, cat, nil, nil)
	sharedKey := "shared-idempotency-key"

	userA := uuid.New()
	userB := uuid.New()

	reqA := CreateOrderRequest{
		Currency: "USD",
		Items:    []OrderItemRequest{{SKU: "SKU-SHARED", Quantity: 1}},
	}
	rawA, _ := json.Marshal(reqA)

	reqB := CreateOrderRequest{
		Currency: "USD",
		Items:    []OrderItemRequest{{SKU: "SKU-SHARED", Quantity: 3}},
	}
	rawB, _ := json.Marshal(reqB)

	// User A creates order with sharedKey
	orderA, isCachedA, errA := svc.CreateOrder(context.Background(), userA, sharedKey, reqA, rawA)
	require.NoError(t, errA)
	assert.False(t, isCachedA)
	require.NotNil(t, orderA)
	assert.Equal(t, userA, orderA.UserID)

	// User B creates order with SAME sharedKey
	orderB, isCachedB, errB := svc.CreateOrder(context.Background(), userB, sharedKey, reqB, rawB)
	require.NoError(t, errB, "User B must succeed even with same idempotency key string")
	assert.False(t, isCachedB)
	require.NotNil(t, orderB)
	assert.Equal(t, userB, orderB.UserID)

	// Must be two distinct orders
	assert.NotEqual(t, orderA.ID, orderB.ID, "orders must have different IDs")
	assert.Equal(t, int64(1500), orderA.TotalAmountMinor)
	assert.Equal(t, int64(4500), orderB.TotalAmountMinor)

	repo.mu.Lock()
	assert.Len(t, repo.orders, 2, "exactly 2 orders must be saved in the database")
	repo.mu.Unlock()
}

// TestChallenger_Idempotency_ThunderingHerd_Races verifies race conditions when 50 concurrent
// requests submit the exact same idempotency key and payload simultaneously.
func TestChallenger_Idempotency_ThunderingHerd_Races(t *testing.T) {
	defer goleak.VerifyNone(t)
	repo := NewMockRepository()
	cat := NewMockCatalogReader()

	cat.AddProduct(catalog.Product{
		ID:         uuid.New(),
		SKU:        "SKU-RACE",
		Title:      "Race Test Item",
		PriceMinor: 500,
		Currency:   "USD",
		IsActive:   true,
	})

	svc := NewService(repo, cat, nil, nil)
	userID := uuid.New()
	idemKey := "thundering-herd-key-race"

	req := CreateOrderRequest{
		Currency: "USD",
		Items:    []OrderItemRequest{{SKU: "SKU-RACE", Quantity: 2}},
	}
	raw, _ := json.Marshal(req)

	concurrency := 50
	var wg sync.WaitGroup
	wg.Add(concurrency)

	var createdCount atomic.Int32
	var cachedCount atomic.Int32
	var conflictCount atomic.Int32
	var otherErrors atomic.Int32

	startGate := make(chan struct{})

	for i := 0; i < concurrency; i++ {
		go func() {
			defer wg.Done()
			<-startGate

			ord, isCached, err := svc.CreateOrder(context.Background(), userID, idemKey, req, raw)
			if err == nil {
				if isCached {
					cachedCount.Add(1)
				} else {
					createdCount.Add(1)
				}
				require.NotNil(t, ord)
			} else if err == ErrConcurrentProcessing || err == ErrIdempotencyConflict {
				conflictCount.Add(1)
			} else {
				otherErrors.Add(1)
			}
		}()
	}

	close(startGate)
	wg.Wait()

	assert.Equal(t, int32(0), otherErrors.Load(), "Zero unexpected errors")
	assert.Equal(t, int32(1), createdCount.Load(), "EXACTLY ONE order must be created across all concurrent requests")

	totalHandled := createdCount.Load() + cachedCount.Load() + conflictCount.Load()
	assert.Equal(t, int32(concurrency), totalHandled, "All concurrent requests must be accounted for")

	// Verify persistence invariant: exactly 1 order in the repository
	repo.mu.Lock()
	assert.Len(t, repo.orders, 1, "Only 1 order must ever exist in persistence")
	assert.Len(t, repo.outboxMessages, 1, "Only 1 outbox message must ever be created")
	repo.mu.Unlock()
}

// TestChallenger_MonetaryIntegrity_AST_Scan verifies that float32 and float64 are completely
// absent from the entire internal/domain/order package.
func TestChallenger_MonetaryIntegrity_AST_Scan(t *testing.T) {
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

// TestChallenger_MonetaryArithmetic_OverflowGuards checks boundary cases for int64 minor money calculations.
func TestChallenger_MonetaryArithmetic_OverflowGuards(t *testing.T) {
	// 1. Extreme multiplication overflow
	orderMult := &Order{
		Items: []OrderItem{
			{
				SKU:            "SKU-OV-1",
				Quantity:       10,
				UnitPriceMinor: math.MaxInt64 / 5, // (MaxInt64 / 5) * 10 overflows MaxInt64
			},
		},
	}
	err := orderMult.CalculateTotals()
	assert.ErrorIs(t, err, ErrArithmeticOverflow)

	// 2. Extreme summation overflow
	orderSum := &Order{
		Items: []OrderItem{
			{
				SKU:            "SKU-SUM-1",
				Quantity:       1,
				UnitPriceMinor: math.MaxInt64 - 50,
			},
			{
				SKU:            "SKU-SUM-2",
				Quantity:       1,
				UnitPriceMinor: 100,
			},
		},
	}
	err = orderSum.CalculateTotals()
	assert.ErrorIs(t, err, ErrArithmeticOverflow)
}
