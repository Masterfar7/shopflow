package order

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"

	"shopflow/internal/domain/catalog"
	"shopflow/internal/platform/web"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func setupTestRouter() (*chi.Mux, *Service, *MockRepository, *MockCatalogReader) {
	repo := NewMockRepository()
	cat := NewMockCatalogReader()
	cat.AddProduct(catalog.Product{
		ID:         uuid.New(),
		SKU:        "SKU-H1",
		Title:      "Handler Item 1",
		PriceMinor: 2000,
		Currency:   "USD",
		IsActive:   true,
	})
	cat.AddProduct(catalog.Product{
		ID:         uuid.New(),
		SKU:        "SKU-H2",
		Title:      "Handler Item 2",
		PriceMinor: 3000,
		Currency:   "USD",
		IsActive:   true,
	})

	svc := NewService(repo, cat, nil, nil)
	handler := NewHandler(svc, nil)

	r := chi.NewRouter()
	r.Mount("/orders", handler.Routes())

	return r, svc, repo, cat
}

func TestHandler_CreateOrder_Unauthorized(t *testing.T) {
	r, _, _, _ := setupTestRouter()

	body := []byte(`{"currency":"USD","items":[{"sku":"SKU-H1","quantity":1}]}`)
	req := httptest.NewRequest(http.MethodPost, "/orders", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Idempotency-Key", "k1")
	// Omit X-User-ID

	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	assert.Equal(t, http.StatusUnauthorized, w.Code)
	var prob web.ProblemDetails
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &prob))
	assert.Equal(t, "UNAUTHORIZED", prob.Code)
}

func TestHandler_CreateOrder_MissingIdempotencyKey(t *testing.T) {
	r, _, _, _ := setupTestRouter()

	body := []byte(`{"currency":"USD","items":[{"sku":"SKU-H1","quantity":1}]}`)
	req := httptest.NewRequest(http.MethodPost, "/orders", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-User-ID", uuid.New().String())
	// Omit Idempotency-Key

	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	assert.Equal(t, http.StatusBadRequest, w.Code)
	var prob web.ProblemDetails
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &prob))
	assert.Equal(t, "MISSING_IDEMPOTENCY_KEY", prob.Code)
}

func TestHandler_CreateOrder_InvalidJSON(t *testing.T) {
	r, _, _, _ := setupTestRouter()

	req := httptest.NewRequest(http.MethodPost, "/orders", bytes.NewReader([]byte("{invalid-json")))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-User-ID", uuid.New().String())
	req.Header.Set("Idempotency-Key", "k-invalid")

	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	assert.Equal(t, http.StatusBadRequest, w.Code)
	var prob web.ProblemDetails
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &prob))
	assert.Equal(t, "INVALID_REQUEST_BODY", prob.Code)
}

func TestHandler_CreateOrder_Success_And_CachedReplay(t *testing.T) {
	r, _, _, _ := setupTestRouter()
	userID := uuid.New().String()
	idemKey := "idem-test-success"

	orderReq := CreateOrderRequest{
		Currency: "USD",
		Items: []OrderItemRequest{
			{SKU: "SKU-H1", Quantity: 2},
			{SKU: "SKU-H2", Quantity: 1},
		},
	}
	body, _ := json.Marshal(orderReq)

	// 1. Initial Request -> 201 Created
	req1 := httptest.NewRequest(http.MethodPost, "/orders", bytes.NewReader(body))
	req1.Header.Set("Content-Type", "application/json")
	req1.Header.Set("X-User-ID", userID)
	req1.Header.Set("Idempotency-Key", idemKey)

	w1 := httptest.NewRecorder()
	r.ServeHTTP(w1, req1)

	assert.Equal(t, http.StatusCreated, w1.Code)
	var order1 Order
	require.NoError(t, json.Unmarshal(w1.Body.Bytes(), &order1))
	assert.Equal(t, int64(7000), order1.TotalAmountMinor) // 2x2000 + 1x3000 = 7000
	assert.Len(t, order1.Items, 2)
	assert.Equal(t, fmt.Sprintf("/api/v1/orders/%s", order1.ID), w1.Header().Get("Location"))

	// 2. Exact Duplicate Request with Same Idempotency-Key -> 200 OK
	req2 := httptest.NewRequest(http.MethodPost, "/orders", bytes.NewReader(body))
	req2.Header.Set("Content-Type", "application/json")
	req2.Header.Set("X-User-ID", userID)
	req2.Header.Set("Idempotency-Key", idemKey)

	w2 := httptest.NewRecorder()
	r.ServeHTTP(w2, req2)

	assert.Equal(t, http.StatusOK, w2.Code)
	var order2 Order
	require.NoError(t, json.Unmarshal(w2.Body.Bytes(), &order2))
	assert.Equal(t, order1.ID, order2.ID)
	assert.Equal(t, order1.TotalAmountMinor, order2.TotalAmountMinor)

	// 3. Request with Same Idempotency-Key but modified payload -> 409 Conflict
	tamperedReq := CreateOrderRequest{
		Currency: "USD",
		Items: []OrderItemRequest{
			{SKU: "SKU-H1", Quantity: 10},
		},
	}
	tamperedBody, _ := json.Marshal(tamperedReq)

	req3 := httptest.NewRequest(http.MethodPost, "/orders", bytes.NewReader(tamperedBody))
	req3.Header.Set("Content-Type", "application/json")
	req3.Header.Set("X-User-ID", userID)
	req3.Header.Set("Idempotency-Key", idemKey)

	w3 := httptest.NewRecorder()
	r.ServeHTTP(w3, req3)

	assert.Equal(t, http.StatusConflict, w3.Code)
	var prob web.ProblemDetails
	require.NoError(t, json.Unmarshal(w3.Body.Bytes(), &prob))
	assert.Equal(t, "IDEMPOTENCY_CONFLICT", prob.Code)
}

func TestHandler_CreateOrder_ValidationErrors(t *testing.T) {
	r, _, _, _ := setupTestRouter()
	userID := uuid.New().String()

	tests := []struct {
		name       string
		req        CreateOrderRequest
		expected   int
		expectCode string
	}{
		{
			name: "empty items",
			req: CreateOrderRequest{
				Currency: "USD",
				Items:    nil,
			},
			expected:   http.StatusBadRequest,
			expectCode: "EMPTY_ORDER_ITEMS",
		},
		{
			name: "invalid quantity",
			req: CreateOrderRequest{
				Currency: "USD",
				Items:    []OrderItemRequest{{SKU: "SKU-H1", Quantity: 0}},
			},
			expected:   http.StatusBadRequest,
			expectCode: "INVALID_QUANTITY",
		},
		{
			name: "product not found",
			req: CreateOrderRequest{
				Currency: "USD",
				Items:    []OrderItemRequest{{SKU: "SKU-DOES-NOT-EXIST", Quantity: 1}},
			},
			expected:   http.StatusNotFound,
			expectCode: "PRODUCT_NOT_FOUND",
		},
		{
			name: "currency mismatch",
			req: CreateOrderRequest{
				Currency: "EUR",
				Items:    []OrderItemRequest{{SKU: "SKU-H1", Quantity: 1}},
			},
			expected:   http.StatusBadRequest,
			expectCode: "CURRENCY_MISMATCH",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			b, _ := json.Marshal(tc.req)
			httpReq := httptest.NewRequest(http.MethodPost, "/orders", bytes.NewReader(b))
			httpReq.Header.Set("Content-Type", "application/json")
			httpReq.Header.Set("X-User-ID", userID)
			httpReq.Header.Set("Idempotency-Key", uuid.New().String())

			w := httptest.NewRecorder()
			r.ServeHTTP(w, httpReq)

			assert.Equal(t, tc.expected, w.Code)
			var prob web.ProblemDetails
			require.NoError(t, json.Unmarshal(w.Body.Bytes(), &prob))
			assert.Equal(t, tc.expectCode, prob.Code)
		})
	}
}

func TestHandler_GetOrder(t *testing.T) {
	r, svc, _, _ := setupTestRouter()
	user1 := uuid.New()
	user2 := uuid.New()

	order, _, err := svc.CreateOrder(context.Background(), user1, "get-key-1", CreateOrderRequest{
		Currency: "USD",
		Items:    []OrderItemRequest{{SKU: "SKU-H1", Quantity: 1}},
	}, nil)
	require.NoError(t, err)

	// Success
	req1 := httptest.NewRequest(http.MethodGet, "/orders/"+order.ID.String(), nil)
	req1.Header.Set("X-User-ID", user1.String())
	w1 := httptest.NewRecorder()
	r.ServeHTTP(w1, req1)

	assert.Equal(t, http.StatusOK, w1.Code)
	var got Order
	require.NoError(t, json.Unmarshal(w1.Body.Bytes(), &got))
	assert.Equal(t, order.ID, got.ID)

	// Forbidden: user2 tries to access user1's order
	req2 := httptest.NewRequest(http.MethodGet, "/orders/"+order.ID.String(), nil)
	req2.Header.Set("X-User-ID", user2.String())
	w2 := httptest.NewRecorder()
	r.ServeHTTP(w2, req2)

	assert.Equal(t, http.StatusForbidden, w2.Code)
	var prob2 web.ProblemDetails
	require.NoError(t, json.Unmarshal(w2.Body.Bytes(), &prob2))
	assert.Equal(t, "FORBIDDEN", prob2.Code)

	// Not Found
	req3 := httptest.NewRequest(http.MethodGet, "/orders/"+uuid.New().String(), nil)
	req3.Header.Set("X-User-ID", user1.String())
	w3 := httptest.NewRecorder()
	r.ServeHTTP(w3, req3)

	assert.Equal(t, http.StatusNotFound, w3.Code)

	// Invalid UUID
	req4 := httptest.NewRequest(http.MethodGet, "/orders/not-a-valid-uuid", nil)
	req4.Header.Set("X-User-ID", user1.String())
	w4 := httptest.NewRecorder()
	r.ServeHTTP(w4, req4)

	assert.Equal(t, http.StatusBadRequest, w4.Code)
}

func TestHandler_ListOrders(t *testing.T) {
	r, svc, _, _ := setupTestRouter()
	user := uuid.New()

	_, _, err := svc.CreateOrder(context.Background(), user, "list-key-1", CreateOrderRequest{
		Currency: "USD",
		Items:    []OrderItemRequest{{SKU: "SKU-H1", Quantity: 1}},
	}, nil)
	require.NoError(t, err)

	req := httptest.NewRequest(http.MethodGet, "/orders", nil)
	req.Header.Set("X-User-ID", user.String())
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	assert.Equal(t, http.StatusOK, w.Code)
	var resp OrderListResponse
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &resp))
	assert.Len(t, resp.Items, 1)
}

func TestHandler_CancelOrder(t *testing.T) {
	r, svc, repo, _ := setupTestRouter()
	user := uuid.New()

	order, _, err := svc.CreateOrder(context.Background(), user, "cancel-key-1", CreateOrderRequest{
		Currency: "USD",
		Items:    []OrderItemRequest{{SKU: "SKU-H1", Quantity: 1}},
	}, nil)
	require.NoError(t, err)

	// Cancel success
	cancelBody, _ := json.Marshal(CancelOrderRequest{Reason: "Changed my mind"})
	req := httptest.NewRequest(http.MethodPost, "/orders/"+order.ID.String()+"/cancel", bytes.NewReader(cancelBody))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-User-ID", user.String())
	req.Header.Set("Idempotency-Key", "cancel-idem-1")

	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	assert.Equal(t, http.StatusOK, w.Code)
	var resp CancelOrderResponse
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &resp))
	assert.Equal(t, order.ID, resp.OrderID)
	assert.Equal(t, "CANCELLED", resp.Status)

	// Cancel terminal order returns 409 Conflict
	confirmed := &Order{
		ID:               uuid.New(),
		UserID:           user,
		Status:           StatusConfirmed,
		TotalAmountMinor: 5000,
		Currency:         "USD",
		Version:          1,
	}
	require.NoError(t, repo.CreateOrder(context.Background(), nil, confirmed))

	reqTerm := httptest.NewRequest(http.MethodPost, "/orders/"+confirmed.ID.String()+"/cancel", bytes.NewReader(cancelBody))
	reqTerm.Header.Set("Content-Type", "application/json")
	reqTerm.Header.Set("X-User-ID", user.String())
	reqTerm.Header.Set("Idempotency-Key", "cancel-idem-2")

	wTerm := httptest.NewRecorder()
	r.ServeHTTP(wTerm, reqTerm)

	assert.Equal(t, http.StatusConflict, wTerm.Code)
	var prob web.ProblemDetails
	require.NoError(t, json.Unmarshal(wTerm.Body.Bytes(), &prob))
	assert.Equal(t, "ORDER_TERMINAL", prob.Code)
}
