package cart_test

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"

	"shopflow/internal/domain/cart"
	"shopflow/internal/domain/catalog"
	"shopflow/internal/domain/money"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func setupCartTestRouter(t *testing.T) (chi.Router, *cart.Service, *mockCatalogReader) {
	repo := newMemoryCartRepo()
	reader := newMockCatalogReader()
	beginner := &mockTxBeginner{}
	svc := cart.NewService(repo, reader, beginner, nil)
	handler := cart.NewHandler(svc, nil)

	r := chi.NewRouter()
	r.Mount("/api/v1/carts", handler.CartRoutes())
	return r, svc, reader
}

func TestCartHandler_CreateAndGetCart(t *testing.T) {
	router, _, _ := setupCartTestRouter(t)
	customerID := uuid.New()

	body := []byte(fmt.Sprintf(`{"customer_id":"%s"}`, customerID.String()))
	req := httptest.NewRequest(http.MethodPost, "/api/v1/carts", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)

	require.Equal(t, http.StatusOK, rec.Code)
	assert.Equal(t, `"1"`, rec.Header().Get("ETag"))

	var c cart.Cart
	err := json.Unmarshal(rec.Body.Bytes(), &c)
	require.NoError(t, err)
	assert.Equal(t, customerID, c.UserID)
	assert.Equal(t, int64(1), c.Version)

	// GET Cart by owner
	getReq := httptest.NewRequest(http.MethodGet, fmt.Sprintf("/api/v1/carts/%s", c.ID.String()), nil)
	getReq.Header.Set("X-User-ID", customerID.String())
	getRec := httptest.NewRecorder()
	router.ServeHTTP(getRec, getReq)

	require.Equal(t, http.StatusOK, getRec.Code)
	assert.Equal(t, `"1"`, getRec.Header().Get("ETag"))

	// GET Cart by unauthorized user -> 403 Forbidden
	unauthReq := httptest.NewRequest(http.MethodGet, fmt.Sprintf("/api/v1/carts/%s", c.ID.String()), nil)
	unauthReq.Header.Set("X-User-ID", uuid.New().String())
	unauthRec := httptest.NewRecorder()
	router.ServeHTTP(unauthRec, unauthReq)
	assert.Equal(t, http.StatusForbidden, unauthRec.Code)
}

func TestCartHandler_AddItem_ETagAndIfMatch(t *testing.T) {
	router, svc, reader := setupCartTestRouter(t)
	customerID := uuid.New()

	reader.AddProduct(&catalog.Product{
		SKU:        "SKU-COFFEE-MUG",
		Title:      "Ceramic Coffee Mug",
		PriceMinor: 1499,
		Price:      money.Money{Amount: 1499, Currency: "USD"},
		IsActive:   true,
	})

	c, err := svc.CreateOrGetActiveCart(t.Context(), customerID)
	require.NoError(t, err)

	url := fmt.Sprintf("/api/v1/carts/%s/items", c.ID.String())
	itemBody := []byte(`{"sku":"SKU-COFFEE-MUG","quantity":2}`)

	// 1. Missing If-Match header -> 400 Bad Request
	req := httptest.NewRequest(http.MethodPost, url, bytes.NewReader(itemBody))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-User-ID", customerID.String())
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)
	assert.Equal(t, http.StatusBadRequest, rec.Code)

	// 2. Stale If-Match header -> 412 Precondition Failed
	req = httptest.NewRequest(http.MethodPost, url, bytes.NewReader(itemBody))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-User-ID", customerID.String())
	req.Header.Set("If-Match", `"999"`)
	rec = httptest.NewRecorder()
	router.ServeHTTP(rec, req)
	assert.Equal(t, http.StatusPreconditionFailed, rec.Code)

	// 3. Matching If-Match header -> 200 OK with ETag "2"
	req = httptest.NewRequest(http.MethodPost, url, bytes.NewReader(itemBody))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-User-ID", customerID.String())
	req.Header.Set("If-Match", `"1"`)
	rec = httptest.NewRecorder()
	router.ServeHTTP(rec, req)

	require.Equal(t, http.StatusOK, rec.Code)
	assert.Equal(t, `"2"`, rec.Header().Get("ETag"))

	var updated cart.Cart
	err = json.Unmarshal(rec.Body.Bytes(), &updated)
	require.NoError(t, err)
	assert.Equal(t, int64(2), updated.Version)
	require.Len(t, updated.Items, 1)
	assert.Equal(t, int64(2998), updated.TotalAmount.Amount)
}

func TestCartHandler_UpdateItemQuantity(t *testing.T) {
	router, svc, reader := setupCartTestRouter(t)
	customerID := uuid.New()

	reader.AddProduct(&catalog.Product{
		SKU:        "SKU-KEYBOARD",
		Title:      "Mechanical Keyboard",
		PriceMinor: 8900,
		Price:      money.Money{Amount: 8900, Currency: "USD"},
		IsActive:   true,
	})

	c, err := svc.CreateOrGetActiveCart(t.Context(), customerID)
	require.NoError(t, err)

	c, err = svc.AddItem(t.Context(), c.ID, customerID, "SKU-KEYBOARD", 1, 1)
	require.NoError(t, err)
	assert.Equal(t, int64(2), c.Version)

	url := fmt.Sprintf("/api/v1/carts/%s/items/SKU-KEYBOARD", c.ID.String())
	body := []byte(`{"quantity":3}`)

	req := httptest.NewRequest(http.MethodPut, url, bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-User-ID", customerID.String())
	req.Header.Set("If-Match", `"2"`)
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)

	require.Equal(t, http.StatusOK, rec.Code)
	assert.Equal(t, `"3"`, rec.Header().Get("ETag"))

	var updated cart.Cart
	err = json.Unmarshal(rec.Body.Bytes(), &updated)
	require.NoError(t, err)
	assert.Equal(t, 3, updated.Items[0].Quantity)
	assert.Equal(t, int64(26700), updated.TotalAmount.Amount)
}

func TestCartHandler_ClearCart(t *testing.T) {
	router, svc, reader := setupCartTestRouter(t)
	customerID := uuid.New()

	reader.AddProduct(&catalog.Product{
		SKU:        "SKU-PEN",
		PriceMinor: 100,
		Price:      money.Money{Amount: 100, Currency: "USD"},
		IsActive:   true,
	})

	c, err := svc.CreateOrGetActiveCart(t.Context(), customerID)
	require.NoError(t, err)

	c, err = svc.AddItem(t.Context(), c.ID, customerID, "SKU-PEN", 2, 1)
	require.NoError(t, err)

	url := fmt.Sprintf("/api/v1/carts/%s/clear", c.ID.String())
	req := httptest.NewRequest(http.MethodDelete, url, nil)
	req.Header.Set("X-User-ID", customerID.String())
	req.Header.Set("If-Match", `"2"`)
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)

	require.Equal(t, http.StatusNoContent, rec.Code)
}

func TestCartHandler_Authorization_MissingAndForbidden(t *testing.T) {
	router, svc, reader := setupCartTestRouter(t)
	ownerID := uuid.New()
	attackerID := uuid.New()

	reader.AddProduct(&catalog.Product{
		SKU:        "SKU-ITEM-AUTH",
		Title:      "Auth Test Item",
		PriceMinor: 1500,
		Price:      money.Money{Amount: 1500, Currency: "USD"},
		IsActive:   true,
	})

	c, err := svc.CreateOrGetActiveCart(t.Context(), ownerID)
	require.NoError(t, err)

	c, err = svc.AddItem(t.Context(), c.ID, ownerID, "SKU-ITEM-AUTH", 1, 1)
	require.NoError(t, err)

	cartID := c.ID.String()

	// 1. Missing Auth -> 401 Unauthorized
	t.Run("Missing_Auth_401", func(t *testing.T) {
		// POST /carts without customer_id or X-User-ID
		createReq := httptest.NewRequest(http.MethodPost, "/api/v1/carts", bytes.NewReader([]byte("{}")))
		createReq.Header.Set("Content-Type", "application/json")
		createRec := httptest.NewRecorder()
		router.ServeHTTP(createRec, createReq)
		assert.Equal(t, http.StatusUnauthorized, createRec.Code)
		assert.Equal(t, "application/problem+json", createRec.Header().Get("Content-Type"))

		// GET /carts/{id} with invalid X-User-ID
		getReqInv := httptest.NewRequest(http.MethodGet, fmt.Sprintf("/api/v1/carts/%s", cartID), nil)
		getReqInv.Header.Set("X-User-ID", "not-a-valid-uuid")
		getRecInv := httptest.NewRecorder()
		router.ServeHTTP(getRecInv, getReqInv)
		assert.Equal(t, http.StatusUnauthorized, getRecInv.Code)

		// GET /carts/{id} without X-User-ID
		getReq := httptest.NewRequest(http.MethodGet, fmt.Sprintf("/api/v1/carts/%s", cartID), nil)
		getRec := httptest.NewRecorder()
		router.ServeHTTP(getRec, getReq)
		assert.Equal(t, http.StatusUnauthorized, getRec.Code)

		// POST /carts/{id}/items without X-User-ID
		addReq := httptest.NewRequest(http.MethodPost, fmt.Sprintf("/api/v1/carts/%s/items", cartID), bytes.NewReader([]byte(`{"sku":"SKU-ITEM-AUTH","quantity":1}`)))
		addReq.Header.Set("Content-Type", "application/json")
		addReq.Header.Set("If-Match", `"2"`)
		addRec := httptest.NewRecorder()
		router.ServeHTTP(addRec, addReq)
		assert.Equal(t, http.StatusUnauthorized, addRec.Code)

		// PUT /carts/{id}/items/{sku} without X-User-ID
		putReq := httptest.NewRequest(http.MethodPut, fmt.Sprintf("/api/v1/carts/%s/items/SKU-ITEM-AUTH", cartID), bytes.NewReader([]byte(`{"quantity":2}`)))
		putReq.Header.Set("Content-Type", "application/json")
		putReq.Header.Set("If-Match", `"2"`)
		putRec := httptest.NewRecorder()
		router.ServeHTTP(putRec, putReq)
		assert.Equal(t, http.StatusUnauthorized, putRec.Code)

		// DELETE /carts/{id}/items/{sku} without X-User-ID
		delReq := httptest.NewRequest(http.MethodDelete, fmt.Sprintf("/api/v1/carts/%s/items/SKU-ITEM-AUTH", cartID), nil)
		delReq.Header.Set("If-Match", `"2"`)
		delRec := httptest.NewRecorder()
		router.ServeHTTP(delRec, delReq)
		assert.Equal(t, http.StatusUnauthorized, delRec.Code)

		// DELETE /carts/{id}/clear without X-User-ID
		clearReq := httptest.NewRequest(http.MethodDelete, fmt.Sprintf("/api/v1/carts/%s/clear", cartID), nil)
		clearReq.Header.Set("If-Match", `"2"`)
		clearRec := httptest.NewRecorder()
		router.ServeHTTP(clearRec, clearReq)
		assert.Equal(t, http.StatusUnauthorized, clearRec.Code)
	})

	// 2. Mismatched Customer -> 403 Forbidden
	t.Run("Mismatched_Customer_403", func(t *testing.T) {
		// GET /carts/{id} by attacker
		getReq := httptest.NewRequest(http.MethodGet, fmt.Sprintf("/api/v1/carts/%s", cartID), nil)
		getReq.Header.Set("X-User-ID", attackerID.String())
		getRec := httptest.NewRecorder()
		router.ServeHTTP(getRec, getReq)
		assert.Equal(t, http.StatusForbidden, getRec.Code)
		assert.Equal(t, "application/problem+json", getRec.Header().Get("Content-Type"))

		// POST /carts/{id}/items by attacker
		addReq := httptest.NewRequest(http.MethodPost, fmt.Sprintf("/api/v1/carts/%s/items", cartID), bytes.NewReader([]byte(`{"sku":"SKU-ITEM-AUTH","quantity":1}`)))
		addReq.Header.Set("Content-Type", "application/json")
		addReq.Header.Set("X-User-ID", attackerID.String())
		addReq.Header.Set("If-Match", `"2"`)
		addRec := httptest.NewRecorder()
		router.ServeHTTP(addRec, addReq)
		assert.Equal(t, http.StatusForbidden, addRec.Code)

		// PUT /carts/{id}/items/{sku} by attacker
		putReq := httptest.NewRequest(http.MethodPut, fmt.Sprintf("/api/v1/carts/%s/items/SKU-ITEM-AUTH", cartID), bytes.NewReader([]byte(`{"quantity":2}`)))
		putReq.Header.Set("Content-Type", "application/json")
		putReq.Header.Set("X-User-ID", attackerID.String())
		putReq.Header.Set("If-Match", `"2"`)
		putRec := httptest.NewRecorder()
		router.ServeHTTP(putRec, putReq)
		assert.Equal(t, http.StatusForbidden, putRec.Code)

		// DELETE /carts/{id}/items/{sku} by attacker
		delReq := httptest.NewRequest(http.MethodDelete, fmt.Sprintf("/api/v1/carts/%s/items/SKU-ITEM-AUTH", cartID), nil)
		delReq.Header.Set("X-User-ID", attackerID.String())
		delReq.Header.Set("If-Match", `"2"`)
		delRec := httptest.NewRecorder()
		router.ServeHTTP(delRec, delReq)
		assert.Equal(t, http.StatusForbidden, delRec.Code)

		// DELETE /carts/{id}/clear by attacker
		clearReq := httptest.NewRequest(http.MethodDelete, fmt.Sprintf("/api/v1/carts/%s/clear", cartID), nil)
		clearReq.Header.Set("X-User-ID", attackerID.String())
		clearReq.Header.Set("If-Match", `"2"`)
		clearRec := httptest.NewRecorder()
		router.ServeHTTP(clearRec, clearReq)
		assert.Equal(t, http.StatusForbidden, clearRec.Code)
	})
}

func TestCartHandler_CurrencyMismatch_BadRequest(t *testing.T) {
	router, svc, reader := setupCartTestRouter(t)
	customerID := uuid.New()

	reader.AddProduct(&catalog.Product{
		SKU:        "SKU-EUR-ITEM",
		Title:      "Euro Product",
		PriceMinor: 2000,
		Price:      money.Money{Amount: 2000, Currency: "EUR"},
		IsActive:   true,
	})

	c, err := svc.CreateOrGetActiveCart(t.Context(), customerID)
	require.NoError(t, err)

	url := fmt.Sprintf("/api/v1/carts/%s/items", c.ID.String())
	body := []byte(`{"sku":"SKU-EUR-ITEM","quantity":1}`)

	req := httptest.NewRequest(http.MethodPost, url, bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-User-ID", customerID.String())
	req.Header.Set("If-Match", `"1"`)
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)

	assert.Equal(t, http.StatusBadRequest, rec.Code)
	assert.Equal(t, "application/problem+json", rec.Header().Get("Content-Type"))

	var prob struct {
		Code   string `json:"code"`
		Detail string `json:"detail"`
	}
	err = json.Unmarshal(rec.Body.Bytes(), &prob)
	require.NoError(t, err)
	assert.Equal(t, "CURRENCY_MISMATCH", prob.Code)
}

func TestCartHandler_OCC_PreconditionFailed(t *testing.T) {
	router, svc, reader := setupCartTestRouter(t)
	customerID := uuid.New()

	reader.AddProduct(&catalog.Product{
		SKU:        "SKU-OCC-TEST",
		Title:      "OCC Test Product",
		PriceMinor: 1000,
		Price:      money.Money{Amount: 1000, Currency: "USD"},
		IsActive:   true,
	})

	c, err := svc.CreateOrGetActiveCart(t.Context(), customerID)
	require.NoError(t, err)

	url := fmt.Sprintf("/api/v1/carts/%s/items", c.ID.String())
	body := []byte(`{"sku":"SKU-OCC-TEST","quantity":1}`)

	// Stale If-Match -> 412 Precondition Failed
	req := httptest.NewRequest(http.MethodPost, url, bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-User-ID", customerID.String())
	req.Header.Set("If-Match", `"999"`)
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)

	assert.Equal(t, http.StatusPreconditionFailed, rec.Code)
	assert.Equal(t, "application/problem+json", rec.Header().Get("Content-Type"))

	// Valid If-Match -> 200 OK
	req = httptest.NewRequest(http.MethodPost, url, bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-User-ID", customerID.String())
	req.Header.Set("If-Match", `"1"`)
	rec = httptest.NewRecorder()
	router.ServeHTTP(rec, req)
	assert.Equal(t, http.StatusOK, rec.Code)

	// Update with stale If-Match "1" when version is now 2 -> 412 Precondition Failed
	updateUrl := fmt.Sprintf("/api/v1/carts/%s/items/SKU-OCC-TEST", c.ID.String())
	upReq := httptest.NewRequest(http.MethodPut, updateUrl, bytes.NewReader([]byte(`{"quantity":5}`)))
	upReq.Header.Set("Content-Type", "application/json")
	upReq.Header.Set("X-User-ID", customerID.String())
	upReq.Header.Set("If-Match", `"1"`)
	upRec := httptest.NewRecorder()
	router.ServeHTTP(upRec, upReq)
	assert.Equal(t, http.StatusPreconditionFailed, upRec.Code)

	// Remove with stale If-Match "1" -> 412 Precondition Failed
	delReq := httptest.NewRequest(http.MethodDelete, updateUrl, nil)
	delReq.Header.Set("X-User-ID", customerID.String())
	delReq.Header.Set("If-Match", `"1"`)
	delRec := httptest.NewRecorder()
	router.ServeHTTP(delRec, delReq)
	assert.Equal(t, http.StatusPreconditionFailed, delRec.Code)

	// Clear with stale If-Match "1" -> 412 Precondition Failed
	clearUrl := fmt.Sprintf("/api/v1/carts/%s/clear", c.ID.String())
	clearReq := httptest.NewRequest(http.MethodDelete, clearUrl, nil)
	clearReq.Header.Set("X-User-ID", customerID.String())
	clearReq.Header.Set("If-Match", `"1"`)
	clearRec := httptest.NewRecorder()
	router.ServeHTTP(clearRec, clearReq)
	assert.Equal(t, http.StatusPreconditionFailed, clearRec.Code)
}
