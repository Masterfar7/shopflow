package inventory_test

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"

	"shopflow/internal/domain/inventory"
	"shopflow/internal/platform/web"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func setupTestRouter() (chi.Router, *inventory.Service, *memoryInventoryRepo) {
	svc, repo := setupInventoryService()
	handler := inventory.NewHandler(svc, nil)

	r := chi.NewRouter()
	r.Mount("/api/v1/inventory", handler.Routes())
	return r, svc, repo
}

func TestHandler_GetInventory_Success(t *testing.T) {
	r, _, repo := setupTestRouter()
	ctx := t.Context()

	_, err := repo.UpsertStock(ctx, "SKU-HDLR-1", 100)
	require.NoError(t, err)

	req := httptest.NewRequest(http.MethodGet, "/api/v1/inventory/SKU-HDLR-1", nil)
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)

	require.Equal(t, http.StatusOK, rec.Code)
	assert.Equal(t, `"1"`, rec.Header().Get("ETag"))

	var item inventory.Item
	err = json.Unmarshal(rec.Body.Bytes(), &item)
	require.NoError(t, err)
	assert.Equal(t, "SKU-HDLR-1", item.SKU)
	assert.Equal(t, 100, item.OnHand)
	assert.Equal(t, 0, item.Reserved)
	assert.Equal(t, 100, item.Available)
}

func TestHandler_GetInventory_NotFound(t *testing.T) {
	r, _, _ := setupTestRouter()

	req := httptest.NewRequest(http.MethodGet, "/api/v1/inventory/NONEXISTENT-SKU", nil)
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)

	require.Equal(t, http.StatusNotFound, rec.Code)
	assert.Equal(t, "application/problem+json", rec.Header().Get("Content-Type"))

	var prob web.ProblemDetails
	err := json.Unmarshal(rec.Body.Bytes(), &prob)
	require.NoError(t, err)
	assert.Equal(t, "SKU_NOT_FOUND", prob.Code)
	assert.Equal(t, 404, prob.Status)
	assert.Contains(t, prob.Detail, "NONEXISTENT-SKU")
}

func TestHandler_ReplenishInventory_Success(t *testing.T) {
	r, _, _ := setupTestRouter()

	body := []byte(`{"quantity": 75, "reference_id": "PO-TEST-1"}`)
	req := httptest.NewRequest(http.MethodPost, "/api/v1/inventory/SKU-REPL-H/replenish", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)

	require.Equal(t, http.StatusOK, rec.Code)
	assert.Equal(t, `"1"`, rec.Header().Get("ETag"))

	var item inventory.Item
	err := json.Unmarshal(rec.Body.Bytes(), &item)
	require.NoError(t, err)
	assert.Equal(t, "SKU-REPL-H", item.SKU)
	assert.Equal(t, 75, item.OnHand)
	assert.Equal(t, 0, item.Reserved)
	assert.Equal(t, 75, item.Available)
}

func TestHandler_RestockInventory_OpenAPI_Success(t *testing.T) {
	r, _, _ := setupTestRouter()

	// Dual route: POST /api/v1/inventory/{sku}/restock
	body := []byte(`{"quantity": 40}`)
	req := httptest.NewRequest(http.MethodPost, "/api/v1/inventory/SKU-RESTOCK-H/restock", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)

	require.Equal(t, http.StatusOK, rec.Code)

	var item inventory.Item
	err := json.Unmarshal(rec.Body.Bytes(), &item)
	require.NoError(t, err)
	assert.Equal(t, "SKU-RESTOCK-H", item.SKU)
	assert.Equal(t, 40, item.OnHand)
}

func TestHandler_ReplenishInventory_InvalidQuantity(t *testing.T) {
	r, _, _ := setupTestRouter()

	testCases := []struct {
		name string
		body string
	}{
		{"zero_quantity", `{"quantity": 0}`},
		{"negative_quantity", `{"quantity": -10}`},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodPost, "/api/v1/inventory/SKU-BAD-QTY/replenish", bytes.NewReader([]byte(tc.body)))
			req.Header.Set("Content-Type", "application/json")
			rec := httptest.NewRecorder()
			r.ServeHTTP(rec, req)

			require.Equal(t, http.StatusBadRequest, rec.Code)
			var prob web.ProblemDetails
			err := json.Unmarshal(rec.Body.Bytes(), &prob)
			require.NoError(t, err)
			assert.Equal(t, "INVALID_QUANTITY", prob.Code)
		})
	}
}

func TestHandler_ReplenishInventory_MalformedJSON(t *testing.T) {
	r, _, _ := setupTestRouter()

	req := httptest.NewRequest(http.MethodPost, "/api/v1/inventory/SKU-MAL/replenish", bytes.NewReader([]byte(`invalid json`)))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)

	require.Equal(t, http.StatusBadRequest, rec.Code)
	var prob web.ProblemDetails
	err := json.Unmarshal(rec.Body.Bytes(), &prob)
	require.NoError(t, err)
	assert.Equal(t, "INVALID_REQUEST", prob.Code)
}

func TestHandler_ReserveStock_Success(t *testing.T) {
	r, _, repo := setupTestRouter()
	ctx := t.Context()

	_, _ = repo.UpsertStock(ctx, "SKU-RES-H", 50)

	orderID := uuid.New()
	resID := uuid.New()
	reqBody, _ := json.Marshal(inventory.ReserveStockRequest{
		ReservationID: resID,
		OrderID:       orderID,
		Items: []inventory.StockItemRequest{
			{SKU: "SKU-RES-H", Quantity: 10},
		},
		TTLSeconds: 900,
	})

	req := httptest.NewRequest(http.MethodPost, "/api/v1/inventory/reserve", bytes.NewReader(reqBody))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)

	require.Equal(t, http.StatusOK, rec.Code)

	var resResult inventory.ReserveStockResult
	err := json.Unmarshal(rec.Body.Bytes(), &resResult)
	require.NoError(t, err)
	assert.Equal(t, resID, resResult.ReservationID)
	assert.Equal(t, orderID, resResult.OrderID)
	assert.Equal(t, inventory.ReservationStatusPending, resResult.Status)
}

func TestHandler_ReserveStock_InsufficientStock(t *testing.T) {
	r, _, repo := setupTestRouter()
	ctx := t.Context()

	_, _ = repo.UpsertStock(ctx, "SKU-SCARCE", 3)

	reqBody, _ := json.Marshal(inventory.ReserveStockRequest{
		OrderID: uuid.New(),
		Items: []inventory.StockItemRequest{
			{SKU: "SKU-SCARCE", Quantity: 10},
		},
	})

	req := httptest.NewRequest(http.MethodPost, "/api/v1/inventory/reserve", bytes.NewReader(reqBody))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)

	require.Equal(t, http.StatusConflict, rec.Code)
	assert.Equal(t, "application/problem+json", rec.Header().Get("Content-Type"))

	var prob web.ProblemDetails
	err := json.Unmarshal(rec.Body.Bytes(), &prob)
	require.NoError(t, err)
	assert.Equal(t, "INSUFFICIENT_STOCK", prob.Code)
	assert.Contains(t, prob.Detail, "SKU-SCARCE")
}

func TestHandler_ReserveStock_DuplicateConflict(t *testing.T) {
	r, _, repo := setupTestRouter()
	ctx := t.Context()

	_, _ = repo.UpsertStock(ctx, "SKU-DUP-H", 50)

	orderID := uuid.New()
	resID := uuid.New()
	reqBody, _ := json.Marshal(inventory.ReserveStockRequest{
		ReservationID: resID,
		OrderID:       orderID,
		Items: []inventory.StockItemRequest{
			{SKU: "SKU-DUP-H", Quantity: 10},
		},
		TTLSeconds: 900,
	})

	// First request succeeds
	req1 := httptest.NewRequest(http.MethodPost, "/api/v1/inventory/reserve", bytes.NewReader(reqBody))
	req1.Header.Set("Content-Type", "application/json")
	rec1 := httptest.NewRecorder()
	r.ServeHTTP(rec1, req1)
	require.Equal(t, http.StatusOK, rec1.Code)

	// Second request with same reservationID returns 409 Conflict
	req2 := httptest.NewRequest(http.MethodPost, "/api/v1/inventory/reserve", bytes.NewReader(reqBody))
	req2.Header.Set("Content-Type", "application/json")
	rec2 := httptest.NewRecorder()
	r.ServeHTTP(rec2, req2)

	require.Equal(t, http.StatusConflict, rec2.Code)
	var prob web.ProblemDetails
	err := json.Unmarshal(rec2.Body.Bytes(), &prob)
	require.NoError(t, err)
	assert.Equal(t, "DUPLICATE_RESERVATION", prob.Code)
	assert.Equal(t, http.StatusConflict, prob.Status)
}

func TestHandler_ReserveStock_EmptyItems(t *testing.T) {
	r, _, _ := setupTestRouter()

	reqBody, _ := json.Marshal(inventory.ReserveStockRequest{
		OrderID: uuid.New(),
		Items:   []inventory.StockItemRequest{},
	})

	req := httptest.NewRequest(http.MethodPost, "/api/v1/inventory/reserve", bytes.NewReader(reqBody))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)

	require.Equal(t, http.StatusBadRequest, rec.Code)
	var prob web.ProblemDetails
	err := json.Unmarshal(rec.Body.Bytes(), &prob)
	require.NoError(t, err)
	assert.Equal(t, "EMPTY_ITEMS", prob.Code)
}

func TestHandler_CommitStock_Success(t *testing.T) {
	r, svc, repo := setupTestRouter()
	ctx := t.Context()

	_, _ = repo.UpsertStock(ctx, "SKU-CMT", 50)
	resID := uuid.New()
	_, err := svc.ReserveStock(ctx, inventory.ReserveStockRequest{
		ReservationID: resID,
		OrderID:       uuid.New(),
		Items: []inventory.StockItemRequest{
			{SKU: "SKU-CMT", Quantity: 5},
		},
	})
	require.NoError(t, err)

	url := fmt.Sprintf("/api/v1/inventory/reservations/%s/commit", resID.String())
	req := httptest.NewRequest(http.MethodPost, url, nil)
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)

	require.Equal(t, http.StatusOK, rec.Code)
	var resp inventory.StatusResponse
	err = json.Unmarshal(rec.Body.Bytes(), &resp)
	require.NoError(t, err)
	assert.Equal(t, inventory.ReservationStatusCommitted, resp.Status)
}

func TestHandler_CommitStock_NotFound(t *testing.T) {
	r, _, _ := setupTestRouter()

	url := fmt.Sprintf("/api/v1/inventory/reservations/%s/commit", uuid.New().String())
	req := httptest.NewRequest(http.MethodPost, url, nil)
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)

	require.Equal(t, http.StatusNotFound, rec.Code)
	var prob web.ProblemDetails
	err := json.Unmarshal(rec.Body.Bytes(), &prob)
	require.NoError(t, err)
	assert.Equal(t, "RESERVATION_NOT_FOUND", prob.Code)
}

func TestHandler_CommitStock_InvalidUUID(t *testing.T) {
	r, _, _ := setupTestRouter()

	req := httptest.NewRequest(http.MethodPost, "/api/v1/inventory/reservations/not-a-valid-uuid/commit", nil)
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)

	require.Equal(t, http.StatusBadRequest, rec.Code)
	var prob web.ProblemDetails
	err := json.Unmarshal(rec.Body.Bytes(), &prob)
	require.NoError(t, err)
	assert.Equal(t, "INVALID_UUID", prob.Code)
}

func TestHandler_ReleaseStock_Success(t *testing.T) {
	r, svc, repo := setupTestRouter()
	ctx := t.Context()

	_, _ = repo.UpsertStock(ctx, "SKU-REL", 50)
	resID := uuid.New()
	_, err := svc.ReserveStock(ctx, inventory.ReserveStockRequest{
		ReservationID: resID,
		OrderID:       uuid.New(),
		Items: []inventory.StockItemRequest{
			{SKU: "SKU-REL", Quantity: 5},
		},
	})
	require.NoError(t, err)

	url := fmt.Sprintf("/api/v1/inventory/reservations/%s/release", resID.String())
	body := []byte(`{"reason": "customer cancellation"}`)
	req := httptest.NewRequest(http.MethodPost, url, bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)

	require.Equal(t, http.StatusOK, rec.Code)
	var resp inventory.StatusResponse
	err = json.Unmarshal(rec.Body.Bytes(), &resp)
	require.NoError(t, err)
	assert.Equal(t, inventory.ReservationStatusReleased, resp.Status)
}

func TestHandler_ReleaseStock_NotFound(t *testing.T) {
	r, _, _ := setupTestRouter()

	url := fmt.Sprintf("/api/v1/inventory/reservations/%s/release", uuid.New().String())
	req := httptest.NewRequest(http.MethodPost, url, nil)
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)

	require.Equal(t, http.StatusNotFound, rec.Code)
	var prob web.ProblemDetails
	err := json.Unmarshal(rec.Body.Bytes(), &prob)
	require.NoError(t, err)
	assert.Equal(t, "RESERVATION_NOT_FOUND", prob.Code)
}

func TestHandler_ReleaseStock_InvalidUUID(t *testing.T) {
	r, _, _ := setupTestRouter()

	req := httptest.NewRequest(http.MethodPost, "/api/v1/inventory/reservations/bad-uuid/release", nil)
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)

	require.Equal(t, http.StatusBadRequest, rec.Code)
	var prob web.ProblemDetails
	err := json.Unmarshal(rec.Body.Bytes(), &prob)
	require.NoError(t, err)
	assert.Equal(t, "INVALID_UUID", prob.Code)
}
